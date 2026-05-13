# Failover semantics (frozen spec)

The v1 contract: **single-active per Group, deterministic Strategy, atomic apply**. This doc nails down the edge cases.

## Definitions

| Term | Meaning |
|---|---|
| **Selection** | The current chosen Member per Group. `null` ⇒ no Selection. |
| **Decision** | A Selection *change* — old → new. Fires the apply pipeline. |
| **Apply** | route write + state snapshot + hook dispatch + metric update, in that order. |

A Group always has exactly one Selection (which may be `null`). The selector emits at most one Decision per recompute.

## When a Decision fires

| Event | Trigger | Reason label |
|---|---|---|
| `ProbeResult` flips a family's Healthy | `wan.healthy` aggregate changes | `health` |
| `LinkEvent` flips `carrierUp()` | rtnl carrier or operstate change | `carrier` |
| Daemon startup | Initial Selection from cold-start carrier-only health | (none yet — `startup` reserved) |
| `wanwatchctl set <group> <wan>` | Manual override | `manual` (reserved for post-v1) |

A given event recomputes every Group containing the affected WAN. Each Group's `recomputeGroup` is independent; one Group's Decision does not gate another's.

## Cold-start invariant

Until the first `ProbeResult` lands for a (WAN, family), that family votes "healthy" in `combineFamilies`. Combined with `carrierUp()`, this means:

1. Daemon boots, rules installed, no probes have completed.
2. rtnl reports `carrier=up, operstate=up` on the primary.
3. `buildMemberHealth` ⇒ primary healthy.
4. `selector.Select` picks primary.
5. `apply.WriteDefault` writes the default route.
6. State + hooks fire.

The whole chain happens in sub-second on a healthy boot, before any probe finishes.

If the first ProbeResult is Lost samples (target unreachable), the hysteresis verdict stays unhealthy and the WAN flips down — same path as a steady-state failure, just compressed to one cycle.

## Failover semantics

```
t=0      primary healthy, backup healthy
         active = primary
         table 100 v4 default = via primary's gw, dev wan0
         table 100 v6 default = via primary's gw, dev wan0

t=1      carrier drops on wan0
         rtnl emits LinkEvent{Name: wan0, Carrier: down}
         handleLinkEvent flips primary's carrierUp() = false
         buildMemberHealth: primary unhealthy, backup healthy
         selector.Select: active = backup

         apply.WriteDefault(family=v4, table=100, gw=backup.v4, ifindex=wan1)
         apply.WriteDefault(family=v6, table=100, gw=backup.v6, ifindex=wan1)
         state.Writer.Write: active=backup, activeSince=t1
         state.Runner.Run /etc/wanwatch/hooks/switch.d/*
         wanwatch_group_decisions_total{reason=carrier}++

t=2      table 100 reflects backup; traffic that was marked routes via wan1
```

Steps inside `t=1` run sequentially on a single goroutine. The apply layer never races with itself; readers of `state.json` always see a complete snapshot.

## Recovery semantics

```
t=10     carrier returns on wan0
         rtnl emits LinkEvent{Name: wan0, Carrier: up}
         handleLinkEvent flips primary's carrierUp() = true
         buildMemberHealth: primary healthy AGAIN (cold-start path —
                            primary.healthy was true at boot and never
                            went false from probes)
         selector.Select: active = primary (lower priority among healthy)
         Decision fires; routes rewritten; hooks run.
```

If the down was probe-driven (not carrier-driven), `wan.healthy` is `false` until consecutiveUp samples accumulate. The recovery latency is `intervalMs * consecutiveUp` in steady state. Defaults: 3 × 1 s = 3 s.

## Single-active invariant

v1 picks exactly one Member per Group (or zero). The selector never returns multiple Active members; the apply layer never installs multipath routes.

Multi-active arrives with the `load-balance` strategy in v2 — the apply layer's `RouteReplace` path will need MultiPath nexthops, and the metrics catalog will gain `wanwatch_group_active{group,wan}` with multiple `1`s per group.

## Determinism

Replaying the same event sequence (config + ProbeResults + LinkEvents) always produces the same sequence of Decisions. The two stateful elements:

- `WindowStats` — deterministic given Sample ordering.
- `HysteresisState` — deterministic given observed-bool ordering.

Both have unit tests that assert determinism across many invocations.

## What can break this

| Failure mode | Daemon behavior |
|---|---|
| `apply.WriteDefault` errors | Decision proceeds; route stays stale until next Decision retries. `wanwatch_apply_route_errors_total{group,family}` increments. |
| `state.Writer.Write` errors | Logged; metric increments; previous `state.json` stays in place (atomic write semantics). |
| A hook times out (5 s default) | Logged; counted; next hooks in the .d directory still run. |
| ProbeResult arrives during Decision | Queued on channel buffer; processed after the current Decision completes. |
| LinkEvent arrives during Decision | Same — single goroutine drains both channels in order. |
| All members unhealthy | `active = null`; routes left in place from the previous Selection; no Decision fires (the previous Selection wasn't changed *to* null this cycle — it just observed unhealthy members). |

The "stale routes when all unhealthy" point is intentional. Tearing down the last default route would create a different problem (no egress at all). Operators wanting a different policy can write a hook that runs `ip route flush table <T>` on the `down` event.

## What's out of scope for v1

- Conntrack flush on failover (`apply.FlushBySource` exists but isn't wired into `recomputeGroup`; PLAN §12 OQ #3).
- Per-Group override at runtime (`wanwatchctl set`).
- Notification of unhealthy WANs that are *not* currently the Active — the daemon publishes per-WAN Health in `state.json` and `wanwatch_wan_healthy` but emits no Decision until the *Selection* changes.
- Probe-failure recovery while carrier stays up — covered by hysteresis, but no explicit "wait 30 minutes before re-trying" backoff. Probing continues at `intervalMs` forever.
