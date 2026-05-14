# daemon-config (frozen spec)

The JSON written by the NixOS module to `/etc/wanwatch/config.json` and read by `wanwatchd` at startup. Produced by `wanwatch.config.toJSON` in `lib/internal/config.nix`; parsed and structurally re-validated by `daemon/internal/config/config.go`.

**Schema version**: `1`. Bumped on any backwards-incompatible field change. The daemon refuses to start if its `SupportedSchema` does not match.

## Top-level shape

```json
{
  "schema": 1,
  "global": { ... },
  "wans":   { "<name>": { ... }, ... },
  "groups": { "<name>": { ... }, ... }
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `schema` | int | yes | Always `1` in this spec. |
| `global` | object | yes | Process-wide settings. Missing keys take `defaultGlobal` values. |
| `wans` | object | yes | Map from WAN name to WAN object. May be empty. |
| `groups` | object | yes | Map from Group name to Group object. May be empty. |

## `global`

```json
{
  "statePath":     "/run/wanwatch/state.json",
  "hooksDir":      "/etc/wanwatch/hooks",
  "metricsSocket": "/run/wanwatch/metrics.sock",
  "logLevel":      "info",
  "hookTimeoutMs": 5000
}
```

| Field | Type | Default | Meaning |
|---|---|---|---|
| `statePath` | string | `/run/wanwatch/state.json` | Where the daemon writes the state snapshot atomically. |
| `hooksDir` | string | `/etc/wanwatch/hooks` | Root of the hook-script tree (`<dir>/{up,down,switch}.d/`). |
| `metricsSocket` | string | `/run/wanwatch/metrics.sock` | Unix-socket path for the Prometheus endpoint. |
| `logLevel` | string | `info` | One of `debug`, `info`, `warn`, `error`. |
| `hookTimeoutMs` | int | `5000` | Per-hook execution deadline in milliseconds. Must be `> 0`. |

## `wans.<name>`

```json
{
  "name": "primary",
  "interface": "eth0",
  "pointToPoint": false,
  "probe": { ... }
}
```

| Field | Type | Required | Meaning |
|---|---|---|---|
| `name` | string | yes | Must match the attribute key. |
| `interface` | string | yes | Linux interface name (passes `dev_valid_name`). |
| `pointToPoint` | bool | no (default `false`) | When true the daemon installs `scope link` default routes (PPP / WireGuard / GRE / tun); when false the daemon discovers the gateway via netlink from the kernel's main routing table at runtime. |
| `probe` | object | yes | Probe configuration; shape below. |

The families a WAN serves are derived from `probe.targets`: a v4 IP literal means the WAN serves v4, a v6 literal means it serves v6. There is no separate gateway / family declaration — the daemon learns the next-hop dynamically and surfaces it in [`state.json`](./daemon-state.md) under `wans.<name>.gateways.{v4,v6}`.

## `wans.<name>.probe`

```json
{
  "method": "icmp",
  "targets": [ "1.1.1.1", "2606:4700:4700::1111" ],
  "intervalMs": 1000,
  "timeoutMs": 1000,
  "windowSize": 10,
  "thresholds": {
    "lossPctUp": 10,
    "lossPctDown": 50,
    "rttMsUp": 200,
    "rttMsDown": 1000
  },
  "hysteresis": {
    "consecutiveUp": 3,
    "consecutiveDown": 3
  },
  "familyHealthPolicy": "all"
}
```

| Field | Type | Default | Meaning |
|---|---|---|---|
| `method` | string | `"icmp"` | Probe method. v1: `icmp` only. |
| `targets` | array<string> | required | IP literals — v4 or v6. Non-empty. |
| `intervalMs` | int | `1000` | Time between cycles. |
| `timeoutMs` | int | `1000` | Per-cycle read deadline. |
| `windowSize` | int | `10` | Sliding-window capacity. |
| `thresholds.lossPctUp` | int | `10` | Loss% at or below which a flip-to-up is allowed. |
| `thresholds.lossPctDown` | int | `50` | Loss% at or above which a flip-to-down fires. |
| `thresholds.rttMsUp` | int | `200` | RTT (ms) at or below which a flip-to-up is allowed. |
| `thresholds.rttMsDown` | int | `1000` | RTT (ms) at or above which a flip-to-down fires. |
| `hysteresis.consecutiveUp` | int | `3` | Cycles of healthy observation needed to flip up. |
| `hysteresis.consecutiveDown` | int | `3` | Cycles of unhealthy observation needed to flip down. |
| `familyHealthPolicy` | string | `"all"` | `"all"` or `"any"`. See [`docs/wan-monitoring.md`](../wan-monitoring.md). |

The Nix-side validator enforces `lossPctUp < lossPctDown` and `rttMsUp < rttMsDown` so the threshold band is always non-empty.

## `groups.<name>`

```json
{
  "name": "home-uplink",
  "members": [ { ... }, { ... } ],
  "strategy": "primary-backup",
  "table": 100,
  "mark": 100
}
```

| Field | Type | Required | Meaning |
|---|---|---|---|
| `name` | string | yes | Must match the attribute key. |
| `members` | array<object> | yes | Non-empty; no duplicate `wan` references. |
| `strategy` | string | yes | v1: `"primary-backup"`. |
| `table` | int | yes | Routing-table id. Auto-allocated (`null` in user input ⇒ resolved by `tables.allocate`). |
| `mark` | int | yes | fwmark. Auto-allocated by `marks.allocate`. |

## `groups.<name>.members[]`

```json
{
  "wan": "primary",
  "weight": 100,
  "priority": 1
}
```

| Field | Type | Required | Meaning |
|---|---|---|---|
| `wan` | string | yes | References a key in the top-level `wans` map. |
| `weight` | int | yes | `1..1000`. Reserved for v2's multi-active strategies; ignored under `primary-backup`. |
| `priority` | int | yes | `1..1000`. Lower is preferred under `primary-backup`. |

## Validation layers

| Layer | What it catches |
|---|---|
| Option types (`lib/types/`) | Wrong field types, enum mismatch, malformed IP literals (via libnet). |
| `wanwatch.<type>.tryMake` | Cross-field invariants (family coupling, duplicate members, threshold ordering). |
| `config.resolveAllocations` | Mark / table collisions between explicit and auto-allocated values. |
| `daemon/internal/config/Validate` | Structural sanity after deserialization: name/key agreement, dangling `member.wan` references, empty paths in `global`. |

## Compatibility policy

Schema version is bumped only when an existing field changes meaning or a required field is added without a default. Adding an optional field with a backwards-compatible default does not require a bump.

A breaking change requires:

1. Increment `schemaVersion` in `lib/internal/config.nix` and `SupportedSchema` in `daemon/internal/config/config.go`.
2. Update this spec.
3. Add a `CHANGELOG.md` entry under the next release with the migration note.
4. Ship in a major version bump (`0.2.0` → `0.3.0`, etc.).
