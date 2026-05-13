# Architecture

Three layers, bottom-up: pure-Nix library, NixOS module, Go daemon. The library and module produce a JSON config; the daemon consumes it and drives the kernel.

```
┌────────────────────────────────────────────────────────────┐
│ NixOS configuration                                        │
│   services.wanwatch.{wans,groups,global}                   │
└──────────────────┬─────────────────────────────────────────┘
                   │ option-type validation
                   ▼
┌────────────────────────────────────────────────────────────┐
│ lib/ — pure Nix                                            │
│   internal/{wan,probe,group,member}.make / .tryMake        │
│   internal/{marks,tables}.allocate (deterministic hash)    │
│   internal/config.render → JSON                            │
└──────────────────┬─────────────────────────────────────────┘
                   │ rendered config.json
                   ▼
┌────────────────────────────────────────────────────────────┐
│ modules/wanwatch.nix — NixOS module                        │
│   environment.etc."wanwatch/config.json"                   │
│   systemd.services.wanwatch (hardened unit)                │
│   users.users.wanwatch / users.groups.wanwatch             │
│   marks.<group> / tables.<group> read-only outputs         │
└──────────────────┬─────────────────────────────────────────┘
                   │ systemd starts wanwatchd
                   ▼
┌────────────────────────────────────────────────────────────┐
│ daemon/ — Go process                                       │
│                                                            │
│   config ─────────────┐                                    │
│                       ▼                                    │
│   ┌──────────┐   ┌─────────┐    ┌────────┐                 │
│   │ probe[N] │──▶│ selector│───▶│ apply  │──▶ kernel       │
│   └──────────┘   │  + hyst │    └────────┘                 │
│   ┌──────────┐   └────┬────┘         │                     │
│   │ rtnl     │────────┘              ▼                     │
│   └──────────┘             ┌─────────────────┐             │
│                            │ state.json +    │             │
│                            │ hook runner +   │             │
│                            │ metrics socket  │             │
│                            └─────────────────┘             │
└────────────────────────────────────────────────────────────┘
```

## Layers in detail

### `lib/`

Imported with `{ lib, libnet }`. Every value type follows the same minimal skeleton: `make`, `tryMake`, `toJSONValue`.

```
lib/internal/
  primitives.nix     — tryOk/tryErr, check, formatErrors
  probe.nix          — value type + threshold/hysteresis sub-types
  wan.nix            — value type; families derived from probe.targets
  member.nix         — per-Group attributes (priority, weight)
  group.nix          — value type, strategy enum, mark/table
  marks.nix          — deterministic int allocator
  tables.nix         — same, in a different range
  selector.nix       — pure Nix mirror of the Go selector
  config.nix         — JSON renderer + resolveAllocations
```

`lib/types/<name>.nix` exposes NixOS option types for each value type. `wanwatch.types.<name>` is the flattened surface module consumers reach for.

### `modules/`

`wanwatch.nix` declares `services.wanwatch.*`, rounds user input through `wanwatch.<type>.make` to get tagged values, runs `config.resolveAllocations` to auto-fill marks/tables (with cross-explicit collision detection), and renders `/etc/wanwatch/config.json`.

Cross-module outputs:

```nix
config.services.wanwatch.marks.<group>   # int
config.services.wanwatch.tables.<group>  # int
```

Both are read-only — downstream consumers like nftzones reference them by name.

`telegraf.nix` is opt-in; when enabled it pushes an `[[inputs.prometheus]]` block into `services.telegraf.extraConfig` and joins the telegraf account to the wanwatch group.

### `daemon/`

Single Go binary, `wanwatchd`. Linux-only. Capabilities: `CAP_NET_ADMIN` (route + rule writes), `CAP_NET_RAW` (ICMP socket bind).

```
daemon/
  cmd/wanwatchd/        — process lifecycle, event loop
  internal/config/      — config.json parser + structural validator
  internal/probe/       — Pinger goroutine, ICMP wire format, WindowStats
  internal/rtnl/        — RTNLGRP_LINK subscriber, LinkEvent dedup
  internal/selector/    — strategies + per-WAN hysteresis state
  internal/apply/       — route / rule / conntrack via vishvananda/netlink
  internal/state/       — atomic state.json writer + hook runner
  internal/metrics/     — Prometheus registry + Unix socket server
```

Event loop (`cmd/wanwatchd/daemon.go`):

```go
for {
    select {
    case <-ctx.Done():           return
    case r := <-probeResults:    d.handleProbeResult(r)
    case e := <-linkEvents:      d.handleLinkEvent(e)
    }
}
```

Decision pipeline (`cmd/wanwatchd/state.go`):

```
ProbeResult  ─►  evaluateThresholds  ─►  hysteresis.Observe
                                                  │
                                                  ▼
                                          combineFamilies(policy)
                                                  │
LinkEvent  ─────►  wan.carrier/operstate  ────────┤
                                                  ▼
                                          buildMemberHealth
                                                  │
                                                  ▼
                                          selector.Apply  →  Selection
                                                  │
                                                  ▼
                                          if changed:
                                            apply.WriteDefault per family
                                            state.Writer.Write
                                            state.Runner.Run (hooks)
                                            metrics.GroupDecisions++
```

## Data flow on a switch

```
1. carrier on wan0 drops
       │
       ▼
2. rtnl.Subscriber emits LinkEvent{Name=wan0, Carrier=down}
       │
       ▼
3. handleLinkEvent sets wan0.carrier = down
       │
       ▼
4. recomputeAffectedGroups runs selector for every group
   containing wan0
       │
       ▼
5. selector.Apply returns Selection{Active="backup"}
       │
       ▼
6. apply.WriteDefault writes the v4+v6 default in the
   group's table via backup's gateway
       │
       ▼
7. state.Writer.Write publishes the new state.json
       │
       ▼
8. state.Runner.Run dispatches /etc/wanwatch/hooks/switch.d/*
       │
       ▼
9. Prometheus gauges + decisions counter update
```

Steps 6-9 run in order on a single goroutine — the apply layer never races with itself.

## Where to look for what

| Goal | Read |
|---|---|
| Add a strategy | `lib/internal/group.nix` (validStrategies), `lib/internal/selector.nix`, `daemon/internal/selector/`. Cross-language drift caught by `tests/unit/internal/selector.nix:testStrategiesMatchGroupValidStrategies`. |
| Change a metric | `daemon/internal/metrics/metrics.go` + `docs/metrics.md` |
| Add a hook env var | `daemon/internal/state/hooks.go` (Env* constants) + `docs/specs/daemon-state.md` |
| Tune probe defaults | `lib/internal/probe.nix` (`defaults` attrset) |
| Change daemon-config wire format | `lib/internal/config.nix`, `daemon/internal/config/config.go`, bump `SchemaVersion` in both, update `docs/specs/daemon-config.md` |
