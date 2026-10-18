# Architecture

Three layers, bottom-up: pure-Nix library, NixOS module, Go daemon. The library and module produce a JSON config; the daemon consumes it and drives the kernel.

```text
┌────────────────────────────────────────────────────────────┐
│ NixOS configuration                                        │
│   services.wanwatch.{wans,groups,global}                   │
└──────────────────┬─────────────────────────────────────────┘
                   │ option-type validation
                   ▼
┌────────────────────────────────────────────────────────────┐
│ lib/ — pure Nix                                            │
│   internal/{wan,probe,group,member,global}.make / .tryMake │
│   types/primitives.{fwmark,routingTableId} validation      │
│   internal/config.render → JSON (+ consistency checks)     │
└──────────────────┬─────────────────────────────────────────┘
                   │ rendered config.json
                   ▼
┌────────────────────────────────────────────────────────────┐
│ nixos/module.nix — NixOS module                            │
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
│   │ probe[N] │──▶│ decision│───▶│ apply  │──▶ kernel       │
│   └──────────┘   │per Group│    └────────┘                 │
│   ┌──────────┐   └────┬────┘         │                     │
│   │ rtnl     │────────┘              ▼                     │
│   └──────────┘             ┌─────────────────┐             │
│                            │ state.json +    │             │
│                            │ hook runner +   │             │
│                            │ metrics socket  │             │
│                            └─────────────────┘             │
└────────────────────────────────────────────────────────────┘
```

## `lib/`

The library is imported with `{ lib, libnet }`. Every value type follows the `make`, `tryMake`, and `toJSONValue` skeleton.

```text
lib/internal/
  primitives.nix     — tryOk/tryErr, check, formatErrors
  probe.nix          — value type + threshold/hysteresis sub-types
  wan.nix            — value type; Families derived from probe.targets
  member.nix         — per-Group attributes (priority)
  group.nix          — value type, Strategy enum, mark/table validators
  global.nix         — value type for the daemon's global settings
  selector.nix       — pure Nix mirror of the Go selector
  config.nix         — JSON renderer, fixed paths, consistency checks
```

`lib/types/<name>.nix` exposes a NixOS option type for each value type, flattened as `wanwatch.types.<name>`. `wanwatch.types.fwmark` and `routingTableId` restrict the integers each Group declares to `[1000, 32767]`.

## `nixos/`

`module.nix` declares `services.wanwatch.*`, passes user input through `wanwatch.<type>.make`, runs `config.assertUniqueMarksAndTables` so no two Groups share a `mark` or `table`, and renders `/etc/wanwatch/config.json` with `config.render`, which rejects Members that reference an undeclared WAN.

It also publishes two read-only outputs that downstream consumers such as nftzones reference by name:

```nix
config.services.wanwatch.marks.<group>   # int
config.services.wanwatch.tables.<group>  # int
```

`telegraf.nix` is opt-in. When enabled, it adds an `[[inputs.prometheus]]` block to `services.telegraf.extraConfig` and adds the telegraf account to the wanwatch group.

## `daemon/`

`wanwatchd` is a single Linux-only Go binary. It needs `CAP_NET_ADMIN` for route and rule writes and `CAP_NET_RAW` for the ICMP socket.

```text
daemon/
  cmd/wanwatchd/        — process lifecycle, event loop
  internal/config/      — config.json parser + structural validator
  internal/probe/       — Pinger goroutine, ICMP wire format, WindowStats
  internal/rtnl/        — link + route subscriptions, Gateway discovery, LinkEvent dedup
  internal/health/      — per-WAN Health: thresholds, Hysteresis, Family policy, carrier
  internal/selector/    — Strategies
  internal/decision/    — per-Group Selection, Apply retries and commits
  internal/apply/       — route / rule / conntrack via vishvananda/netlink
  internal/state/       — atomic state.json writer + Hook notification delivery
  internal/metrics/     — Prometheus registry + Unix socket server
```

The event loop in `cmd/wanwatchd/eventloop.go` multiplexes three event sources:

```go
for {
    select {
    case <-ctx.Done():           return
    case r := <-probeResults:    d.handleProbeResult(ctx, r)
    case e := <-linkEvents:      d.handleLinkEvent(ctx, e)
    case e := <-routeEvents:     d.handleRouteEvent(ctx, e)
    }
}
```

Events progress through `internal/decision/group.go` as follows:

```text
ProbeResult → health.Wans.Probe ───────────────┐
LinkEvent → health.Wans.Link ─────────────────┴→ Member Health
                                                     │
                                                     ▼
                                               Group.Recompute
                                                     │
                                               selector.Select
                                                     │
                                                     ▼
ProbeResult → Group.Probe ──────────────→ pending Selection → Apply
RouteEvent → Gateway cache → Group.RouteObserved ─────┘        │
                                                             ▼
                                                   committed Selection
                                                             │
                                                        commit record
                                                             │
                                                             ▼
                                               conntrack → State → Hook
```

Link monitoring opens the live link subscription before listing existing links, so a carrier change during startup cannot fall between the snapshot and the subscription. The subscriber returns the snapshot rather than queuing it, so its channel carries only later changes. Each notification on a watched interface triggers a fresh read of that link, and the subscriber emits a LinkEvent only when the carrier or operstate differs from the last one it emitted. Buffered history therefore cannot replay a stale carrier loss into Selection. A failed read terminates the subscriber through the subsystem restart path.

