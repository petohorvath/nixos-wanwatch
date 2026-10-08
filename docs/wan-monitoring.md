# WAN monitoring

A host has one or more WANs. Each WAN has a Probe that defines how it is tested, and Groups bundle WANs under a Strategy that picks the one carrying traffic. When a Selection changes, the daemon rewrites the Group's routing table and runs Hooks.

## A single WAN

A WAN is an egress interface plus a Probe. Its Families come from `probe.targets`: a non-empty `targets.v4` serves IPv4 and a non-empty `targets.v6` serves IPv6, so v4-only, v6-only, and dual-stack WANs are all valid. The daemon discovers each Family's Gateway from the kernel at runtime.

```nix
services.wanwatch.wans.primary = {
  interface = "eth0";
  probe.targets.v4 = [ "1.1.1.1" "8.8.8.8" ];
};
```

Point-to-point links without a broadcast next-hop (PPP, WireGuard, GRE, tun) set `pointToPoint = true`. The daemon then installs a `scope link` default route out of the interface instead of using a Gateway.

```nix
services.wanwatch.wans.lte = {
  interface = "wwan0";
  pointToPoint = true;
  probe.targets.v4 = [ "8.8.8.8" ];
};
```

## Probe defaults

Every Probe field except `targets` has a default:

| Field | Default |
|---|---|
| `method` | `icmp` |
| `intervalMs` | `500` |
| `timeoutMs` | `1000` |
| `windowSize` | `10` |
| `thresholds.lossPctUp` / `lossPctDown` | `10` / `30` |
| `thresholds.rttMsUp` / `rttMsDown` | `250` / `500` |
| `hysteresis.consecutiveUp` / `consecutiveDown` | `5` / `3` |
| `familyHealthPolicy` | `all` |

Override them per WAN:

```nix
probe = {
  targets.v4 = [ "1.1.1.1" "9.9.9.9" ];
  intervalMs = 1000;
  windowSize = 20;
  thresholds = {
    lossPctUp = 5;
    lossPctDown = 30;
    rttMsUp = 150;
    rttMsDown = 500;
  };
  hysteresis = {
    consecutiveUp = 5;
    consecutiveDown = 3;
  };
};
```

## Samples and the Window

Every `intervalMs`, the Probe sends one ICMP echo per Target. A reply within `timeoutMs` produces a Sample with an RTT; a missing reply produces a lost Sample.

The most recent `windowSize` Samples per Target form the Window, which yields three statistics:

| Statistic | Computation |
|---|---|
| `LossRatio` | `lost / total`, in `[0, 1]` |
| `MeanRTT` | mean over non-lost Samples |
| `JitterMicros` | population standard deviation over non-lost Samples |

Per-Target statistics average into a per-Family aggregate, which the thresholds evaluate. The full algorithm is in [`specs/probe-algorithm.md`](./specs/probe-algorithm.md).

## Thresholds and Hysteresis

Two thresholds per metric form a band:

| Current Health | Becomes unhealthy when | Becomes healthy when |
|---|---|---|
| healthy | `loss ≥ lossPctDown` or `rtt ≥ rttMsDown` | — |
| unhealthy | — | `loss ≤ lossPctUp` and `rtt ≤ rttMsUp` |

Between the bands, Health holds. The Nix validator enforces `Up < Down` for both metrics, so the band is never empty.

Hysteresis then requires `consecutiveUp` or `consecutiveDown` successive observations in the new direction before Health flips, so single-cycle blips do not propagate. See [`selector.md`](./selector.md#hysteresis).

## Carrier and operstate

`rtnl` subscribes to `RTNLGRP_LINK` and emits a `LinkEvent` whenever the kernel reports a change in:

- `Carrier`: physical link state (`IFF_LOWER_UP`).
- `Operstate`: RFC 2863 operational state (`UP`, `DORMANT`, `LOWERLAYERDOWN`, …).

Carrier loss makes the WAN unhealthy immediately, without waiting for Probe timeouts. At cold start, carrier-up alone marks a Member healthy until its first full Window, so a freshly started daemon publishes a Selection immediately.

## Family Health policy

Each (WAN, Family) has its own Health. `probe.familyHealthPolicy` combines them into WAN Health:

| Policy | WAN is healthy when |
|---|---|
| `all` (default) | every probed Family is healthy |
| `any` | at least one probed Family is healthy |

A Family without a full Window yet counts as healthy. See [`selector.md`](./selector.md#family-policy-aggregation) and [ADR 0005](./adr/0005-family-health-policy-defaults-to-all.md).

## Groups and Strategies

A Group is an ordered list of Members under a Strategy. Each Member references a WAN by name and carries per-Group attributes (`priority`, `weight`).

```nix
services.wanwatch.groups.home-uplink = {
  members = [
    { wan = "primary"; priority = 1; }
    { wan = "backup";  priority = 2; }
  ];
  mark  = 1000;
  table = 1000;
};
```

`mark` and `table` are required integers in `[1000, 32767]`, typed as `wanwatch.types.fwmark` and `routingTableId`. At startup the daemon installs `ip rule add fwmark <mark> table <table>` and then owns that table's contents. The module re-exposes both values as `services.wanwatch.marks.<group>` and `services.wanwatch.tables.<group>`, so firewall modules reference them by name. See [ADR 0003](./adr/0003-user-declared-marks-and-tables.md).

The only Strategy is `primary-backup`: the healthy Member with the lowest `priority` wins, and ties go to the lexicographically first WAN name. `weight` is reserved for multi-active Strategies and is ignored.

When no Member is healthy, the Group has no Selection and `state.json` shows `active: null`. The daemon writes no routes, so the previous default route and the fwmark rule stay in place until a Member recovers.

## What happens on a Decision

A Decision is a Selection change. When `selector.Select` returns a different active Member, the daemon runs these steps in order:

1. `apply.WriteDefault` writes the default route for each Family the new Member serves. Point-to-point WANs get a `scope link` route; others use the cached Gateway. `RouteReplace` overwrites a stale default atomically. If no Gateway is known yet, the write is skipped and the next route event reapplies it.
2. `apply.FlushBySource` flushes conntrack entries for the vacated WAN's source addresses, so SNATted flows re-establish through the new WAN instead of being black-holed. It runs only on a switch, not when the Group goes down. A failure increments `wanwatch_apply_op_errors_total{op="conntrack_flush"}` but never fails the Decision.
3. `state.Writer.Write` publishes `state.json` atomically with a temporary file and rename, so readers never see a partial file.
4. `state.HookNotifier.Notify` queues best-effort delivery to `/etc/wanwatch/hooks/{up,down,switch}.d/*` with the `WANWATCH_*` environment variables in [`specs/daemon-state.md`](./specs/daemon-state.md).
5. `wanwatch_group_decisions_total{group,reason}` increments and `wanwatch_group_active{group,wan}` updates.

## Further reading

- [`selector.md`](./selector.md): thresholds, Hysteresis, and Strategy in detail.
- [`architecture.md`](./architecture.md): layers and the data flow on a switch.
- [`metrics.md`](./metrics.md): the Prometheus catalog.
- [`specs/`](./specs/): frozen wire-format contracts.
