# daemon-state (frozen spec)

The daemon publishes this JSON snapshot to `services.wanwatch.global.statePath` (default `/run/wanwatch/state.json`). Writes are atomic (tmpfile + `rename`), so readers always see a complete file. `daemon/internal/state/state.go` produces it, and the on-disk shape matches the `State` value types exactly.

**Schema version**: `1`. Pre-release, the version is pinned: with no external consumers, in-tree refactors do not bump it. The first tagged release freezes shape 1; after that, any backwards-incompatible field change bumps the number.

## When `state.json` is rewritten

The daemon republishes on every observable state transition, not only on Selection changes:

- **Bootstrap**: once at startup, before any event is processed, so early consumers see the configured WANs and Groups before the first Sample.
- **Decision commit**: when a Group's `active` Member changes and every route write has succeeded, except for Families skipped because no Gateway is known yet. Hooks fire immediately after.
- **Per-Family Health transition**: when a (WAN, Family) flips its `healthy` verdict. Without this trigger, a flip that leaves the WAN aggregate unchanged (for example, v4 drops while v6 holds under `familyHealthPolicy = "any"`) would never reach `state.json`.
- **Carrier or operstate change**: any rtnetlink LinkEvent that changes `wans.<name>.carrier` or `wans.<name>.operstate`.
- **Gateway change**: a current default-route observation on a watched interface that changes `wans.<name>.gateways.{v4,v6}`. Route notifications trigger fresh kernel reads, so a superseded notification never overwrites a newer Gateway or clears a replacement.

Window statistics (`rttSeconds`, `jitterSeconds`, `lossRatio`) are not republished every cycle; several writes per second would dominate the daemon's I/O. `state.json` snapshots them at each transition, which suffices for a consistent current view. Live trend data belongs on the Prometheus endpoint at `/run/wanwatch/metrics.sock`.

## Top-level shape

```json
{
  "schema": 1,
  "updatedAt": "2026-05-12T14:30:01.234567890Z",
  "wans":   { "<name>": { ... }, ... },
  "groups": { "<name>": { ... }, ... }
}
```

| Field | Type | Meaning |
|---|---|---|
| `schema` | int | The daemon's `SchemaVersion`. |
| `updatedAt` | string (RFC 3339 nanos UTC) | Write time. The daemon fills it with the current time unless the caller supplies one; a Decision supplies the same timestamp it passes to Hooks as `WANWATCH_TS`. |
| `wans` | object | Map from WAN name to per-WAN State. |
| `groups` | object | Map from Group name to per-Group State. |

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
| `carrier` | string | `"up"`, `"down"`, or `"unknown"`. |
| `operstate` | string | IFLA_OPERSTATE text: `up`, `down`, `dormant`, `lowerlayerdown`, `notpresent`, `testing`, `unknown`. |
| `healthy` | bool | WAN Health, aggregated under `probe.familyHealthPolicy`. |
| `gateways.v4` | string | Discovered v4 Gateway, or `""` when the kernel has no v4 default on this interface or the route is scope-link (`pointToPoint`). |
| `gateways.v6` | string | Same for v6. |
| `families` | object | One entry per Family present in `probe.targets`. |

## `wans.<name>.families.<v4|v6>`

```json
{
  "healthy": true,
  "rttSeconds": 0.0124,
  "jitterSeconds": 0.0012,
  "lossRatio": 0.0,
  "targets": [ "1.1.1.1" ]
}
```

| Field | Type | Meaning |
|---|---|---|
| `healthy` | bool | Verdict after thresholds and Hysteresis. `false` until the first ProbeResult cooks the Family; see [Cold-start invariant](./failover.md#cold-start-invariant). |
| `rttSeconds` | float | Mean RTT across the Family's Targets, in seconds. |
| `jitterSeconds` | float | Mean jitter (stddev) across the Family's Targets, in seconds. |
| `lossRatio` | float | Mean loss in `[0, 1]`. |
| `targets` | array<string> | The Family's Targets, echoed from config. |

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
| `activeSince` | string (RFC 3339 nanos UTC) \| null | When `active` took its current value. `null` if never active. |
| `decisionsTotal` | int | Decisions emitted for this Group since daemon start. |
| `strategy` | string | Echo of `groups.<name>.strategy`. |

## Hook env-var contract

Every Decision runs the Hooks under `<hooksDir>/{up,down,switch}.d/*` with these env vars. `daemon/internal/state/hooks.go` exports the names as `state.Env*` constants. Like `state.json`, Hooks run only after Apply succeeds; a hard route-write failure holds them back. A Family whose Gateway is not yet known is skipped rather than failed, so a switch Hook does not prove that every Family already routes through the new WAN; that Family's route follows when its Gateway appears.

| Variable | Value (always set) |
|---|---|
| `WANWATCH_EVENT` | `up`, `down`, or `switch`. |
| `WANWATCH_GROUP` | Group name. |
| `WANWATCH_WAN_OLD` | Previously active WAN; empty if none. |
| `WANWATCH_WAN_NEW` | Newly active WAN; empty if none. |
| `WANWATCH_IFACE_OLD` / `_NEW` | Linux interface names; empty when the corresponding WAN is unset. |
| `WANWATCH_GATEWAY_V4_OLD` / `_NEW` | Discovered v4 Gateway on the WAN's interface; empty when the kernel has no v4 default there or the WAN is `pointToPoint`. |
| `WANWATCH_GATEWAY_V6_OLD` / `_NEW` | Same for v6. |
| `WANWATCH_FAMILIES` | Comma-joined probed Families of the new WAN; `""` when the new WAN is null. |
| `WANWATCH_TABLE` | Routing-table ID (int as string). |
| `WANWATCH_MARK` | fwmark (int as string). |
| `WANWATCH_TS` | Emit time, RFC 3339 nanos UTC. |

### Event matrix

| `WAN_OLD` | `WAN_NEW` | `EVENT` |
|---|---|---|
| `""` | `"primary"` | `up` |
| `"primary"` | `""` | `down` |
| `"primary"` | `"backup"` | `switch` |
| identical | identical | *(no event)* |

### Hook execution

- The daemon submits captured Decision data after Apply. A worker runs accepted notifications in Decision order, keeping their captured timestamps. The queue holds 32 waiting events; when full, it drops the newest event and logs a warning.
- Files run in lexicographic order (`a-first.sh`, `b-second.sh`, …), following the `run-parts` convention.
- At most eight executable files run per event; the rest are skipped and logged.
- Each invocation is a fresh process with the `global.hookTimeoutMs` deadline (5 seconds by default, `state.DefaultHookTimeout`).
- Non-zero exits and timeouts are logged and counted in `wanwatch_hook_invocations_total{event,result}`, but never abort Apply. Hooks are notifications, not gates.
- Daemon shutdown cancels in-flight scripts, kills their process groups, discards queued events, and waits for the worker to finish.

### Example Hook

```sh
#!/bin/sh
# /etc/wanwatch/hooks/switch.d/notify.sh
logger -t wanwatch \
    "$WANWATCH_GROUP: $WANWATCH_WAN_OLD → $WANWATCH_WAN_NEW (families=$WANWATCH_FAMILIES)"
```

## Compatibility policy

Pre-release, `state.SchemaVersion` stays at 1; with no external consumers, in-tree refactors do not bump it.

Post-release, bump `state.SchemaVersion` whenever a field is added, renamed, or changes meaning. This is stricter than `config.json`, whose only reader is the daemon. `state.json` readers are downstream (dashboards, scripts, monitoring agents) and need a schema number to branch on before opting into new fields, so additive bumps are deliberate.
