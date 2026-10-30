# Repair the Group table of a committed Selection

Apply also converges the Group table of a committed Selection, not only of a pending Decision. Every main-table default-route observation on the selected WAN's interface rewrites that Family's Group-table route, whether or not the Gateway changed. A failed rewrite leaves the Group due for a Repair: the selected WAN's next Probe cycle or route observation rewrites every Family, and each one retries until a write succeeds.

[ADR-0009](./0009-publish-after-apply-converges.md) retries only pending Decisions, which left two gaps:

- A failed Gateway refresh left the Group table on the old Gateway until the next Gateway change or Decision.
- The kernel flushes the IPv4 routes through a device that goes down, and sends no `RTM_DELROUTE` for them. After a quick down and up, networkd or DHCP re-adds the main-table default with the same Gateway, so the Gateway cache saw no change, nothing rewrote the Group-table route, and marked traffic fell through to the main table.

Reading the Group table back on every Probe cycle would not depend on route notifications, but it costs a route dump per cycle and Group. `RouteReplace` is idempotent, so rewriting on each observation is cheap. Link observations do not trigger a rewrite: the link subscriber coalesces a down and up that it reads as unchanged, and the re-added main-table default already follows.

## Consequences

A Repair is not a Decision. It changes no Selection, publishes no State, runs no Hook, flushes no conntrack, and keeps `selectionSince`. A pending Decision holds a due Repair until it commits, and any commit clears it. Only `wanwatch_apply_route_errors_total` and the logs show a due Repair.

A Group-table route flushed with no main-table observation after it stays missing until the next Decision. Without a main-table default route, the WAN has no Gateway to write anyway.
