# daemon-config (frozen spec)

The NixOS module writes this JSON to `/etc/wanwatch/config.json`, and `wanwatchd` reads it at startup. `wanwatch.config.toJSON` in `lib/internal/config.nix` produces it; `daemon/internal/config/config.go` parses it and re-validates its structure.

**Schema version**: `1`, frozen from v0.1.0. The daemon refuses to start when `schema` does not match its `SupportedSchema`; see [Compatibility policy](#compatibility-policy).

## Top-level shape

```json
{
  "schema": 1,
  "global": { ... },
  "wans":   { "<name>": { ... }, ... },
  "groups": { "<name>": { ... }, ... }
}
```

| Field    | Type   | Required | Notes                                                                                                                                                                                                                                                                                                                                                                                                        |
| -------- | ------ | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `schema` | int    | yes      | Always `1` in this spec.                                                                                                                                                                                                                                                                                                                                                                                     |
| `global` | object | yes      | Process-wide settings. `config.render` validates `logLevel` and `hookTimeoutMs` with `wanwatch.global.make`, which fills unset fields from `wanwatch.global.defaults`, and adds the paths from `wanwatch.config.paths`, so the rendered file always has every key; the daemon applies no defaults and rejects missing or empty paths and `hookTimeoutMs`, but reads a missing or empty `logLevel` as `info`. |
| `wans`   | object | yes      | Map from WAN name to WAN object. May be empty.                                                                                                                                                                                                                                                                                                                                                               |
| `groups` | object | yes      | Map from Group name to Group object. May be empty.                                                                                                                                                                                                                                                                                                                                                           |

## Units

Config gives durations in integer milliseconds (`intervalMs`, `timeoutMs`, `rttMsUp`, `rttMsDown`, `hookTimeoutMs`) and loss in integer percentages (`lossPctUp`, `lossPctDown`), the units an operator writes in the Nix options. [`state.json`](./daemon-state.md) and the [metrics](../metrics.md) report seconds and ratios in `[0, 1]` instead (`rttSeconds`, `lossRatio`, `wanwatch_probe_rtt_seconds`), the base units that Prometheus conventions call for. The split is deliberate: each side uses its readers' units, every field name carries its unit, and the daemon converts between them.

## `global`

```json
{
  "statePath": "/run/wanwatch/state.json",
  "hooksDir": "/etc/wanwatch/hooks",
  "metricsSocket": "/run/wanwatch/metrics.sock",
  "logLevel": "info",
  "hookTimeoutMs": 5000
}
```

| Field           | Type   | Default                      | Meaning                                                                             |
| --------------- | ------ | ---------------------------- | ----------------------------------------------------------------------------------- |
| `statePath`     | string | `/run/wanwatch/state.json`   | Path of the atomically written State snapshot.                                      |
| `hooksDir`      | string | `/etc/wanwatch/hooks`        | Root of the Hook tree (`<dir>/{up,down,switch}.d/`).                                |
| `metricsSocket` | string | `/run/wanwatch/metrics.sock` | Unix-socket path of the Prometheus endpoint.                                        |
| `logLevel`      | string | `info`                       | One of `debug`, `info`, `warn`, `error`; the daemon reads an empty value as `info`. |
| `hookTimeoutMs` | int    | `5000`                       | Per-Hook execution deadline in milliseconds. Must be `> 0`.                         |

The paths are not settings: `config.render` always writes `wanwatch.config.paths`, because the service's systemd sandbox makes the file system read-only apart from `/run/wanwatch` and a private `/tmp` ([ADR 0014](../adr/0014-module-owns-identity-and-paths.md)). `wanwatch.global` and the `services.wanwatch.global` options hold only `logLevel` and `hookTimeoutMs`.

## `wans.<name>`

```json
{
  "name": "primary",
  "interface": "eth0",
  "pointToPoint": false,
  "probe": { ... }
}
```

| Field          | Type   | Required             | Meaning                                                                                                                                                            |
| -------------- | ------ | -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `name`         | string | yes                  | Must match the attribute key.                                                                                                                                      |
| `interface`    | string | yes                  | Linux interface name (passes `dev_valid_name`).                                                                                                                    |
| `pointToPoint` | bool   | no (default `false`) | `true` installs `scope link` default routes (PPP, WireGuard, GRE, tun). `false` discovers the Gateway at runtime from the kernel's main routing table via netlink. |
| `probe`        | object | yes                  | Probe configuration; shape below.                                                                                                                                  |

`probe.targets` determines the Families a WAN serves: a non-empty `targets.v4` means v4, and a non-empty `targets.v6` means v6. No separate Gateway or Family declaration exists. The daemon learns each next-hop at runtime and publishes it in [`state.json`](./daemon-state.md) under `wans.<name>.gateways.{v4,v6}`; see [ADR 0004](../adr/0004-runtime-gateway-discovery.md).

## `wans.<name>.probe`

```json
{
  "method": "icmp",
  "targets": { "v4": ["1.1.1.1"], "v6": ["2606:4700:4700::1111"] },
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

| Field                        | Type   | Default  | Meaning                                                                                                                                                                                                                                                                                                         |
| ---------------------------- | ------ | -------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `method`                     | string | `"icmp"` | Probe method. v1: `icmp` only.                                                                                                                                                                                                                                                                                  |
| `targets`                    | object | required | Per-Family Target lists: `{ "v4": [...], "v6": [...] }`. At least one list must be non-empty. Each item must be a valid, unzoned IP literal of its list's Family, with no IPv4-mapped address (`::ffff:0:0/96`) in `v6` and no address twice in one list, compared canonically. Non-canonical text is accepted. |
| `intervalMs`                 | int    | `500`    | Time between cycles.                                                                                                                                                                                                                                                                                            |
| `timeoutMs`                  | int    | `1000`   | Per-cycle read deadline.                                                                                                                                                                                                                                                                                        |
| `windowSize`                 | int    | `10`     | Window capacity.                                                                                                                                                                                                                                                                                                |
| `thresholds.lossPctUp`       | int    | `10`     | Loss% at or below which a flip to up is allowed.                                                                                                                                                                                                                                                                |
| `thresholds.lossPctDown`     | int    | `30`     | Loss% at or above which a flip to down fires.                                                                                                                                                                                                                                                                   |
| `thresholds.rttMsUp`         | int    | `250`    | RTT (ms) at or below which a flip to up is allowed.                                                                                                                                                                                                                                                             |
| `thresholds.rttMsDown`       | int    | `500`    | RTT (ms) at or above which a flip to down fires.                                                                                                                                                                                                                                                                |
| `hysteresis.consecutiveUp`   | int    | `5`      | Healthy cycles needed to flip up.                                                                                                                                                                                                                                                                               |
| `hysteresis.consecutiveDown` | int    | `3`      | Unhealthy cycles needed to flip down.                                                                                                                                                                                                                                                                           |
| `familyHealthPolicy`         | string | `"all"`  | `"all"` or `"any"`. See [`docs/wan-monitoring.md`](../wan-monitoring.md) and [ADR 0005](../adr/0005-family-health-policy-defaults-to-all.md).                                                                                                                                                                   |

The Nix-side validator enforces `lossPctUp < lossPctDown` and `rttMsUp < rttMsDown`, so the threshold band is never empty.

## `groups.<name>`

```json
{
  "name": "home-uplink",
  "members": { "<wan>": { ... }, ... },
  "strategy": "primary-backup",
  "table": 1000,
  "mark": 1000
}
```

| Field      | Type   | Required | Meaning                                                                                            |
| ---------- | ------ | -------- | -------------------------------------------------------------------------------------------------- |
| `name`     | string | yes      | Must match the attribute key.                                                                      |
| `members`  | object | yes      | Non-empty map from WAN name to Member object.                                                      |
| `strategy` | string | yes      | v1: `"primary-backup"`.                                                                            |
| `table`    | int    | yes      | Routing-table ID. User-declared integer in `[1000, 32767]` (type `wanwatch.types.routingTableId`). |
| `mark`     | int    | yes      | fwmark. User-declared integer in `[1000, 32767]` (type `wanwatch.types.fwmark`).                   |

See [ADR 0003](../adr/0003-user-declared-marks-and-tables.md) for why marks and tables are not allocated automatically.

## `groups.<name>.members.<wan>`

```json
{
  "wan": "primary",
  "priority": 1
}
```

| Field      | Type   | Required | Meaning                                                                        |
| ---------- | ------ | -------- | ------------------------------------------------------------------------------ |
| `wan`      | string | yes      | Must match the attribute key, which must be a key in the top-level `wans` map. |
| `priority` | int    | yes      | Positive integer. Lower is preferred under `primary-backup`.                   |

## Validation layers

| Layer                               | Catches                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| ----------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Option types (`lib/types/`)         | Wrong field types, enum mismatches, malformed IP literals (via libnet).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `wanwatch.<type>.tryMake`           | Unknown fields, which a direct library caller could otherwise misspell unnoticed, and cross-field invariants: Family coupling, IPv4-mapped Targets in `v6`, duplicate Targets in one list compared canonically, Member `wan`/key disagreement, threshold ordering.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `config.render`                     | Invalid `global` settings, through `wanwatch.global.make`: unknown fields, including the paths, an unknown `logLevel`, or a non-positive `hookTimeoutMs`; and Member `wan` references that `wans` does not declare.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `config.assertUniqueMarksAndTables` | Marks or tables shared across Groups.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `daemon/internal/config/Validate`   | Unknown keys and trailing data at parse time, then the first failing check of: empty paths or non-positive `hookTimeoutMs` in `global`; name/key disagreement; empty `interface`; `method` other than `icmp`; no Targets; a Target that is not a valid IP literal, is zoned, is not of its list's Family, is IPv4-mapped in `v6`, or repeats an address in its list (non-canonical text is accepted); non-positive `intervalMs`, `timeoutMs`, or `windowSize`; `familyHealthPolicy` other than `all` or `any`; loss thresholds outside `0..100` or not ordered up < down; RTT thresholds not positive or not ordered up < down; Hysteresis counters below 1; unknown `strategy`; non-positive `table` or `mark`; no Members; Member `wan`/key disagreement; dangling `member.wan` references. |

## Compatibility policy

From v0.1.0, schema 1 is frozen ([ADR-0018](../adr/0018-0x-stability-promise.md)); the changes before that release kept schema 1, because no release had shipped it. No field is removed or renamed, none changes its type or meaning, and the daemon never starts to require a new field. A release may add a field that the daemon accepts as absent, because a `config.json` written for the previous release still starts the daemon with the same meaning.

The committed golden `daemon/internal/config/testdata/config.golden.json` pins the shape on both sides. The nix-unit test `unit.internal.config.render.testMatchesGolden` checks that `config.render` produces it from the unit fixtures, and the daemon's config tests parse it and re-encode it to the same document. A change to the rendered shape, breaking or not, regenerates the golden in the same commit:

```sh
nix eval --json .#tests.systems.x86_64-linux.unit.internal.config.render.testMatchesGolden.expr \
  | jq . > daemon/internal/config/testdata/config.golden.json
```

The renderer and the daemon ship together, and the daemon rejects unknown fields. Any change to the rendered shape, even an added optional field, therefore needs a daemon of the same release: an older daemon refuses the new file. A `services.wanwatch.package` override must come from the same release as the module.

An incompatible change needs a new ADR that supersedes ADR-0018, and then:

1. Increment `schemaVersion` in `lib/internal/config.nix` and `SupportedSchema` in `daemon/internal/config/config.go`.
2. Update this spec.
3. Add a `CHANGELOG.md` entry with the migration note under the next release.
