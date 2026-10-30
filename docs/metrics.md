# Metrics catalog

The daemon serves Prometheus metrics on the Unix socket `/run/wanwatch/metrics.sock`. The socket has mode `0660` and owner `wanwatch:wanwatch`; Telegraf reads it through supplementary group membership.

This catalog matches the series registered in `daemon/internal/metrics/metrics.go`. Every name has the `wanwatch_` prefix.

## Probe

| Metric                          | Type  | Labels                    | Meaning                            |
| ------------------------------- | ----- | ------------------------- | ---------------------------------- |
| `wanwatch_probe_rtt_seconds`    | gauge | `wan`, `target`, `family` | RTT of the last Sample per Target. |
| `wanwatch_probe_jitter_seconds` | gauge | `wan`, `family`           | Jitter across the Window.          |
| `wanwatch_probe_loss_ratio`     | gauge | `wan`, `family`           | Loss in `[0, 1]`.                  |

## WAN

| Metric                               | Type    | Labels          | Meaning                                                        |
| ------------------------------------ | ------- | --------------- | -------------------------------------------------------------- |
| `wanwatch_wan_carrier`               | gauge   | `wan`           | `1` = carrier up (`IFF_LOWER_UP`), `0` = down, `-1` = unknown. |
| `wanwatch_wan_operstate`             | gauge   | `wan`, `state`  | Operstate as a state-set; see below.                           |
| `wanwatch_wan_family_healthy`        | gauge   | `wan`, `family` | `1` = healthy after thresholds and Hysteresis.                 |
| `wanwatch_wan_healthy`               | gauge   | `wan`           | WAN Health under `probe.familyHealthPolicy`.                   |
| `wanwatch_wan_carrier_changes_total` | counter | `wan`           | Carrier transitions.                                           |

Both gauges are set for every WAN at daemon start and report unknown until the kernel reports the interface's link state, for example while the interface does not exist. `wanwatch_wan_carrier` encodes unknown as `-1`, so an alert on `== 0` fires only on a carrier the kernel reports down.

`wanwatch_wan_operstate` has one series per WAN for each RFC 2863 operstate: `up`, `down`, `dormant`, `lowerlayerdown`, `notpresent`, `testing`, and `unknown`. The series of the current state is `1` and the others are `0`. A kernel value outside this set reports as `unknown`.

## Group

| Metric                           | Type    | Labels            | Meaning                                                                                                |
| -------------------------------- | ------- | ----------------- | ------------------------------------------------------------------------------------------------------ |
| `wanwatch_group_selected`        | gauge   | `group`, `wan`    | `1` for the Group's Selection, `0` for its other Members. Every Member is exposed at `0` from startup. |
| `wanwatch_group_decisions_total` | counter | `group`, `reason` | Decisions. `reason` is `health` or `carrier`.                                                          |

## Apply

Apply metrics are split into per-Family and Family-agnostic series so no label is ever empty.

| Metric                                  | Type      | Labels            | Meaning                                                                                 |
| --------------------------------------- | --------- | ----------------- | --------------------------------------------------------------------------------------- |
| `wanwatch_apply_route_duration_seconds` | histogram | `group`, `family` | Wall time of `RouteReplace`.                                                            |
| `wanwatch_apply_route_errors_total`     | counter   | `group`, `family` | Route writes that returned a netlink error.                                             |
| `wanwatch_apply_op_errors_total`        | counter   | `group`, `op`     | Family-agnostic errors. `op` is `conntrack_flush`, `ifindex_lookup`, or `rule_install`. |

## Daemon

| Metric                              | Type    | Labels                             | Meaning                                                                                     |
| ----------------------------------- | ------- | ---------------------------------- | ------------------------------------------------------------------------------------------- |
| `wanwatch_state_publications_total` | counter | —                                  | Successful atomic writes of `state.json`.                                                   |
| `wanwatch_hook_invocations_total`   | counter | `event`, `result`                  | Hook runs. `event` is `up`, `down`, or `switch`; `result` is `ok`, `nonzero`, or `timeout`. |
| `wanwatch_build_info`               | gauge   | `version`, `revision`, `goversion` | `1`; the labels identify the binary.                                                        |

The `wanwatch_build_info` labels follow Prometheus's own `*_build_info` series. `revision` is the flake's short revision, its dirty short revision for a dirty tree, or `unknown` for a build without Git metadata. `goversion` is the Go toolchain version.

## Example PromQL

WAN flapping:

```promql
rate(wanwatch_wan_carrier_changes_total[5m]) > 0.05
```

WAN link down, without firing on an unknown carrier or operstate:

```promql
wanwatch_wan_carrier == 0 or wanwatch_wan_operstate{state="down"} == 1
```

Decisions per Group over the last hour:

```promql
sum by (group) (increase(wanwatch_group_decisions_total[1h]))
```

No metric records when a Decision happened. `state.json` records when each Group's Selection last changed, as `groups.<name>.selectionSince`: when the current Selection took over, or when the Group lost its last healthy Member.

Group without a Selection, including a Group that never had a healthy Member:

```promql
max by (group) (wanwatch_group_selected) == 0
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
  urls = [ "unix:///run/wanwatch/metrics.sock" ]
  interval = "10s"
  namepass = [ "wanwatch_*" ]
```

Scrape manually with curl:

```sh
sudo -u telegraf curl --unix-socket /run/wanwatch/metrics.sock \
    http://wanwatch/metrics
```

Keep scrape intervals at 10 s or longer except when debugging; shorter intervals load the daemon without improving observability.
