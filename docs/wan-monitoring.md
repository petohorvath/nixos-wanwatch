# WAN monitoring

The model in three sentences: a host has one or more uplinks (WANs); each WAN has a Probe configuration that defines how it's tested; Groups bundle WANs under a Strategy that picks which one carries traffic. When health changes, the daemon rewrites the relevant routing table and dispatches a hook.

This doc walks through that model end-to-end with a working configuration.

## A single WAN

A WAN is an egress interface; the daemon learns its current default-route gateway from the kernel at runtime. v4-only, v6-only, and dual-stack are all valid — the families the WAN serves are derived from `probe.targets` (v4 literals mean it serves v4, v6 literals mean it serves v6).

```nix
services.wanwatch.wans.primary = {
  interface = "eth0";
  probe.targets = [ "1.1.1.1" "8.8.8.8" ];
};
```

For point-to-point links with no broadcast next-hop (PPP, WireGuard, GRE, tun), set `pointToPoint = true`; the daemon installs a `scope link` default route out of the interface instead of looking up a gateway.

```nix
services.wanwatch.wans.lte = {
  interface = "wwan0";
  pointToPoint = true;
  probe.targets = [ "8.8.8.8" ];
};
```

The Probe is the *configuration* of how the WAN is tested, not the test itself. Defaults — interval 1 s, timeout 1 s, window 10 samples, loss thresholds 10/50, RTT thresholds 200/1000 ms, hysteresis 3/3 — are usable as-is for the typical router workload. Override per-WAN:

```nix
probe = {
  targets = [ "1.1.1.1" "9.9.9.9" ];
  intervalMs = 500;
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

## Probes and the sliding window

Every probe Cycle (`intervalMs`) sends one ICMP echo per target. Replies that come back within `timeoutMs` produce a Sample with RTT; missing replies produce a Lost sample.

The most recent `windowSize` Samples per target feed three statistics:

| Stat | Computation |
|---|---|
| `LossRatio` | `lost / total` in `[0, 1]` |
| `MeanRTT` | mean over non-Lost Samples |
| `JitterMicros` | population stddev over non-Lost Samples |

Per-target stats average across the WAN's targets into a per-family aggregate; that aggregate is what the threshold layer sees.

## Thresholds (band-pass)

Two thresholds per metric form a band:

| Currently healthy? | Flip to unhealthy when | Flip to healthy when |
|---|---|---|
| Yes | `loss ≥ lossPctDown` OR `rtt ≥ rttMsDown` | (already healthy) |
| No | (already unhealthy) | `loss ≤ lossPctUp` AND `rtt ≤ rttMsUp` |

Between the bands the verdict holds. The Nix-side validator enforces `Up < Down` for both metrics, so the band is always non-empty.

## Hysteresis

The band-pass output feeds a consecutive-cycle state machine. A flip in either direction requires `consecutiveUp` (or `consecutiveDown`) successive observations in the new direction. Single-cycle blips do not propagate.

## Carrier and operstate

`rtnl` subscribes to RTNLGRP_LINK and feeds the daemon a `LinkEvent` whenever the kernel reports a change in:

- `Carrier` — physical link state (`IFF_LOWER_UP`).
- `Operstate` — RFC 2863 oper state (`UP`, `DORMANT`, `LOWERLAYERDOWN`, …).

A carrier-down event fast-tracks the WAN to unhealthy without waiting for the probe loop to time out. The cold-start path runs in reverse: until the first ProbeResult lands, carrier-up alone is enough to mark a member healthy, so a freshly-booted daemon publishes a Selection immediately.

## Per-family Health and policy

Each declared (WAN, family) tuple produces an independent Healthy verdict. The verdicts combine into a per-WAN Healthy under `probe.familyHealthPolicy`:

| Policy | Result |
|---|---|
| `"all"` (default) | every probed family must be healthy |
| `"any"` | at least one probed family must be healthy |

The cold-start path treats uncooked families (no ProbeResult yet) as a healthy vote — see [`docs/selector.md`](./selector.md).

## Groups and Strategies

A Group is an ordered list of Members under a Strategy. Members reference a WAN by name and carry per-Group attributes (priority, weight).

```nix
services.wanwatch.groups.home-uplink.members = [
  { wan = "primary"; priority = 1; }
  { wan = "backup";  priority = 2; }
];
```

The v1 Strategy is `primary-backup`: among healthy Members, pick the one with the lowest `priority`; ties broken by lexicographic WAN name. `weight` is reserved for v2's multi-active strategies and is ignored today.

If no Member is healthy, the Group has no Selection — `state.json` shows `active: null` and the daemon installs no default route. The fwmark rule itself stays in place (no traffic gets a usable next hop until at least one Member recovers).

## What the daemon does on a switch

`Decision = Selection change`. When `selector.Select` returns an Active that differs from the previous Selection, the daemon runs, in order:

1. `apply.WriteDefault` per family the new active serves — for point-to-point WANs the route is `scope link`; for normal WANs the daemon reads the discovered next-hop from its in-memory gateway cache. `RouteReplace` is idempotent, so a stale default in the same table is overwritten atomically. A cache miss (kernel hasn't installed a default on that link yet) skips the write; a subsequent route-discovery event triggers a reapply.
2. `state.Writer.Write` — atomic tmpfile + rename. Readers see either the old or new file, never a partial one.
3. `state.Runner.Run` — dispatches `/etc/wanwatch/hooks/{up,down,switch}.d/*` with the `WANWATCH_*` env vars from [`docs/specs/daemon-state.md`](./specs/daemon-state.md).
4. Metrics — `wanwatch_group_decisions_total{group,reason}` increments; `wanwatch_group_active{group,wan}` updates.

Conntrack flush (`apply.FlushBySource`) lives in the apply package but is not wired into the per-Decision path yet — see PLAN §12 OQ #3 for the v0.2 hook.

## Where to go next

- [`docs/selector.md`](./selector.md) — the threshold + hysteresis + strategy chain in detail.
- [`docs/architecture.md`](./architecture.md) — full layering diagram and data flow on a switch.
- [`docs/metrics.md`](./metrics.md) — every Prometheus series the daemon exposes.
- [`docs/specs/`](./specs/) — frozen wire-format contracts.
