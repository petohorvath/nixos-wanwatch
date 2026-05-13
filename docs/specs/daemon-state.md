# daemon-state (frozen spec)

The JSON snapshot the daemon publishes to `services.wanwatch.global.statePath` (default `/run/wanwatch/state.json`). Written atomically (tmpfile + `rename`) so readers always see a consistent file.

**Schema version**: `2`. Bumped on any backwards-incompatible field change.

Schema-2 additions: per-WAN `gateways.{v4,v6}` reflecting the daemon-discovered next-hop. See `SchemaVersion` history in `daemon/internal/state/state.go`.

Produced by `daemon/internal/state/state.go`; the on-disk shape exactly matches `State` and `State.Wans/Groups` value types.

## Top-level shape

```json
{
  "schema": 2,
  "updatedAt": "2026-05-12T14:30:01.234567890Z",
  "wans":   { "<name>": { ... }, ... },
  "groups": { "<name>": { ... }, ... }
}
```

| Field | Type | Meaning |
|---|---|---|
| `schema` | int | Matches the daemon's `SchemaVersion`. |
| `updatedAt` | string (RFC 3339 nanos UTC) | Write time. The daemon overwrites any caller-supplied value. |
| `wans` | object | Map from WAN name to per-WAN state. |
| `groups` | object | Map from Group name to per-Group state. |

## `wans.<name>`

```json
{
  "interface": "eth0",
  "carrier": "up",
  "operstate": "up",
  "healthy": true,
  "gateways": { "v4": "192.0.2.1", "v6": "2001:db8::1" },
  "families": {
    "v4": { ... },
    "v6": { ... }
  }
}
```

| Field | Type | Meaning |
|---|---|---|
| `interface` | string | Linux interface name. |
| `carrier` | string | `"up"` / `"down"` / `"unknown"`. |
| `operstate` | string | IFLA_OPERSTATE textual: `up`, `down`, `dormant`, `lowerlayerdown`, `notpresent`, `testing`, `unknown`. |
| `healthy` | bool | Aggregate per `probe.familyHealthPolicy`. |
| `gateways.v4` | string | Daemon-discovered v4 next-hop, or `""` if the kernel has no v4 default on this interface (or the route is scope-link, i.e. `pointToPoint`). Schema-2. |
| `gateways.v6` | string | Same for v6. Schema-2. |
| `families` | object | Per-family slice; one entry per family present in `probe.targets`. |

## `wans.<name>.families.<v4|v6>`

```json
{
  "healthy": true,
  "rttMs": 12.4,
  "jitterMs": 1.2,
  "lossPct": 0.0,
  "targets": [ "1.1.1.1" ]
}
```

| Field | Type | Meaning |
|---|---|---|
| `healthy` | bool | Post-threshold, post-hysteresis verdict. False until the first ProbeResult cooks (PLAN §8 cold-start). |
| `rttMs` | float | Mean RTT across the family's targets in milliseconds. |
| `jitterMs` | float | Mean jitter (stddev) across the family's targets. |
| `lossPct` | float | Mean loss ratio × 100. |
| `targets` | array<string> | Probe targets for this family (echo of config). |

## `groups.<name>`

```json
{
  "active": "primary",
  "activeSince": "2026-05-12T14:30:01.234567890Z",
  "decisionsTotal": 3,
  "strategy": "primary-backup"
}
```

| Field | Type | Meaning |
|---|---|---|
| `active` | string \| null | Current Selection. `null` when no Member is healthy. |
| `activeSince` | string (RFC 3339 nanos UTC) \| null | When `active` was set to its current value. Null if never active. |
| `decisionsTotal` | int | Decisions emitted for this Group since daemon start. |
| `strategy` | string | Echo of `groups.<name>.strategy`. |

## Hook env-var contract (PLAN §5.5)

Hooks under `<hooksDir>/{up,down,switch}.d/*` receive the following env vars on every Decision. The constants are exported as `state.Env*` in `daemon/internal/state/hooks.go`.

| Variable | Set when |
|---|---|
| `WANWATCH_EVENT` | Always. One of `up`, `down`, `switch`. |
| `WANWATCH_GROUP` | Always. Group name. |
| `WANWATCH_WAN_OLD` | Always. Previous active WAN; empty if none. |
| `WANWATCH_WAN_NEW` | Always. New active WAN; empty if none. |
| `WANWATCH_IFACE_OLD` / `_NEW` | Always. Linux interface names; empty when the corresponding WAN is unset. |
| `WANWATCH_GATEWAY_V4_OLD` / `_NEW` | Always. Discovered v4 next-hop for the WAN's interface; empty when the kernel has no v4 default on it (or the WAN is `pointToPoint`). |
| `WANWATCH_GATEWAY_V6_OLD` / `_NEW` | Always. Same for v6. |
| `WANWATCH_FAMILIES` | Always. Comma-joined set of probed families for the new WAN. `""` when new is null. |
| `WANWATCH_TABLE` | Always. Routing-table id (int as string). |
| `WANWATCH_MARK` | Always. fwmark (int as string). |
| `WANWATCH_TS` | Always. Emit time, RFC 3339 nanos UTC. |

### Event matrix

| `WAN_OLD` | `WAN_NEW` | `EVENT` |
|---|---|---|
| `""` | `"primary"` | `up` |
| `"primary"` | `""` | `down` |
| `"primary"` | `"backup"` | `switch` |
| identical | identical | *(no event fired)* |

### Hook execution

- Files are executed in lexicographic order (`a-first.sh`, `b-second.sh`, …) — matches `run-parts` convention.
- Each invocation gets a fresh process with a 5-second timeout (`state.DefaultHookTimeout`).
- Non-zero exits and timeouts are logged + counted via `wanwatch_hook_invocations_total{event,result}` but do not abort the apply transaction. Hooks are notifications, not gates.

### Example hook

```sh
#!/bin/sh
# /etc/wanwatch/hooks/switch.d/notify.sh
logger -t wanwatch \
    "$WANWATCH_GROUP: $WANWATCH_WAN_OLD → $WANWATCH_WAN_NEW (families=$WANWATCH_FAMILIES)"
```

## Compatibility policy

Same rules as [`daemon-config.md`](./daemon-config.md): bump `state.SchemaVersion` only when an existing field changes meaning or a required field is added without a default.
