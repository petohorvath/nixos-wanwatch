# wanwatch — design plan (v1)

Authoritative design intent. Records the working agreement on scope,
public surface, internal layering, build sequence, and conventions.
Updated when an intended change diverges from the document.

**Status**: v0.1.0 shipped (2026-05-12). All six passes complete.
See `CHANGELOG.md` for post-release deltas and `TODO.md` for the
deferred-work backlog.

---

## 1. Context

NixOS lacks first-class multi-WAN management — probing WAN interfaces,
deciding which is healthy, selecting one as the active egress per
logical group, and switching kernel routing state on health changes.
OpenWrt's `mwan3`, pfSense's gateway-group + `dpinger`, and VyOS'
`load-balancing wan` solve this elsewhere. On NixOS the user runs
`mwan3` under OpenWrt-style configuration, writes per-deployment shell
scripts, or pairs `keepalived` health checks with custom routing logic.

`nixos-wanwatch` provides three layers:

1. A **pure-Nix library** (`lib/`) modeling WANs, probes, groups, and
   selection — same shape and discipline as `nix-libnet` and
   `nix-nftzones`.
2. A **NixOS module** (`modules/`) wiring the lib into a systemd
   service + state directory + optional Telegraf metrics integration.
3. A **Go daemon** (`daemon/`) that probes, applies the selection
   algorithm with hysteresis, mutates kernel routing state via
   netlink, and publishes observability data.

Integrates with `nix-nftzones` for the firewall marking layer. The two
projects communicate by published Nix attribute values
(`services.wanwatch.marks.<group>`, `.tables.<group>`) and a runtime
convention: the firewall sets a mark per zone via `sroute`/`droute`;
wanwatch owns the per-group routing table that mark dispatches into.

---

## 2. Goals / Non-goals (v1)

### Goals

- Single-active failover per group — at any moment, one healthy
  member of each group carries that group's traffic.
- **First-class IPv4 and IPv6.** A WAN serves one or both families
  per its `probe.targets`; next-hops are discovered at runtime via
  rtnetlink rather than declared statically. Each family is probed
  independently. On a Decision the daemon rewrites the default route
  in the v4 routing table and the v6 routing table independently,
  per-family. Per-WAN Health aggregates per-family Health under a
  configurable policy (`all` or `any` — default `all`).
- ICMP and ICMPv6 probing per WAN with sliding-window RTT, jitter,
  and loss statistics (algorithm equivalent to `dpinger`'s sample
  window).
- Threshold-based health decisions with consecutive-cycle hysteresis
  in both directions (down-to-up and up-to-down).
- Carrier / operstate integration via rtnetlink — drop a WAN
  immediately on kernel-reported carrier loss; don't wait for the
  next probe cycle.
- Per-group fwmark + routing-table-id allocation, deterministic from
  the group's name.
- Atomic state publication at `/run/wanwatch/state.json`.
- Hook script directory (`/etc/wanwatch/hooks/{up,down,switch}.d/`)
  invoked on decision events with env vars.
- Public Nix attribute outputs (`services.wanwatch.marks`,
  `services.wanwatch.tables`) for downstream consumers — primarily
  nftzones, but any nftables/iptables configuration.
- Prometheus metrics over a Unix socket; Telegraf integration
  documented and (optionally) auto-configured via a companion
  NixOS module.
- Full test discipline — unit, integration, and VM tiers; coverage
  gates that fail CI on regression.

### Non-goals (deferred to v2+)

- Multi-active groups (ECMP, weighted load balancing). The Nix data
  model accommodates them so users' v1 configs forward-compat; the
  applier path is not implemented in v1.
- TCP / HTTP / DNS probes. ICMP / ICMPv6 only in v1.
- **Per-family Selection** — independently choosing the active
  member for v4 and for v6 within the same group (e.g. v4 via
  `primary`, v6 via `backup` simultaneously). v1 produces one
  Selection per group, applied to every family the active member
  has a gateway for. Defer to v2.
- Per-flow / per-app routing. Group-level only.
- Conntrack stickiness across switches beyond a flush of the dead
  path's entries.
- Cross-host coordination (clustering, VRRP, anycast).
- Bandwidth measurement; latency-aware weight adjustment.

---

## 3. Glossary

Terms have non-overlapping meanings. Reusing them loosely is a defect.
This table lives in `docs/glossary.md` and is referenced from
`CLAUDE.md`.

| Term | Definition | Not to be confused with |
|---|---|---|
| **WAN** | An egress interface plus a Probe configuration. Serves one or two IP families depending on `probe.targets`; next-hops are discovered at runtime via netlink. The atomic monitored unit. | Group, Member |
| **Probe** | Configuration of how to test a WAN — targets, method, interval, thresholds, hysteresis. | Sample |
| **Target** | A single IP being probed. A Probe has one or more Targets. | Probe |
| **Sample** | One probe attempt + result (RTT in microseconds, or `loss`). | Probe |
| **Window** | Sliding collection of recent Samples used to compute Health metrics. | Hysteresis |
| **Health** | Derived status of a WAN: `healthy` / `unhealthy` (boolean in v1). | Selection |
| **Hysteresis** | State machine suppressing flapping — consecutive cycles required to flip Health in either direction. | Window |
| **Group** | Ordered collection of Members + Strategy + Table + Mark. | WAN, Selection |
| **Member** | A WAN's *membership* in a Group, carrying per-Group attributes (weight, priority). | WAN |
| **Strategy** | Algorithm choosing active Member(s) from healthy ones. v1: `primary-backup`. | Selection |
| **Selection** | Current chosen Member(s) per Group. Output of Strategy applied to Member Health. | Decision |
| **Decision** | A Selection *change* event — old Selection → new Selection. Triggers Apply. | Selection |
| **Gateway** | Daemon-mirrored default-route next-hop for a (WAN, family). Discovered via rtnetlink from the kernel's main routing table; empty when no default exists on the WAN's interface or when the WAN is `pointToPoint`. Consumed by Apply when writing per-Group default routes. | Apply |
| **Apply** | The act of mutating kernel state (route, conntrack) to reflect a Decision. | Decision |
| **State** | Externalized view: per-WAN Health, per-Group Selection. Atomic JSON file. | Selection (sub-component) |
| **Hook** | User script invoked on Decision with structured env vars. | Apply |

---

## 4. Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│                       User's NixOS configuration                  │
│                                                                    │
│   services.wanwatch.wans.* / .groups.* / .global.*                │
└─────────────────────────────┬────────────────────────────────────┘
                              │ NixOS module evaluation
                              ▼
