# Metrics catalog

The daemon serves Prometheus metrics on the Unix socket `/run/wanwatch/metrics.sock`, configurable with `services.wanwatch.global.metricsSocket`. The socket has mode `0660` and owner `wanwatch:wanwatch`; Telegraf reads it through supplementary group membership.

This catalog matches the series registered in `daemon/internal/metrics/metrics.go`. Every name has the `wanwatch_` prefix.

## Probe

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `wanwatch_probe_rtt_seconds` | gauge | `wan`, `target`, `family` | RTT of the last Sample per Target. |
| `wanwatch_probe_jitter_seconds` | gauge | `wan`, `family` | Jitter across the Window. |
| `wanwatch_probe_loss_ratio` | gauge | `wan`, `family` | Loss in `[0, 1]`. |

## WAN

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `wanwatch_wan_carrier` | gauge | `wan` | `1` = carrier up (`IFF_LOWER_UP`), `0` = down. |
| `wanwatch_wan_operstate` | gauge | `wan` | `IFLA_OPERSTATE` integer (`0` = unknown, `6` = up). |
| `wanwatch_wan_family_healthy` | gauge | `wan`, `family` | `1` = healthy after thresholds and Hysteresis. |
| `wanwatch_wan_healthy` | gauge | `wan` | WAN Health under `probe.familyHealthPolicy`. |
| `wanwatch_wan_carrier_changes_total` | counter | `wan` | Carrier transitions. |

## Group

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `wanwatch_group_active` | gauge | `group`, `wan` | `1` for the active Member, `0` for the others. |
| `wanwatch_group_decisions_total` | counter | `group`, `reason` | Decisions. Emitted `reason` values are `health` and `carrier`; `startup` and `manual` are reserved. |

## Apply

Apply metrics are split into per-Family and Family-agnostic series so no label is ever empty.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `wanwatch_apply_route_duration_seconds` | histogram | `group`, `family` | Wall time of `RouteReplace`. |
| `wanwatch_apply_route_errors_total` | counter | `group`, `family` | Route writes that returned a netlink error. |
| `wanwatch_apply_op_errors_total` | counter | `group`, `op` | Family-agnostic errors. Emitted `op` values are `conntrack_flush`, `ifindex_lookup`, and `rule_install`; `state_write` and `hook` are reserved. |

## Daemon

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `wanwatch_state_publications_total` | counter | — | Successful atomic writes of `state.json`. |
| `wanwatch_hook_invocations_total` | counter | `event`, `result` | Hook runs. `event` is `up`, `down`, or `switch`; `result` is `ok`, `nonzero`, or `timeout`. |
| `wanwatch_build_info` | gauge | `version`, `go_version`, `commit` | `1`; the labels identify the binary. |

## Example PromQL

WAN flapping:

```promql
rate(wanwatch_wan_carrier_changes_total[5m]) > 0.05
```

Time since the last switch per Group:

```promql
time() - max by (group) (wanwatch_group_decisions_total)
```

Group without a healthy active Member:

```promql
max by (group) (wanwatch_group_active) == 0
```

Loss above 30% on any (WAN, Family):

```promql
wanwatch_probe_loss_ratio > 0.30
```

RTT across Families on one WAN:

```promql
wanwatch_probe_rtt_seconds{wan="primary"}
```

## Scrape configuration

Enable the companion Telegraf module:

```nix
services.wanwatch.telegraf.enable = true;
services.wanwatch.telegraf.interval = "10s";  # default
```

It adds the equivalent of:

```toml
[[inputs.prometheus]]
  urls = [ "unix:///run/wanwatch/metrics.sock:/metrics" ]
  interval = "10s"
  namepass = [ "wanwatch_*" ]
```

Scrape manually with curl:

```sh
sudo -u telegraf curl --unix-socket /run/wanwatch/metrics.sock \
    http://wanwatch/metrics
```

Keep scrape intervals at 10 s or longer except when debugging; shorter intervals load the daemon without improving observability.
