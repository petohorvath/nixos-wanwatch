# Failover semantics (frozen spec)

The v1 contract is **single-active per Group, deterministic Strategy, atomic Apply**; see [ADR 0001](../adr/0001-single-active-failover.md). This spec pins down the edge cases. Terms follow [`GLOSSARY.md`](../../GLOSSARY.md).

## Definitions

| Term | Meaning |
|---|---|
| **Selection** | The current chosen Member per Group. `null` means no Selection. |
| **Decision** | A Selection change, old → new. Starts the Apply pipeline. |
| **Apply** | Route write, State snapshot, Hook dispatch, and metric update, in that order. |

A Group always has exactly one Selection, which may be `null`. The selector emits at most one Decision per recompute.

## When a Decision fires

| Event | Trigger | Reason label |
|---|---|---|
| `ProbeResult` flips a Family's Health | `wan.healthy` aggregate changes | `health` |
| `LinkEvent` flips `carrierUp()` | rtnl carrier or operstate change | `carrier` |
| Daemon startup | Initial Selection from cold-start carrier-only Health | none yet; `startup` reserved |
| `wanwatchctl set <group> <wan>` | Manual override | `manual`, reserved for post-v1 |

An event recomputes every Group containing the affected WAN. Each `decision.Group` owns its Selection independently, so one Group's pending Decision never blocks another's commit.

## Cold-start invariant

Until the first `ProbeResult` lands for a (WAN, Family), that Family votes healthy in `combineFamilies`. Combined with `carrierUp()`, a healthy boot runs this chain in under a second, before any Probe finishes:

1. The daemon boots and installs rules; no Probes have completed.
2. rtnl reports `carrier=up, operstate=up` on the primary.
3. `buildMemberHealth` marks the primary healthy.
4. `selector.Select` picks the primary.
5. `apply.WriteDefault` writes the default route.
6. State and Hooks fire.

If the first ProbeResult contains Lost Samples (Target unreachable), the Hysteresis verdict stays unhealthy and the WAN flips down: the steady-state failure path, compressed to one cycle.

## Failover

```text
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
         state.HookNotifier.Notify queues the switch notification
         wanwatch_group_decisions_total{reason=carrier}++

t=2      table 100 reflects backup; marked traffic routes via wan1
```

The `t=1` steps run sequentially on one goroutine. Apply never races with itself, and `state.json` readers always see a complete snapshot.

## Recovery

```text
t=10     carrier returns on wan0
         rtnl emits LinkEvent{Name: wan0, Carrier: up}
         handleLinkEvent flips primary's carrierUp() = true
         buildMemberHealth: primary healthy again (cold-start path:
                            primary.healthy was true at boot and never
                            went false from probes)
         selector.Select: active = primary (lower priority among healthy)
         Decision fires; routes rewritten; hooks run.
```

After a Probe-driven failure (rather than a carrier loss), `wan.healthy` stays `false` until `consecutiveUp` Samples accumulate. Steady-state recovery latency is `intervalMs × consecutiveUp`: 3 × 1 s = 3 s with the defaults.

## Single-active invariant

v1 selects exactly one Member per Group, or none. The selector never returns multiple active Members, and Apply never installs multipath routes.

Multi-active arrives with the v2 `load-balance` Strategy. Apply's `RouteReplace` path will then need multipath nexthops, and `wanwatch_group_active{group,wan}` will report several `1`s per Group.

## Determinism

Replaying the same event sequence (config, ProbeResults, LinkEvents) always produces the same Decision sequence. Both stateful elements are deterministic, and unit tests assert this across many invocations:

- `WindowStats`, given the Sample order.
- `HysteresisState`, given the order of observed booleans.

## Failure modes

| Failure mode | Daemon behavior |
|---|---|
| `apply.WriteDefault` errors | The Decision stays pending; the committed Selection and Hooks stay unchanged. Probe cycles or Gateway changes on the pending WAN retry Apply. `wanwatch_apply_route_errors_total{group,family}` increments. |
| `state.Writer.Write` errors | Logged and counted; the previous `state.json` stays in place (atomic write). |
| A Hook times out (5 s default) | Logged and counted; the remaining Hooks in the `.d` directory still run. |
| ProbeResult arrives during a Decision | Buffered on its channel; processed after the current Decision completes. |
| LinkEvent arrives during a Decision | Same: one goroutine drains both channels in order. |
| All Members unhealthy | A transition to `active = null` commits without route writes and emits a `down` Hook if a Member was active. Routes stay in place; repeated all-down observations emit no Decision. |

Keeping stale routes when every Member is unhealthy is intentional: removing the last default route would leave no egress at all. Operators who want a different policy can add a `down` Hook that runs `ip route flush table <T>`.

## Out of scope for v1

- Runtime per-Group override (`wanwatchctl set`).
- Notifications for unhealthy WANs that are not active. `state.json` and `wanwatch_wan_healthy` publish per-WAN Health, but no Decision fires until the Selection changes.
- Backoff for Probe failures while carrier stays up. Hysteresis covers recovery, and probing continues at `intervalMs` indefinitely.