┌──────────────────────────────────────────────────────────────────┐
│  modules/wanwatch.nix                                              │
│    - validates via lib/types                                       │
│    - calls lib/marks.allocate, lib/tables.allocate                 │
│    - renders lib/config.toJSON → /etc/wanwatch/config.json         │
│    - emits systemd unit: ExecStart = wanwatchd --config=…          │
│    - exposes services.wanwatch.marks, .tables for downstream       │
└─────────────────────────────┬────────────────────────────────────┘
                              │ /etc/wanwatch/config.json
                              ▼
┌──────────────────────────────────────────────────────────────────┐
│  wanwatchd (Go daemon)                                             │
│                                                                    │
│   ┌──────────┐                                                     │
│   │ probe    │──┐                                                  │
│   │ (icmp+v6)│  │   ┌────────┐   ┌───────┐   ┌────────┐           │
│   └──────────┘  ├──▶│selector│──▶│ apply │──▶│ kernel │           │
│   ┌──────────┐  │   │ (pure) │   │(nlnk) │   │        │           │
│   │ rtnl     │──┘   └────┬───┘   └───────┘   └────────┘           │
│   │ events   │           │                                          │
│   └──────────┘           ├──▶ state   ──▶ /run/wanwatch/           │
│                          ├──▶ hooks   ──▶ /etc/wanwatch/           │
│                          └──▶ metrics ──▶ unix socket               │
└──────────────────────────────────────────────────────────────────┘
                              │
                              ▼
                       Telegraf scrapes /metrics
```

### Layer responsibilities

- **`lib/`** — validation, builders, predicates, and
  serialization for `wan`, `probe`, `group`, `member`. Allocators for
  fwmarks and routing-table ids. Pure selection function (used both
  by daemon tests and by module-level assertions). Takes
  `{ lib, libnet }` at import time; `nixpkgs.lib` is a standard
  dependency, used freely throughout (`lib.nameValuePair`,
  `lib.partition`, `lib.types.*`, …). NixOS option types are
  always available at `wanwatch.types`.
- **`modules/`** — thin NixOS layer. `wanwatch.nix` is the main
  entrypoint (declares options, renders config, emits systemd unit,
  creates state dir + hooks dir). `telegraf.nix` is an opt-in
  companion that pre-configures Telegraf's `inputs.prometheus`
  scrape target.
- **`daemon/`** — self-contained Go module. Seven internal packages
  (`config`, `probe`, `rtnl`, `selector`, `apply`, `state`, `metrics`)
  plus `cmd/wanwatchd` (entrypoint). Single binary, no IPC, no
  subprocess management.
- **`docs/`** — audience-targeted, matching nftzones discipline.
- **`tests/`** — three tiers (unit, integration, vm), matching
  nftzones discipline.

---

## 5. Public API surface

### 5.1 Nix lib

Every value type implements the same minimal skeleton:

```
For value type T ∈ { wan, probe, group, member }:
  make        : attrs → T              throws on invalid input
  tryMake     : attrs → tryResult T    { success; value; error; }
  toJSONValue : T → attrset            canonical attrset embedded by config.render
