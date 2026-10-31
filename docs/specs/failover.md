# Failover semantics (frozen spec)

The v1 contract is **single-active per Group, deterministic Strategy, atomic Apply**; see [ADR 0001](../adr/0001-single-active-failover.md). This spec pins down the edge cases. Terms follow [`GLOSSARY.md`](../../GLOSSARY.md).

## Definitions

| Term          | Meaning                                                                       |
| ------------- | ----------------------------------------------------------------------------- |
| **Selection** | The current chosen Member per Group. `null` means no Selection.               |
| **Decision**  | A Selection change, old → new. Starts the Apply pipeline.                     |
| **Apply**     | Route write, State snapshot, Hook dispatch, and metric update, in that order. |

A Group always has exactly one Selection, which may be `null`. The selector emits at most one Decision per recompute.

## When a Decision fires

| Event                                 | Trigger                                               | Reason label                          |
| ------------------------------------- | ----------------------------------------------------- | ------------------------------------- |
| `ProbeResult` flips a Family's Health | `wan.healthy` aggregate changes                       | `health`                              |
| `LinkEvent` flips `carrierUp()`       | rtnl carrier or operstate change                      | `carrier`                             |
| Daemon startup                        | Initial Selection from cold-start carrier-only Health | `carrier`, from the startup snapshots |

An event recomputes every Group containing the affected WAN. Each `decision.Group` owns its Selection independently, so one Group's pending Decision never blocks another's commit.

## Cold-start invariant

Until a (WAN, Family) delivers its first `ProbeResult` with a full Window (`WindowFilled`), that Family is uncooked and votes healthy in `combineFamilies`. Combined with `carrierUp()`, a healthy boot runs this chain in under a second, before any Window fills:

1. The daemon boots and installs rules; no Probes have completed.
2. The startup snapshots report each WAN's Gateways and `carrier=up, operstate=up` on the primary.
3. `Wans.MemberHealth` marks the primary healthy.
4. `selector.Select` picks the primary.
5. `apply.WriteDefault` writes the default route.
6. State and Hooks fire.

The daemon folds both snapshots in before it handles any event: the Gateways first, then every link as one batch. Each Group therefore recomputes once, makes at most one startup Decision, onto its preferred healthy Member whatever order the snapshot lists the WANs in, and writes every Family whose Gateway is known. The daemon reports `READY=1` to systemd only after that, so a unit ordered after `wanwatch.service` finds the startup Decision's Apply attempted and its State published; its Hooks may still be running. READY follows as well when no Member is healthy, so no Decision fires, and when a route write fails, so the Decision stays pending.

Results that arrive before the Window fills update metrics and retry pending Applies, but leave Health alone, so a Lost first Sample cannot seed a spurious down→up Decision pair. The first full Window seeds Hysteresis directly from its threshold verdict, bypassing the consecutive-cycle ramp. That verdict uses the failure thresholds (`lossPctDown`, `rttMsDown`), the band of the healthy vote the Family cast before its Window filled, so a WAN Selected on carrier goes down at the seed only when it would also go down while up; a Window between the two bands keeps it up. If that verdict is unhealthy, the WAN flips down at once; later Windows go through the normal `consecutiveUp` and `consecutiveDown` ramp.

## Failover

```text
t=0      primary healthy, backup healthy
         Selection = primary
         table 1000 v4 default = via primary's gw, dev wan0
         table 1000 v6 default = via primary's gw, dev wan0

t=1      carrier drops on wan0
         rtnl emits LinkEvent{Name: wan0, Carrier: down}
         health.Wans.Link flips primary's carrierUp() = false
         Wans.MemberHealth: primary unhealthy, backup healthy
         selector.Select: Selection = backup

         apply.WriteDefault(family=v4, table=1000, gw=backup.v4, ifindex=wan1)
         apply.WriteDefault(family=v6, table=1000, gw=backup.v6, ifindex=wan1)
         state.Writer.Write: selection=backup, selectionSince=t1
         state.HookNotifier.Notify queues the switch notification
         wanwatch_group_decisions_total{reason=carrier}++

t=2      table 1000 reflects backup; marked traffic routes via wan1
```

The `t=1` steps run sequentially on one goroutine. Apply never races with itself, and `state.json` readers always see a complete snapshot.

## Recovery

```text
t=10     carrier returns on wan0
         rtnl emits LinkEvent{Name: wan0, Carrier: up}
         health.Wans.Link flips primary's carrierUp() = true
         Wans.MemberHealth: primary healthy again (cold-start path:
                            primary.healthy was true at boot and never
                            went false from probes)
         selector.Select: Selection = primary (lower priority among healthy)
         Decision fires; routes rewritten; hooks run.
```

