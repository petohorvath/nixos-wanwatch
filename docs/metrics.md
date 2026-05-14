# Metrics catalog

The daemon exposes Prometheus-format metrics over a Unix socket at `/run/wanwatch/metrics.sock` (configurable via `services.wanwatch.global.metricsSocket`). The socket is mode `0660` and owned by `wanwatch:wanwatch`; Telegraf reads via supplementary group membership.

Every metric is prefixed `wanwatch_`. The catalog below is the source of truth — `daemon/internal/metrics/metrics.go` registers exactly these series.

## Probe layer

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `wanwatch_probe_rtt_seconds` | gauge | `wan`, `target`, `family` | Last sample's RTT per probe target. |
| `wanwatch_probe_jitter_seconds` | gauge | `wan`, `family` | Per-(WAN, family) jitter across the sliding window. |
| `wanwatch_probe_loss_ratio` | gauge | `wan`, `family` | Per-(WAN, family) packet loss in `[0, 1]`. |

## WAN layer

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `wanwatch_wan_carrier` | gauge | `wan` | `1` = carrier up (IFF_LOWER_UP), `0` = down. |
| `wanwatch_wan_operstate` | gauge | `wan` | IFLA_OPERSTATE integer (`0`=unknown, `6`=up). |
| `wanwatch_wan_family_healthy` | gauge | `wan`, `family` | `1` = healthy under thresholds + hysteresis. |
| `wanwatch_wan_healthy` | gauge | `wan` | Aggregate per `probe.familyHealthPolicy`. |
| `wanwatch_wan_carrier_changes_total` | counter | `wan` | Carrier transitions observed. |

## Group layer

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `wanwatch_group_active` | gauge | `group`, `wan` | `1` for the currently active Member; `0` for the others. |
| `wanwatch_group_decisions_total` | counter | `group`, `reason` | Decisions emitted. `reason ∈ {health, carrier, startup, manual}` (`manual` reserved for `wanwatchctl` post-v1). |

## Apply layer

Split into per-family and family-agnostic so labels never collapse to empty.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `wanwatch_apply_route_duration_seconds` | histogram | `group`, `family` | Wall time of `RouteReplace`. |
| `wanwatch_apply_route_errors_total` | counter | `group`, `family` | Route writes that returned a netlink error. |
| `wanwatch_apply_op_errors_total` | counter | `group`, `op` | Errors per family-agnostic op. `op ∈ {conntrack_flush, state_write, hook, rule_install}`. |

## Daemon-wide

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `wanwatch_state_publications_total` | counter | — | Successful atomic writes of `state.json`. |
| `wanwatch_hook_invocations_total` | counter | `event`, `result` | Hook invocations. `event ∈ {up, down, switch}`, `result ∈ {ok, nonzero, timeout}`. |
| `wanwatch_build_info` | gauge | `version`, `go_version`, `commit` | Set to `1` at startup; labels identify the binary. |

## Example PromQL

Detect a WAN flapping:

```promql
rate(wanwatch_wan_carrier_changes_total[5m]) > 0.05
```

Per-group time-since-last-switch:

```promql
time() - max by (group) (wanwatch_group_decisions_total)
```

Alert when no group has a healthy active member:

```promql
max by (group) (wanwatch_group_active) == 0
```

Probe loss above 30% on any (WAN, family):

```promql
wanwatch_probe_loss_ratio > 0.30
```

Compare RTT across families on the same WAN:

```promql
wanwatch_probe_rtt_seconds{wan="primary"}
```

## Scrape configuration

Telegraf (via the opt-in companion module):

```nix
services.wanwatch.telegraf.enable = true;
services.wanwatch.telegraf.interval = "10s";  # default
```

The module pushes the equivalent of:

```toml
[[inputs.prometheus]]
  urls = [ "unix:///run/wanwatch/metrics.sock:/metrics" ]
  interval = "10s"
  namepass = [ "wanwatch_*" ]
```

Raw Prometheus / curl scrape:

```sh
sudo -u telegraf curl --unix-socket /run/wanwatch/metrics.sock \
    http://wanwatch/metrics
```

Sub-10 s scrape intervals stress the daemon's per-cycle hot path with no observability benefit — keep at ≥10 s unless debugging.