The health package owns WAN Health: threshold verdicts, Hysteresis, the cold-start gate, the Family policy, carrier and operstate, and the `wan_*` and `probe_*` gauges. It folds a LinkEvent into every WAN on the interface before reporting any of them, and returns a `Change` naming the WANs whose Health flipped and whether State must be republished. The daemon recomputes the Groups of each flipped WAN and republishes State on request.

The decision package owns pending and committed Selections, Decision counts, active-Member gauges, and commit timestamps. The daemon forwards events and publishes returned commits; `Snapshot` supplies each Group's State. Strategies stay pure in `internal/selector`.

A hard Apply failure leaves the target Selection pending, and State and Hooks stay on the committed Selection. Probe cycles and route observations retry every Family for the pending target, and a newer Selection supersedes it.

A missing Gateway is a soft skip: a commit can leave that Family's existing route untouched. Every main-table default-route observation on a committed Selection's WAN rewrites the observed Family, even when its Gateway is unchanged, because the kernel flushes IPv4 routes on link down without a notification. A failed rewrite leaves a Repair due, and the WAN's next Probe cycle or route observation rewrites every Family. Neither produces a Decision or Hook ([ADR-0017](./adr/0017-repair-committed-group-table.md)).

## Families and Gateways

A WAN's Families come from the IP literals in `probe.targets`: IPv4 literals make it serve `v4`, IPv6 literals make it serve `v6`. Each Family's Health is computed independently and combined into WAN Health under `probe.familyHealthPolicy` (`all` or `any`).

Gateways are never declared by the operator. `internal/rtnl` discovers each (WAN, Family) default-route next-hop from the kernel's main routing table. The Gateway stays unknown, `null` in `state.json`, until a default route exists on the WAN's interface. See [ADR 0004](./adr/0004-runtime-gateway-discovery.md).

Interfaces without a broadcast next-hop (PPP, WireGuard, GRE, tun) set `pointToPoint = true`. The daemon then installs a `scope link` default route out of the interface and needs no Gateway.

Discovery opens the live route subscription before listing existing IPv4 and IPv6 routes, so a route installed during startup cannot fall between the snapshot and the subscription. The subscriber returns the snapshot rather than queuing it, so its channel carries only later changes. Each matching notification triggers a fresh route read for the affected interface and Family, and the subscriber emits the current default, or a deletion only when none remains. Buffered history therefore cannot replay an obsolete Gateway into Apply or State. A failed or interrupted read terminates the subscriber through the subsystem restart path.

Before the event loop handles any event, and before it reports `READY=1` to systemd, the daemon primes itself from both snapshots. It learns every Gateway first, then folds every link into Health and recomputes each affected Group once. The first Decision therefore writes the routes of every Family with a known Gateway, its Hook carries those Gateways, and each Group makes at most one startup Decision, whatever order the snapshots list the WANs in. Because neither snapshot passes through a channel, a dump larger than the channel buffer cannot block startup.

## Data flow on a switch

```text
1. carrier on wan0 drops
       │
       ▼
2. rtnl.LinkSubscriber emits LinkEvent{Name=wan0, Carrier=down}
       │
       ▼
3. health.Wans.Link sets carrier = down on every WAN on wan0
       │
       ▼
4. recomputeAffectedGroups sends current Member Health to
   each decision.Group containing wan0
       │
       ▼
5. Group.Recompute uses selector.Select and records the
   pending backup Selection and Decision count
       │
       ▼
6. Apply writes the probed Families with known Gateways.
   A hard failure defers commit until a retry succeeds
       │
       ▼
7. The Group commits backup and updates active gauges.
   The daemon flushes the vacated WAN and writes State
       │
       ▼
8. state.HookNotifier.Notify queues a switch Hook with
   the same commit timestamp as the State publication
```

The event loop submits Hook notifications after Apply and the State write. `state.HookNotifier` owns the bounded queue, serial script execution, result logs, metrics, and shutdown. Accepted events keep Decision order and their captured timestamps.

A full queue drops the newest event, so slow Hooks cannot stall routing or watchdog replies. `Close` drains delivery; cancelling the daemon context kills running scripts and discards queued events.

## Where to look

| Goal                                 | Read                                                                                                                                                                                                             |
| ------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Add a Strategy                       | `lib/internal/group.nix` (`validStrategies`), `lib/internal/selector.nix`, `daemon/internal/selector/`. `tests/unit/internal/selector.nix:testStrategiesMatchGroupValidStrategies` catches cross-language drift. |
| Change a metric                      | `daemon/internal/metrics/metrics.go`, `docs/metrics.md`                                                                                                                                                          |
| Add a Hook env var                   | `daemon/internal/state/hooks.go` (`Env*` constants), `docs/specs/daemon-state.md`                                                                                                                                |
| Tune Probe defaults                  | `lib/internal/probe.nix` (`defaults`)                                                                                                                                                                            |
| Change the daemon-config wire format | `lib/internal/config.nix`, `daemon/internal/config/config.go`, and `docs/specs/daemon-config.md`; after the first release, also bump `schemaVersion` in both                                                     |
