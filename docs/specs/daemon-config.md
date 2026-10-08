# daemon-config (frozen spec)

The NixOS module writes this JSON to `/etc/wanwatch/config.json`, and `wanwatchd` reads it at startup. `wanwatch.config.toJSON` in `lib/internal/config.nix` produces it; `daemon/internal/config/config.go` parses it and re-validates its structure.

**Schema version**: `1`. Any backwards-incompatible field change bumps it. The daemon refuses to start when `schema` does not match its `SupportedSchema`.

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
| `global` | object | yes | Process-wide settings. The Nix renderer merges user settings over `defaultGlobal`, so the rendered file always has every key; the daemon applies no defaults and rejects missing or empty values. |
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
| `statePath` | string | `/run/wanwatch/state.json` | Path of the atomically written State snapshot. |
| `hooksDir` | string | `/etc/wanwatch/hooks` | Root of the Hook tree (`<dir>/{up,down,switch}.d/`). |
| `metricsSocket` | string | `/run/wanwatch/metrics.sock` | Unix-socket path of the Prometheus endpoint. |
| `logLevel` | string | `info` | One of `debug`, `info`, `warn`, `error`. |
| `hookTimeoutMs` | int | `5000` | Per-Hook execution deadline in milliseconds. Must be `> 0`. |

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
| `pointToPoint` | bool | no (default `false`) | `true` installs `scope link` default routes (PPP, WireGuard, GRE, tun). `false` discovers the Gateway at runtime from the kernel's main routing table via netlink. |
| `probe` | object | yes | Probe configuration; shape below. |

`probe.targets` determines the Families a WAN serves: a non-empty `targets.v4` means v4, and a non-empty `targets.v6` means v6. No separate Gateway or Family declaration exists. The daemon learns each next-hop at runtime and publishes it in [`state.json`](./daemon-state.md) under `wans.<name>.gateways.{v4,v6}`; see [ADR 0004](../adr/0004-runtime-gateway-discovery.md).

## `wans.<name>.probe`

```json
{
  "method": "icmp",
  "targets": { "v4": [ "1.1.1.1" ], "v6": [ "2606:4700:4700::1111" ] },
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
| `targets` | object | required | Per-Family Target lists: `{ "v4": [...], "v6": [...] }`. At least one list must be non-empty, and each item must be an IP literal of its list's Family. |
| `intervalMs` | int | `500` | Time between cycles. |
| `timeoutMs` | int | `1000` | Per-cycle read deadline. |
| `windowSize` | int | `10` | Window capacity. |
| `thresholds.lossPctUp` | int | `10` | Loss% at or below which a flip to up is allowed. |
| `thresholds.lossPctDown` | int | `30` | Loss% at or above which a flip to down fires. |
| `thresholds.rttMsUp` | int | `250` | RTT (ms) at or below which a flip to up is allowed. |
| `thresholds.rttMsDown` | int | `500` | RTT (ms) at or above which a flip to down fires. |
| `hysteresis.consecutiveUp` | int | `5` | Healthy cycles needed to flip up. |
| `hysteresis.consecutiveDown` | int | `3` | Unhealthy cycles needed to flip down. |
| `familyHealthPolicy` | string | `"all"` | `"all"` or `"any"`. See [`docs/wan-monitoring.md`](../wan-monitoring.md) and [ADR 0005](../adr/0005-family-health-policy-defaults-to-all.md). |

The Nix-side validator enforces `lossPctUp < lossPctDown` and `rttMsUp < rttMsDown`, so the threshold band is never empty.

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
| `table` | int | yes | Routing-table ID. User-declared integer in `[1000, 32767]` (type `wanwatch.types.routingTableId`). |
| `mark` | int | yes | fwmark. User-declared integer in `[1000, 32767]` (type `wanwatch.types.fwmark`). |

See [ADR 0003](../adr/0003-user-declared-marks-and-tables.md) for why marks and tables are not allocated automatically.

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
| `wan` | string | yes | Key in the top-level `wans` map. |
| `weight` | int | yes | `1..1000`. Reserved for v2 multi-active Strategies; ignored under `primary-backup`. |
| `priority` | int | yes | `1..1000`. Lower is preferred under `primary-backup`. |

## Validation layers

| Layer | Catches |
|---|---|
| Option types (`lib/types/`) | Wrong field types, enum mismatches, malformed IP literals (via libnet). |
| `wanwatch.<type>.tryMake` | Cross-field invariants: Family coupling, duplicate Members, threshold ordering. |
| `config.assertUniqueMarksAndTables` | Marks or tables shared across Groups. |
| `daemon/internal/config/Validate` | Unknown keys and trailing data at parse time, then the first failing check of: empty paths or non-positive `hookTimeoutMs` in `global`; name/key disagreement; empty `interface`; `method` other than `icmp`; no Targets; non-positive `intervalMs`, `timeoutMs`, or `windowSize`; `familyHealthPolicy` other than `all` or `any`; loss thresholds outside `0..100` or not ordered up < down; RTT thresholds not positive or not ordered up < down; Hysteresis counters below 1; unknown `strategy`; non-positive `table` or `mark`; no Members; dangling `member.wan` references. |

## Compatibility policy

The schema version is bumped only when an existing field changes meaning or a required field is added without a default. An optional field with a backwards-compatible default needs no bump.

A breaking change requires:

1. Increment `schemaVersion` in `lib/internal/config.nix` and `SupportedSchema` in `daemon/internal/config/config.go`.
2. Update this spec.
3. Add a `CHANGELOG.md` entry with the migration note under the next release.
4. Ship in a major version bump (`0.2.0` → `0.3.0`, and so on).