After a Probe-driven failure (rather than a carrier loss), the WAN stays unhealthy until `consecutiveUp` consecutive Window verdicts pass the up thresholds. Old losses must first leave the sliding Window: with the defaults, a fully lost Window needs 9 successful cycles to reach `lossPctUp = 10`, and the ramp completes 4 cycles later, about 13 × 500 ms = 6.5 s with prompt replies. Once the Window already passes, the ramp alone takes `intervalMs × consecutiveUp` (2.5 s). Cycles that wait for `timeoutMs` stretch both figures.

## Single-active invariant

v1 selects exactly one Member per Group, or none. A Group's Selection is never more than one Member, and Apply never installs multipath routes.

Multi-active arrives with the v2 `load-balance` Strategy. Apply's `RouteReplace` path will then need multipath nexthops, and `wanwatch_group_selected{group,wan}` will report several `1`s per Group.

## Determinism

Replaying the same event sequence (config, ProbeResults, LinkEvents) always produces the same Decision sequence. Both stateful elements are deterministic, and unit tests assert this across many invocations:

- `WindowStats`, given the Sample order.
- Hysteresis (`internal/health`), given the order of observed booleans.

## Failure modes

| Failure mode                                       | Daemon behavior                                                                                                                                                                                                                                                                              |
| -------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `apply.WriteDefault` errors                        | The Decision stays pending; the committed Selection and Hooks stay unchanged. Probe cycles or route observations on the pending WAN retry Apply. `wanwatch_apply_route_errors_total{group,family}` increments.                                                                               |
| A route refresh errors                             | The Selection, State, and Hooks stay unchanged, and a Repair is due. Probe cycles or route observations on the selected WAN rewrite every Family until a write succeeds. `wanwatch_apply_route_errors_total{group,family}` increments.                                                       |
| The kernel flushes a Group-table route             | The next main-table default-route observation on the selected WAN's interface rewrites it, even with an unchanged Gateway. Without such an observation, the route stays missing until the next Decision; see [ADR 0017](../adr/0017-repair-committed-group-table.md).                        |
| A netlink dump is interrupted                      | The table changed mid-dump. The subscriber discards the partial result and dumps again, up to four attempts in all; a dump interrupted on every attempt stops the daemon so that systemd restarts it.                                                                                        |
| `state.Writer.Write` errors                        | Logged and counted; the previous `state.json` stays in place (atomic write).                                                                                                                                                                                                                 |
| The metrics server fails                           | Logged, and retried after a delay that doubles from 1 s to 1 min and resets after a run of at least 1 min. Failover keeps running, because it doesn't need metrics; a scrape of the dead endpoint fails meanwhile. Unlike a prober or subscriber, the metrics server never stops the daemon. |
| A prober or subscriber exits while the daemon runs | Logged; the daemon exits non-zero so that systemd restarts it, whether the subsystem returned an error, `nil`, or a `context.Canceled` of its own. A subsystem that stopped silently would leave failover running on frozen Health or Gateways.                                              |
| The daemon is stopped during a handler             | The handler finishes the kernel call in progress, then applies nothing more: no further route write, Decision, `state.json` write, or Hook. The next start rebuilds State from the startup snapshots and Probes.                                                                             |
| A Hook times out (5 s default)                     | Logged and counted; the remaining Hooks in the `.d` directory still run.                                                                                                                                                                                                                     |
| ProbeResult arrives during a Decision              | Buffered on its channel; processed after the current Decision completes.                                                                                                                                                                                                                     |
| LinkEvent arrives during a Decision                | Same: one goroutine drains every channel, each in order. Events on different channels have no order between them.                                                                                                                                                                            |
| All Members unhealthy                              | A transition to `selection = null` commits without route writes and emits a `down` Hook if the Group had a Selection. Routes stay in place; repeated all-down observations emit no Decision.                                                                                                 |

Keeping stale routes when every Member is unhealthy is intentional: removing the last default route would leave no egress at all. Because those routes stay, a `down` flushes no conntrack; the next `up` flushes the vacated WAN's entries when it lands on another WAN ([What happens on a Decision](../wan-monitoring.md#what-happens-on-a-decision)). Operators who want a different policy can add a `down` Hook that runs `ip route flush table <T>`.

## Out of scope for v1

- Runtime per-Group override (`wanwatchctl set`).
- Notifications for unhealthy WANs that are not active. `state.json` and `wanwatch_wan_healthy` publish per-WAN Health, but no Decision fires until the Selection changes.
- Backoff for Probe failures while carrier stays up. Hysteresis covers recovery, and probing continues at `intervalMs` indefinitely.