```

A unit test asserts that every value type exports the full skeleton;
catches "I added a type and forgot `toJSONValue`" regressions.

Pure-function modules (`selector`, `marks`, `tables`, `config`) get a
different, explicitly-documented skeleton (`compute` / `allocate` /
`render` + helpers), not a value-type skeleton. `config.toJSON` is
the only top-level "render to JSON string" — value types compose
through `toJSONValue` (attrset), and the daemon-config renderer
hands the final assembled attrset to `builtins.toJSON` once.

Module-level layout:

Layout mirrors `nix-nftzones`: per-concept files split between
`lib/internal/<name>.nix` (operational code — `make`, `tryMake`,
predicates, accessors, `toJSON`) and `lib/types/<name>.nix`
(NixOS option types). `lib/default.nix` composes both halves
and exposes convenience aliases (`wanwatch.probe`, `wanwatch.wan`)
that resolve to the operational modules.

| File | Exports |
|---|---|
| `lib/default.nix` | top-level entry; composes `internal` + `types`; exposes `probe` / `wan` aliases |
| `lib/internal/default.nix` | three-tier composition (primitives → probe → wan) |
| `lib/internal/primitives.nix` | generic helpers: `tryOk`/`tryErr`, `check`, `partitionTry`, `formatErrors`, `isValidName`, `isPositiveInt` |
| `lib/internal/probe.nix` | `probe` value type — `make`, `tryMake`, `toJSONValue`, `families` accessor |
| `lib/internal/wan.nix` | `wan` value type — `make`, `tryMake`, `toJSONValue`, `families` accessor; `pointToPoint` toggles scope-link vs gateway-discovery apply path |
| `lib/internal/group.nix` *(Pass 3)* | `group` + `member` value types |
| `lib/internal/selector.nix` *(Pass 4)* | pure `compute` + closed-set strategy registry (v1: `primary-backup`) |
| `lib/internal/marks.nix` *(Pass 3)* | `allocate : groupNames → { <group> = <mark>; … }` deterministic |
| `lib/internal/tables.nix` *(Pass 3)* | `allocate : groupNames → { <group> = <tableId>; … }` deterministic |
| `lib/internal/config.nix` *(Pass 4)* | `toDaemonJson : evaluatedConfig → string` |
| `lib/types/default.nix` | aggregates per-type option-type files via `lib.mergeAttrsList` |
| `lib/types/primitives.nix` | shared option-type primitives (Pass 5) |
| `lib/types/probe.nix` | probe-related NixOS option types (Pass 5) |
| `lib/types/wan.nix` | wan-related NixOS option types (Pass 5) |

### 5.2 NixOS module

User-facing option tree (illustrative; final schema lives in
`docs/specs/module-options.md`). Conventions:

- `wans.<name>` and `groups.<name>` derive their `name` field from
  the attribute key (read-only submodule default = `name`, mirroring
  nftzones' zone/filter pattern).
- `wans.<name>.interface` is validated via
  `libnet.types.interfaceName` (kernel-`dev_valid_name` parity).
- Probe targets are validated via
  `libnet.types.{ipv4,ipv6,ip}`. WANs no longer carry a static
  gateway declaration — the daemon discovers next-hops at runtime
  via netlink, surfaced in `state.json`.
- `strategy` is an enum; v1 accepts only `"primary-backup"` (lib
  enforces a closed set — see §5.1).

```nix
services.wanwatch = {
  enable = true;

  global = {
    statePath     = "/run/wanwatch/state.json";   # default
    hooksDir      = "/etc/wanwatch/hooks";         # default
    metricsSocket = "/run/wanwatch/metrics.sock";  # default
    logLevel      = "info";                         # info|debug|warn|error
  };

  wans.primary = {
    interface = "eth0";
    # pointToPoint = false (default) — daemon discovers the
    # gateway via netlink. Set true for PPP / WireGuard / tun.
    probe = {
      method  = "icmp";        # ICMP for v4 targets, ICMPv6 for v6 targets
      targets = {              # per-family lists; at least one non-empty
        v4 = [ "1.1.1.1" "8.8.8.8" ];
        v6 = [ "2606:4700:4700::1111" ];
      };
      familyHealthPolicy = "all";   # "all": WAN healthy iff every configured family healthy
                                    # "any": WAN healthy if any configured family healthy
      intervalMs = 500;
      timeoutMs  = 1000;
      windowSize = 10;
      thresholds = {
        lossPctDown = 30;
        lossPctUp   = 10;
        rttMsDown   = 500;
        rttMsUp     = 250;
      };
      hysteresis = {
        consecutiveDown = 3;
        consecutiveUp   = 5;
      };
    };
  };

  wans.backup = {
    interface = "wwan0";
    pointToPoint = true;           # LTE / PPP — no broadcast next-hop
    # probe.targets.v4 only → WAN serves v4 only
    # …
  };

  groups.home-uplink = {
    members = [
      { wan = "primary"; weight = 100; priority = 1; }
      { wan = "backup";  weight =  50; priority = 2; }
    ];
    strategy = "primary-backup";
    # table and mark are auto-allocated by lib/{tables,marks}.allocate
    # unless overridden explicitly:
    # table = 100; mark = 100;
  };
};
```

### 5.3 Public outputs for downstream consumers

The module assigns and exposes deterministic allocations so firewall
rules in nftzones (or hand-written nftables) can reference them
without magic numbers:

```nix
# Read-only outputs assigned by the module.
# Allocator output values shown below are illustrative — actual ints
# come from hash + linear-probe over the configured group-name set
# (see Open Question 2).
services.wanwatch.marks  = { home-uplink = 0x1A3F; guest-uplink = 0x4C82; … };
services.wanwatch.tables = { home-uplink = 6719;   guest-uplink = 19586;  … };
services.wanwatch.groups = { … };   # echo of evaluated groups
services.wanwatch.wans   = { … };   # echo of evaluated wans
```

Consumers should always reference these by name
(`config.services.wanwatch.marks.<group>`), never by hardcoded
literal — the allocator output depends on the full group-name set
and may change if groups are added or removed.

### 5.4 Family derivation from probe targets

A WAN's served families are derived from `probe.targets` — a
non-empty `targets.v4` means it serves v4, a non-empty `targets.v6`
means it serves v6. There is no separate gateway / family
declaration; the daemon discovers
the next-hop dynamically from the kernel's main routing table
(see §8 GatewayCache).

**Validators on `wan.make` / `wan.tryMake`**:

| Error kind | Triggered by |
|---|---|
| `"wanInvalidName"` | missing / non-identifier `name` |
| `"wanInvalidInterface"` | interface fails `dev_valid_name` |
| `"wanInvalidPointToPoint"` | `pointToPoint` not a bool |
| `"wanInvalidProbe"` | embedded `probe.make` rejected the config |

Probe-level constraints (e.g. "at least one target") live on the
probe value type — see `probe.tryMake` errors. Aggregation is
nftzones-style (all violations reported together via
`lib.nameValuePair`, not fail-on-first).

**Test discipline** (`tests/unit/internal/wan.nix`):

- Each error kind exercised in isolation.
- At least one multi-violation config asserting that all
  violations appear in a single error report.
- Positive case: dual-stack WAN (mixed-family targets).
- Positive case: v4-only WAN.
- Positive case: v6-only WAN.
- Positive case: `pointToPoint = true` accepted.
- `tryMake` returns `{ success = false; error = "...wanInvalidProbe..."; }`
  rather than throwing.

**Rationale**: WAN monitoring is critical infrastructure. A WAN
silently-not-probed-in-some-family is exactly the latent failure
mode this project exists to prevent. Failing loud at eval time
beats failing subtle at runtime.

### 5.5 Daemon contract

Three artifacts comprise the daemon's public contract:

1. **Config file** `/etc/wanwatch/config.json` — schema versioned
   (`schema: 1`). Written by the NixOS module, read by the daemon at
   startup. Full schema in `docs/specs/daemon-config.md`.
2. **State file** `/run/wanwatch/state.json` — schema versioned
   (`schema: 1`). Written atomically (tmpfile + rename) on any
   observable state transition — a per-family Health verdict
   flips, a WAN's carrier/operstate changes, the kernel's
   gateway-cache mutates on a watched interface, or a Decision's
   routes converge in the kernel — plus an initial publish at
   startup. Consumers (Telegraf, custom scripts, `wanwatchctl`)
   read it. Full schema in `docs/specs/daemon-state.md`.

   Per-probe-sample stats (`rttSeconds`, `jitterSeconds`,
   `lossRatio`) are *not* republished every cycle — they belong on
   the Prometheus endpoint and would otherwise turn state.json
   into a multi-write-per-second hot path. State.json snapshots
   them at each transition, which is enough for consumers that
   want a consistent "what's the daemon's current view" read but
   need to query the metrics endpoint for live trend data.
3. **Hook env vars** — when invoking `/etc/wanwatch/hooks/{up,down,switch}.d/*`:

   ```
   WANWATCH_EVENT          up | down | switch
   WANWATCH_GROUP          group name
   WANWATCH_WAN_OLD        previous active wan name (empty on first up)
   WANWATCH_WAN_NEW        new active wan name (empty on all-down)
   WANWATCH_IFACE_OLD      previous interface (empty if no prev)
   WANWATCH_IFACE_NEW      new interface
   WANWATCH_GATEWAY_V4_OLD previous v4 gateway (empty if none)
   WANWATCH_GATEWAY_V4_NEW new v4 gateway (empty if none on new active)
   WANWATCH_GATEWAY_V6_OLD previous v6 gateway (empty if none)
   WANWATCH_GATEWAY_V6_NEW new v6 gateway (empty if none on new active)
   WANWATCH_FAMILIES       comma-separated families whose default route
                           was rewritten (e.g. "v4,v6", "v4", "v6")
   WANWATCH_TABLE          routing table id (int, used for both families)
   WANWATCH_MARK           fwmark (int)
   WANWATCH_TS             ISO8601 UTC timestamp of the decision
   ```

   Hook exit status: daemon logs non-zero but does not retry or block.
   Hooks are best-effort notifications, not part of the apply
   transaction.

   Both the state file and the hooks are deferred until a Decision's
   routes have actually landed in the kernel — a hard apply failure
   holds the Decision pending and is retried on the active WAN's next
   probe result — so neither ever reports a switch the kernel has not
   made.

### 5.6 WAN multiplicity across Groups

A WAN may be a Member of more than one Group. The two relationships
are orthogonal:

- **Probe state is per-WAN, not per-Group.** A WAN runs one probe
  process per family regardless of how many Groups reference it.
  Probe Samples, Window, per-family Health, and aggregate Health are
  all WAN-scoped values, reused by every Group that has the WAN as
  a Member.
- **Selection state is per-Group.** Each Group runs its own Strategy
  over its Members' WAN Health, producing its own Selection
  independent of any other Group. Two Groups sharing the same WANs
  can have different active Members if their Strategy settings
  differ.
- **Hysteresis is per-WAN.** Consecutive-cycle counters live on the
  WAN, not the (WAN, Group) pair. A WAN that flips down via probe
  thresholds is unhealthy in every Group containing it,
  simultaneously.

Test discipline (`tests/unit/group.nix` + a VM scenario): one WAN in
two groups; each group's Selection is computed independently.

---

## 6. nftzones integration contract

The two projects communicate via Nix attribute values and a runtime
convention. **No nftzones changes are required for wanwatch v1.**

### 6.1 The contract

1. wanwatch assigns a fwmark (int) and routing-table id (int) per
   group, deterministically from the group name. Both are exposed as
   `services.wanwatch.marks.<group>` and `.tables.<group>`. The
   table id is shared across families — v4 uses `table <table>` in
   the v4 RIB, v6 uses the same `table <table>` in the v6 RIB.
2. wanwatch's daemon creates `ip rule add fwmark <mark> table <table>`
   AND `ip -6 rule add fwmark <mark> table <table>` at startup
   (idempotent — checks before adding) per group, per family.
3. wanwatch's daemon owns the *contents* of `table <table>` in both
   families — writes `default via <gw> dev <iface>` per family the
   active member has a gateway for, and rewrites on switch.
4. The user's nftzones configuration sets `meta mark set <mark>` in
   `sroute` (forwarded traffic) or `droute` (locally-generated
   traffic) rules, referencing `config.services.wanwatch.marks.<group>`.
5. Optional: the user's nftzones `snat` rule masquerades egress out of
   the WAN zone, automatically following the active interface.

### 6.2 End-to-end example

```nix
# wanwatch declarations (described in §5.2)
services.wanwatch.groups.home-uplink = { … };

# nftzones-side, referencing wanwatch's published mark:
networking.nftzones.tables.fw = {
  zones = {
    lan        = { interfaces = [ "br-lan" ]; };
    wan-home   = { interfaces = [ "eth0" "wwan0" ]; };
  };

  sroutes.lan-via-home = {
    from = [ "lan" ];
    rule = [ (mangle meta.mark config.services.wanwatch.marks.home-uplink) ];
  };

  snats.wan-home = { from = [ "lan" ]; to = [ "wan-home" ]; };
};
```

No magic numbers: mark `100` (or whatever the allocator assigned) is
referenced by name everywhere.

### 6.3 Integration tests (VM tier)

A dedicated VM scenario (`tests/vm/nftzones-integration.nix`) boots a
router with wanwatch + nftzones, induces a carrier-down on `eth0`,
asserts the LAN client's traffic switches to `wwan0` egress (verified
by destination conntrack from the LAN side).

---

## 7. Telegraf integration

### 7.1 Endpoint

Daemon exposes Prometheus-format metrics over a Unix socket
(default `/run/wanwatch/metrics.sock`). HTTP on a Unix socket avoids
exposing anything on the network — a routing-box concern.

Implementation: `prometheus/client_golang` + `net.Listen("unix", …)`
inside the daemon.

### 7.2 Metrics catalog (v1)

```
# Probe layer — family ∈ {v4, v6}
wanwatch_probe_rtt_seconds{wan,target,family}          gauge
wanwatch_probe_jitter_seconds{wan,family}              gauge (per-family aggregate)
wanwatch_probe_loss_ratio{wan,family}                  gauge (0.0–1.0, per-family aggregate)

# WAN layer — carrier/operstate are family-agnostic; health is per-family + aggregate
wanwatch_wan_carrier{wan}                              gauge (0|1)
wanwatch_wan_operstate{wan}                            gauge (IFLA_OPERSTATE value)
wanwatch_wan_family_healthy{wan,family}                gauge (0|1, per-family decision)
wanwatch_wan_healthy{wan}                              gauge (0|1, aggregate per policy)
wanwatch_wan_carrier_changes_total{wan}                counter

# Group layer
wanwatch_group_active{group,wan}                       gauge (1 for active, 0 others)
wanwatch_group_decisions_total{group,reason}           counter
                                                       reason ∈ {health,carrier}

# Apply layer — split per-family vs family-agnostic ops to avoid empty labels
wanwatch_apply_route_duration_seconds{group,family}    histogram (per-family RTM_NEWROUTE)
wanwatch_apply_route_errors_total{group,family}        counter
wanwatch_apply_op_errors_total{group,op}               counter
                                                       op ∈ {conntrack_flush,ifindex_lookup,
                                                             rule_install}

# Daemon
wanwatch_state_publications_total                      counter
wanwatch_hook_invocations_total{event,result}          counter
                                                       result ∈ {ok,nonzero,timeout}
wanwatch_build_info{version,go_version,commit}         gauge (always 1)
```

Catalog lives in `docs/metrics.md` with descriptions, units, and
example PromQL.

### 7.3 Telegraf NixOS module (optional)

`modules/telegraf.nix` is opt-in. When enabled alongside
`services.telegraf.enable`, it adds:

```toml
[[inputs.prometheus]]
  urls = ["unix:///run/wanwatch/metrics.sock:/metrics"]
  interval = "10s"
  namepass = ["wanwatch_*"]
```

Users not running Telegraf don't import this module; the daemon's
metrics endpoint works the same regardless.

---

## 8. Daemon spec

Single Go binary, `wanwatchd`. Estimated v1 size: 1500–2500 LOC.

Package responsibilities:

### `internal/config`

Parses `/etc/wanwatch/config.json` produced by the NixOS module.
Validates schema version. Performs a second pass of structural
validation (the Nix-side validation is authoritative; this catches
hand-edited or fuzzed configs). Exposes immutable typed values to
the rest of the daemon.

### `internal/probe`

ICMP / ICMPv6 echo client using `golang.org/x/net/icmp` (same
package handles both families via different `IPProto` values). One
probe goroutine per (WAN, family) tuple; each rotates through that
family's Targets at the configured interval. Maintains a sliding
Window of Samples per Target; aggregates Targets into per-family
WAN statistics (RTT mean, jitter, loss). Per-family Health is
combined into per-WAN Health under `probe.familyHealthPolicy`
(`all` / `any`). Emits `ProbeResult` events keyed by (WAN, family)
on each Window update via channels. Separates probing (`icmp.go`)
from statistics (`stats.go`); `stats.go` is pure,
table-driven-testable in isolation.

Three implementation points worth documenting because they're easy
to get wrong:

- **Interface binding.** Probes must egress out of the WAN being
  tested, not whichever interface the kernel would normally choose
  for the target. Each probe socket is bound to the WAN's interface
  via `SO_BINDTODEVICE` (Linux; requires CAP_NET_RAW or the
  unprivileged-ping codepath). Without this, a probe from `backup`
  would leave via `primary` and report `primary`'s health, defeating
  the test.
- **ICMP identifier allocation.** Identifiers are 16-bit; one per
  socket is needed to demultiplex echo replies from concurrent
  probes. The daemon assigns identifiers deterministically per
  (WAN, family) at startup (low 16 bits of a hash, with collision
  detection — error out at startup if any collision survives
  resolution, rather than silently mis-routing replies).
- **Sequence numbers.** Per-socket monotonic counter, modulo 2^16.

Algorithm matches `dpinger`'s sample-window approach. Documented in
`docs/specs/probe-algorithm.md`.

### `internal/rtnl`

rtnetlink subscription via `vishvananda/netlink`. Emits LinkEvent on
operstate / carrier changes. Carrier-down events fast-track a WAN to
unhealthy without waiting for the probe Window to fill.

### `internal/selector`

Pure decision logic. Given per-WAN Health + Group config, produces a
Selection per Group. Hysteresis state lives here (per-WAN consecutive
counters). Strategy implementations are functions registered in a
table:

```go
type Strategy func(g Group, members []MemberHealth) (Selection, error)

var strategies = map[string]Strategy{
    "primary-backup": primaryBackup,
}
```

100% unit-test coverage target; the failure modes (all-down,
partial-recovery, flapping-suppressed, sticky-preference) are
exhaustively table-tested.

### `internal/apply`

Mutates kernel state via `vishvananda/netlink` — no shellouts to `ip`.
All operations are family-parameterized: route and rule operations
take `family ∈ {AF_INET, AF_INET6}` and the daemon iterates over
both families per Decision.

- `route.go` — `RTM_NEWROUTE` per family the new active member has a
  gateway for; default route via the gateway/interface in that
  family's `table <T>`. Idempotent. Families the new member lacks a
  gateway for are left untouched (configurable in v0.2: clear vs
  retain). A write that *hard*-fails (ifindex lookup or a netlink
  error) leaves the Decision pending and is retried on the active
  WAN's next probe result until it converges.
- `rule.go` — `RTM_NEWRULE` per family to install `fwmark <m> table
  <t>` at startup. Idempotent — checks existing rules before adding.
  Runs once per (group, family) on daemon start.
- `conntrack.go` — `vishvananda/netlink`'s `ConntrackDeleteFilters`
  to flush entries on the old interface after a switch. Per-family
  flush (both v4 and v6 conntrack tables, iterated). Best-effort;
  failures logged but don't block.

### `internal/state`

Atomic JSON writer (write to `.tmp` + `os.Rename`) for
`/run/wanwatch/state.json`. Hook runner — enumerates
`/etc/wanwatch/hooks/{up,down,switch}.d/*`, executes each with the
env vars from §5.5, captures exit status, applies a 5s timeout.

### `internal/metrics`

`prometheus/client_golang` registry. HTTP server bound to a Unix
socket (mode `0660`, owner `wanwatch:wanwatch` — group-readable by
the user running Telegraf via supplementary group). Exposes
`/metrics`.

### `cmd/wanwatchd`

Wiring. Sets up channels between packages. Implements signal
handling (SIGTERM clean shutdown), the sd_notify watchdog
(`sdnotify.go` — stdlib `net`, a datagram to `$NOTIFY_SOCKET`; no
external dependency, the protocol and env contract are ABI-stable),
and the main event loop:

```go
for {
    select {
    case <-ctx.Done():        return
    case r := <-probeResults: d.handleProbeResult(ctx, r)
    case e := <-linkEvents:   d.handleLinkEvent(ctx, e)
    case e := <-routeEvents:  d.handleRouteEvent(ctx, e)
    }
}
```

A third channel feeds `RouteEvent`s from the per-WAN
`rtnl.RouteSubscriber`s — the daemon mirrors the kernel's main-table
default routes into the per-WAN Gateway cache so Apply can write the
per-Group table without re-reading the kernel on every Decision.

### Cold-start behavior

When the daemon starts, no Samples exist yet. Two questions are
load-bearing:

1. **Initial Selection.** Per Group, the daemon picks the
   highest-priority Member whose interface has `carrier=up`
   per rtnetlink — health is unknown but carrier is at least known.
   If no Member has carrier, no Selection is published yet (the
   group is in `selection: null` state, recorded in state.json
   and reflected in `wanwatch_group_active`).
2. **Initial Apply.** With an initial Selection in hand, the daemon
   immediately writes the corresponding default route(s) and `ip
   rule` entries — *before* the first probe Window has filled. This
   bootstraps routing from boot rather than leaving the kernel in
   whatever state nixpkgs / `systemd-networkd` left it.

Once the first Window completes, hysteresis counters initialize from
the actual Health and the regular Decision loop takes over. The
"first Decision" after cold-start is suppressed if it would just
re-confirm the bootstrap Selection (no hook fires for a no-op).

### Daemon privileges (systemd unit)

The module's systemd unit declares:

- `User = wanwatch`, `Group = wanwatch` (created by the module via
  `users.users.wanwatch` / `users.groups.wanwatch`).
- `AmbientCapabilities = CAP_NET_RAW CAP_NET_ADMIN`
  (`NET_RAW` for ICMP / ICMPv6 raw sockets; `NET_ADMIN` for
  rtnetlink subscription, route mutation, and conntrack flush).
- `CapabilityBoundingSet = CAP_NET_RAW CAP_NET_ADMIN`.
- `NoNewPrivileges = true`, `PrivateTmp = true`, `ProtectSystem = strict`,
  `ProtectHome = true`, `ProtectKernelTunables = true`,
  `ProtectKernelModules = true`, `ProtectControlGroups = true`,
  `RestrictAddressFamilies = AF_UNIX AF_INET AF_INET6 AF_NETLINK`,
  `MemoryDenyWriteExecute = true`, `LockPersonality = true`.
- `RuntimeDirectory = wanwatch` covers `/run/wanwatch` (state file
  + metrics socket); hook scripts under `/etc/wanwatch/hooks` are
  read-only-readable under `ProtectSystem = strict` — the daemon
  never writes there, so no `ReadWritePaths` line is required.
- `Restart = on-failure`, `RestartSec = 5s`,
  `WatchdogSec = 30s` (paired with `sd_notify` keepalive at half-interval).

ICMP probes ship on `SOCK_RAW` via
`golang.org/x/net/icmp.ListenPacket` — `CAP_NET_RAW` is the gate.
An unprivileged variant (`SOCK_DGRAM+IPPROTO_ICMP` keyed off
`net.ipv4.ping_group_range`, with `CAP_NET_RAW` dropped after
socket setup) was originally promised here, but the marginal
security gain — `CAP_NET_ADMIN` is still required for route, rule,
and conntrack mutation, and `SO_BINDTODEVICE` carries its own
kernel-version-dependent capability requirements — leaves it as a
maybe-never. Tracked under `TODO.md` "considered — not currently
planned" with revisit conditions.

---

## 9. Test plan

Three tiers. Every tier runnable via `nix flake check`.

### 9.1 Unit (Nix lib)

Mirrors nftzones structure: `tests/unit/<file>.nix` ↔ `lib/<file>.nix`.

Runner: `lib.runTests` shape (`testFoo = { expr; expected; }`),
wrapped by `tests/unit/runner.nix` into a derivation that fails on
any miss.

Per public function, required test coverage:

1. Happy-path test.
2. Every `throws` branch (negative-case tests via `evalFails`).
3. Every predicate, both `true` and `false` outcomes.
4. Every boundary (empty list, single item, max/min).
5. Round-trip for serialization (`make → toJSON → fromJSON → eq`).
6. Total-order properties for `compare` when present (reflexivity,
   antisymmetry, transitivity). No v1 value type defines `compare`;
   this requirement activates with the first ordered value type.
7. Determinism for allocators (same input → same output).

A meta-test in `tests/unit/skeleton.nix` asserts that every value
type exports the full skeleton from §5.1.

CI gate: `nix build .#checks.x86_64-linux.unit` must succeed.

### 9.2 Unit (Go daemon)

Per-package `_test.go` files. Table-driven tests using `t.Run` and
`t.Parallel()` where appropriate.

- `internal/probe/stats_test.go` — exhaustive sliding-window cases
  (empty, partial, full, after-drop, monotonic RTT, oscillating RTT).
- `internal/selector/*_test.go` — every Strategy under every Health
  permutation + hysteresis state.
- `internal/apply/*_test.go` — table-driven netlink message construction;
  separate netns-based integration test (gated by `-tags=netns`,
  runs in CI under privileged container or sandbox with `unshare`).
- `internal/state/state_test.go` — atomic write under concurrent
  writers; tmpfile cleanup on failure.

Coverage gate (measured by `go test -cover` on the package, line
coverage, excluding `_test.go` files and `cmd/`):

- `internal/selector/` — ≥95% (pure logic; no excuse)
- `internal/probe/stats.go` — ≥95% (pure math)
- `internal/probe/` overall — ≥90%
- `internal/config/` — ≥90% (JSON parsing edge cases)
- `internal/state/` — ≥85%
- `internal/apply/`, `internal/rtnl/`, `internal/metrics/` — ≥70%
  each (netlink-heavy; remainder covered by VM tier)
- `daemon/` aggregate over `internal/...` — ≥85%

CI fails if any individual gate regresses on a PR. `cmd/wanwatchd/`
has table-driven unit coverage (~75% on the daemon pipeline —
decision commit, gateway cache, event loop, prober wiring) backed
by end-to-end behavior in the VM tier; no explicit gate.

### 9.3 Integration (Nix)

`tests/integration/scenarios/<name>.nix` — each file declares a
NixOS configuration, evaluates the module, and asserts:

- The rendered daemon-config JSON matches expectations
  (structural equality after normalization).
- The systemd unit's `ExecStart` line is well-formed.
- `services.wanwatch.marks` and `.tables` are populated correctly.

`tests/integration/rejections/<name>.nix` — declares an
intentionally-invalid config; build succeeds iff module evaluation
*throws*. Confirms that the Nix-side validators are wired into the
live module path, not just unit-tested in isolation.

### 9.4 VM (NixOS test framework)

`tests/vm/*.nix` — `pkgs.testers.nixosTest` multi-machine scenarios.

Initial scenarios (more added as needed):

| File | Topology | Assertion |
|---|---|---|
| `failover-v4.nix` | 1 router (2 v4-only WANs as `dummy0` + `dummy1`) + 1 v4 internet target | Carrier down on primary triggers switch within hysteresis-window time; default route in v4 `table 100` reflects new gateway |
| `failover-v6.nix` | 1 router (2 v6-only WANs) + 1 v6 internet target | Same assertion in the v6 RIB; `ip -6 route show table 100` reflects new gateway |
| `failover-dual-stack.nix` | 1 router (2 dual-stack WANs) + v4 + v6 targets | Carrier down on primary triggers switch; both v4 and v6 default routes in `table 100` update atomically; `WANWATCH_FAMILIES=v4,v6` in hook |
| `family-health-policy.nix` | 1 router (1 dual-stack WAN, v6 target unreachable) | With `familyHealthPolicy = "all"`, WAN flagged unhealthy. With `"any"`, WAN flagged healthy. State JSON reflects per-family Health. |
| `recovery.nix` | dual-stack | After primary recovers, switch back within `consecutiveUp` cycles in both families |
| `nftzones-integration.nix` | 1 router + 1 LAN client (dual-stack) + 2 internet targets | LAN v4 traffic egresses via the active WAN's v4 path; v6 traffic via the v6 path; switching the WAN switches both |
| `metrics.nix` | 1 router + 1 Telegraf scraper | Telegraf successfully scrapes `/metrics` over the Unix socket; metrics with `family` label appear in output |
| `hooks.nix` | 1 router (dual-stack) + hook scripts under `/etc/wanwatch/hooks/` | On switch, hook scripts receive the documented env vars including `WANWATCH_GATEWAY_V4_*`, `WANWATCH_GATEWAY_V6_*`, `WANWATCH_FAMILIES` |

CI gate: VM tier runs on Linux+KVM only (gated via
`pkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux`).

---

## 10. Build order (bottom-up)

Each pass is a milestone. The pass is "done" only when **all** of its
artifacts have tests, format/lint clean, and `nix flake check` passes
at HEAD.

### Pass 1 — foundations

- `flake.nix` skeleton (inputs: nixpkgs, libnet, treefmt-nix;
  outputs: lib, formatter, empty checks)
- `lib/internal/primitives.nix` — generic helpers (`tryOk`,
  `tryErr`, `check`, `partitionTry`, `formatErrors`,
  `isValidName`, `isPositiveInt`)
- `lib/internal/default.nix` — three-tier composition
- `lib/types/{default,primitives,probe,wan}.nix` — stubs for NixOS
  option types (real types in Pass 5)
- `tests/unit/runner.nix` — `lib.runTests` derivation wrapper
- `daemon/go.mod` skeleton + `daemon/internal/probe/stats.go`
  (pure sliding-window math) + tests
- `CLAUDE.md` — conventions documented
- `docs/glossary.md` — initial term set

**Exit criteria**: `nix flake check` runs and passes (with zero
non-trivial tests, but the infrastructure works).

### Pass 2 — leaf value types

- `lib/internal/wan.nix` + tests — `interface` + `pointToPoint`
  fields; `families` accessor derives the served families from the
  embedded probe's targets. No static gateway declaration — the
  daemon learns next-hops at runtime (see Pass 5 GatewayCache).
- `lib/internal/probe.nix` + nested `thresholds`, `hysteresis`,
  `familyHealthPolicy` + tests — `targets` validated as list of
  IPs; family of each target detected via libnet's `ip.isIpv4` /
  `ip.isIpv6`.
- `daemon/internal/probe/icmp.go` + tests (`golang.org/x/net/icmp`)
  — handles both ICMP (v4) and ICMPv6 from the start, dispatched by
  target family.
- `tests/unit/skeleton.nix` — meta-test for §5.1 contract

**Exit criteria**: a dual-stack WAN can be `make`'d, serialized to
JSON, and the JSON parses; ICMP and ICMPv6 probes can both be sent
and timed in a netns test.

### Pass 3 — composites and allocators

- `lib/group.nix` (incl. `member` type) + tests
- `lib/marks.nix` (deterministic allocator) + tests
- `lib/tables.nix` (deterministic allocator) + tests
- `daemon/internal/selector/` — all strategies + hysteresis + tests

**Exit criteria**: given a config, the lib produces deterministic
marks/tables; the daemon's selector reproduces the same active
member the lib would predict (cross-checked by tests).

### Pass 4 — orchestration

- `lib/selector.nix` (pure selection — used by lib-level assertions
  AND daemon parity tests)
- `lib/config.nix` (render to daemon JSON)
- `daemon/internal/config/` — JSON parsing of the daemon-config
  contract (§5.5). Needed in Pass 4 because the daemon's exit
  criterion requires reading a hand-written config.
- `daemon/internal/apply/` — route, rule, conntrack via netlink.
  **Both families from day one** — the netlink calls are
  family-parameterized (`AF_INET` / `AF_INET6`). Pass 4 ships the
  v4+v6 apply path together; there is no v6-as-followup phase.
- `daemon/internal/state/` — atomic JSON, hook runner (per-family
  env vars per §5.5)
- `daemon/internal/rtnl/` — subscription (carrier/operstate are
  family-agnostic)

**Exit criteria**: the daemon can be started against a hand-written
dual-stack config, probes v4 and v6 targets, decides per the
configured family-health policy, writes state.json with per-family
detail, executes a hook script that sees `WANWATCH_FAMILIES` set
correctly. End-to-end on a single host without the Nix module.

### Pass 5 — surfaces

- `lib/types/*.nix` — flattened option types (per-concept files)
- `modules/wanwatch.nix` — the NixOS module (creates user/group,
  systemd unit with capabilities per §8, state dir, hooks dir)
- `daemon/internal/metrics/` — Prometheus + Unix socket listener
- `daemon/cmd/wanwatchd/main.go` — full wiring, signals, sd_notify
- `modules/telegraf.nix` — optional integration

**Exit criteria**: `nix flake check` passes including VM tier;
nftzones integration scenario passes; Telegraf scrape works.

### Pass 6 — documentation polish

- `README.md` — quickstart
- `docs/wan-monitoring.md` — newcomer intro
- `docs/architecture.md` — daemon + Nix layering
- `docs/selector.md` — algorithm spec
- `docs/nftzones-integration.md` — full integration guide
- `docs/metrics.md` — Prometheus catalog
- `docs/specs/failover.md` — failover semantics
- `docs/specs/daemon-config.md` — config JSON schema
- `docs/specs/daemon-state.md` — state JSON schema
- `docs/specs/probe-algorithm.md` — probe stats algorithm
- `docs/specs/failover.md` — single-active failover semantics
- `docs/specs/prior-art.md` — distillation of the planning-phase survey
- `CHANGELOG.md` — `0.1.0` initial entry

**Exit criteria**: `0.1.0` tag.

---

## 11. Conventions

### 11.1 Terminology

Strictly per §3 glossary. New code uses these terms; new terms get
added to the glossary in the same commit that introduces them.

### 11.2 API skeleton

Every value type implements §5.1's skeleton. Asserted by a meta-test.

### 11.3 Tests for everything

§9 discipline. Coverage gates in CI fail PRs that regress.

### 11.4 Bottom-up build

§10 ordering. Lower-layer refactors are encouraged when higher
layers reveal inadequacy; refactor in a dedicated commit (with
updated tests), not as a slipped-in change.

### 11.5 Self-contained commits

Each commit:

- One logical change.
- Tests live with code (adding `lib/internal/wan.nix` and
  `tests/unit/internal/wan.nix` is one commit).
- `nix flake check` + `go test ./...` pass at HEAD after the commit.

Subject format (imperative, ≤72 chars):

```
<scope>: <imperative summary>

scope ∈ { lib, internal, types, modules, daemon, tests,
          docs, ci, deps, flake }
```

Body explains *why* when not obvious from the diff. Reference issue /
discussion only when load-bearing context.

No `--no-verify`, no `--no-gpg-sign` unless explicitly requested for
a specific commit and explained in the body.

### 11.6 Modern Nix — flake first

- `flake.nix` is the canonical entry point. No `default.nix` at root.
- Outputs: `lib` (system-agnostic — see below), `nixosModules.{default,telegraf}`,
  `packages.<system>.{wanwatchd,default}`, `checks.<system>.*`,
  `formatter.<system>`, `devShells.<system>.default`.
- The `lib` output is intentionally **not** wrapped in `forAllSystems`.
  It operates on Nix values (validators, allocators, JSON renderers)
  and inherits `nixpkgs.lib` at import time — a per-system wrapping
  would yield the same attrset for every system. Consumers wanting
  the lib bound against a different nixpkgs's `lib` can call
  `import (wanwatch + "/lib") { lib = …; libnet = …; }` directly.
- Inputs minimal and pinned, each with
  `.inputs.nixpkgs.follows = "nixpkgs"`.
- No `flake-utils` / `flake-parts`. Hand-written `forAllSystems`
  matching nftzones' style.
- `flake.lock` committed; updated deliberately in audit cadence.
- `nix flake check` is the contract.
- Per-system support for `x86_64-linux`, `aarch64-linux`,
  `x86_64-darwin`, `aarch64-darwin`. Daemon Linux-only — VM tier
  and `packages.wanwatchd` gated via
  `pkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux`.
- `nixpkgs.lib` is a standard dependency, not an opt-in extension.
  `lib/` takes `{ lib, libnet }` at import time and uses `lib.*`
  freely. There is no "core that evaluates with `lib = null`" —
  that pattern fits primitives libraries like libnet, not
  downstream consumers like wanwatch whose NixOS module requires
  `lib.evalModules` and `lib.types.*` regardless.
- Current `lib.types.*` only; in particular avoid
  `lib.types.either` in favor of `lib.types.oneOf`, and
  `lib.types.uniq` is forbidden (removed in modern nixpkgs).
- Explicit `inherit (x) y z` (no `with x;` at file top).
- `lib.pipe` for transformations.
- `mkOption` declarations must have `description`, `type`, and
  either `default` or `example` (preferably both); `description`
  uses `lib.mdDoc` where applicable.
- `mkEnableOption` for booleans gating optional behavior.

### 11.7 Modern Go

- Go ≥1.24 (current stable at the time of writing).
- Standard project layout: `cmd/<binary>/` + `internal/<pkg>/`.
- `context.Context` propagation everywhere; cancellation honored.
- `log/slog` from stdlib for structured logging.
- Error wrapping with `%w`; `errors.Is` / `errors.As` for inspection;
  no `fmt.Errorf("%v", err)`.
- `any` over `interface{}`; prefer concrete types over interfaces
  when there's a single implementation.
- Table-driven tests; `t.Helper()` in test helpers; `testing.TB` in
  shared test utilities.
- No `init()` for side effects.
- Functional options for multi-optional-param constructors.
- No `panic()` outside `main`; all errors returned.
- `-trimpath` via `buildGoModule` defaults; reproducible builds.

### 11.8 Unified formatter

- `treefmt-nix` wired into `flake.formatter.<system>`.
- Programs: `nixfmt` (Nix, RFC 166), `gofumpt` + `goimports` (Go).
- CI gate: `nix fmt -- --fail-on-change`.
- Editor integration: contributors run `nixfmt` / `gofumpt` on save.

### 11.9 Lint

- `golangci-lint` with curated `.golangci.yml`. Enabled checks:
  `errcheck`, `gosec`, `govet`, `revive`, `staticcheck`, `unused`,
  `gocritic`, `gofumpt`, `unparam`, `errorlint`, `bodyclose`,
  `goconst`, `prealloc`.
- CI step distinct from format. Runs on every PR / push.

### 11.10 Audits — cadence

| Audit | Cadence | Gate |
|---|---|---|
| Formatter drift | every commit | `nix fmt -- --fail-on-change` in CI |
| Lint clean | every commit | `golangci-lint run` + `nixfmt --check` |
| Test coverage | every commit | `go test -cover` per package + new-file-needs-test check |
| Vulnerability scan | weekly + on release | GitHub Actions cron workflow (`.github/workflows/audit.yml`): runs `govulncheck ./...` and `nix run nixpkgs#vulnix -- -S` against the daemon closure; opens an issue on findings |
| Dependency-update review | monthly | manual: review `go.mod` + `flake.lock` deltas |
| Public-API surface review | each minor version | manual: read `lib/default.nix` + daemon exports |
| Glossary drift | each minor version | grep usage vs `docs/glossary.md` |
| Convention drift | each minor version | re-read `CLAUDE.md` against code |

---

## 12. Open questions

Tracked here rather than in code comments. Each gets resolved before
the relevant Pass starts; resolution moves to the relevant doc.

1. **State file backward compatibility**. Schema `version: 1` is set,
   but the daemon hasn't shipped. Do we commit to schema-evolution
   discipline from day 1 (write a `docs/specs/state-evolution.md`)
   or defer? Recommendation: defer until v0.2 — note in changelog.

2. **fwmark / table allocation collisions**. The deterministic
   allocator hashes group names to mark/table ints. Range proposed:
   marks `0x64`–`0x7FFF` (avoiding `0x100` boundaries that some
   tools special-case), tables `100`–`32766` (avoiding `main=254`,
   `local=255`, `default=253`, `unspec=0`). Collisions on hash —
   how do we resolve? Recommendation: linear probe within the
   range; document determinism contract as "function of group-name
   set, not function of any single group's name". Test:
   adding/removing a group changes only the affected entry plus any
   probe-displaced ones, and the displacement is deterministic.

3. **IPv6 family-health policy default**. With dual-stack WANs and a
   single v6 target outage, the user wants to know whether the WAN is
   "healthy". `familyHealthPolicy = "all"` says no (any family
   unhealthy → WAN unhealthy); `"any"` says yes. Recommendation:
   default `"all"` — conservative, matches user expectation that a
   "broken" WAN should be avoided. Document `"any"` as the right
   choice for ISPs with unreliable v6 deployments where v4 is the
   primary path.

4. **Per-family Selection (deferred to v2)**. v1 produces one
   Selection per group, applied to every family the active member
   has a gateway for. v2 may produce a Selection per (group, family)
   so v4 can route via primary while v6 routes via backup
   simultaneously. The Decision state-space grows linearly with the
   number of families per group (2× for v4+v6). Tracking as v2 work
   to keep v1 scope manageable.

5. **Hook timeout**. v1 sets a 5s hook timeout. Configurable?
   Recommendation: not in v1; configurable in v0.2 if users complain.

6. **State file consumer locking**. Atomic-rename writes give
   readers a consistent view, but readers polling at >1Hz may miss
   short-lived states. Do we need an event stream (Unix socket
   subscription) for low-latency consumers? Recommendation:
   defer to v0.2 if requested; v1 is poll-only.

7. **Daemon hot-reload**. SIGHUP re-reads config? Or require restart?
   Recommendation: restart-only in v1. Hot-reload adds significant
   complexity (re-allocating marks/tables would require kernel-state
   reconciliation).

8. **wanwatchctl CLI**. A small CLI for status queries
   (`wanwatchctl status`, `wanwatchctl group <name>`, etc.). Useful
   but not v1. Defer to v0.2.

9. **MTU/link-speed awareness**. Some setups want to prefer a
   higher-bandwidth WAN even at slightly higher latency. Out of
   scope for v1's pure-health-based selection. v2+ topic.

---

## 13. References

- [`nix-libnet`](../nix-libnet) — IP/CIDR/interface library, used
  for interface-name validation and address-type modeling.
- [`nix-nftzones`](../nix-nftzones) — zone-based nftables firewall;
  v1 integration target.
- [`mwan3`](https://openwrt.org/docs/guide-user/network/wan/multiwan/mwan3) —
  OpenWrt prior art.
- [`dpinger`](https://github.com/dennypage/dpinger) — pfSense's
  probe daemon; algorithm reference.
- VyOS WAN load balancing, MikroTik netwatch, NetworkManager
  connection priorities — surveyed during planning;
  see `docs/specs/prior-art.md` (Pass 6).
