# Glossary

Authoritative terminology for `nixos-wanwatch`. Terms have
non-overlapping meanings — reusing them loosely is a defect.

Code, comments, commit messages, error kinds, metric names, hook env
vars, and docs all use these terms exactly as defined here. Adding a
new term means updating this file in the same commit.

| Term | Definition | Not to be confused with |
|---|---|---|
| **WAN** | An egress interface plus a Probe configuration. Serves one or two IP families depending on `probe.targets`; next-hops are discovered at runtime via netlink. The atomic monitored unit. | Group, Member |
| **Probe** | Configuration of *how* a WAN is tested — targets, method, interval, thresholds, hysteresis. | Sample |
| **Target** | A single IP being probed. A Probe has one or more Targets. | Probe |
| **Sample** | One probe attempt + result (RTT in microseconds, or `loss`). | Probe |
| **Window** | Sliding collection of recent Samples used to compute Health metrics. | Hysteresis |
| **Health** | Derived status of a WAN: `healthy` / `unhealthy` (boolean in v1; see TODO for v2 multi-state). | Selection |
| **Hysteresis** | State machine suppressing flapping — consecutive cycles required to flip Health in either direction. | Window |
| **Group** | Ordered collection of Members + Strategy + Table + Mark. | WAN, Selection |
| **Member** | A WAN's *membership* in a Group, carrying per-Group attributes (weight, priority). | WAN |
| **Strategy** | Algorithm choosing active Member(s) from healthy ones. v1: `primary-backup`. | Selection |
| **Selection** | Current chosen Member(s) per Group. Output of Strategy applied to Member Health. | Decision |
| **Decision** | A Selection *change* event — old Selection → new Selection. Triggers Apply. | Selection |
| **Gateway** | Daemon-mirrored default-route next-hop for a (WAN, family). Discovered via rtnetlink from the kernel's main routing table; empty when no default exists on the WAN's interface or when the WAN is `pointToPoint`. Consumed by Apply when writing per-Group default routes. | Apply |
| **Apply** | The act of mutating kernel state (route, conntrack) to reflect a Decision. | Decision |
| **State** | Externalized view: per-WAN Health, per-Group Selection. Atomic JSON file. | Selection (sub-component) |
| **Hook** | User script invoked on Decision with structured env vars. | Apply |

## Relationships

- A **WAN** belongs to zero or more **Groups**. Each membership is a
  **Member**.
- A **Group** has exactly one **Strategy** and produces exactly one
  **Selection** at any given time (v1: single-active).
- A **Probe** is a property of a **WAN**, not of a (WAN, Group) pair —
  probing state is shared across every Group a WAN is in.
- **Hysteresis** is also per-**WAN**. A WAN flipping unhealthy goes
  unhealthy in every Group containing it simultaneously.
- A **Decision** is a transition between two **Selections** — the
  daemon emits one Decision per Group whose Selection changed.
- **Apply** runs once per Decision; **Hooks** run after Apply, with
  the Decision's data in env vars.
- A **Gateway** is per-(WAN, family) and tracked independently of
  Health: a WAN can be Health-up with no Gateway yet (kernel hasn't
  installed a default route) — Apply will skip that family's route
  write and reapply when the Gateway is discovered.

## Family

A WAN is a "family" in the IP-stack sense:

- `v4` — at least one of the WAN's `probe.targets` is an IPv4
  literal; its v4 default route lives in the group's v4 routing
  table.
- `v6` — likewise for v6 literals in `probe.targets`.

The daemon discovers the per-family default-route next-hop from
the kernel's main routing table at runtime — there is no separate
operator-typed gateway declaration. For interfaces with no
broadcast next-hop (PPP, WireGuard, GRE, tun) set
`pointToPoint = true` so the daemon installs a `scope link`
default route instead.

Per-family Health is combined into per-WAN Health under the WAN's
`probe.familyHealthPolicy` (`all` / `any`).
