# TODO

Deferred work — items intentionally out of scope for the current release,
plus accumulated cleanups. Closed items move to [`CHANGELOG.md`](./CHANGELOG.md);
design changes large enough to warrant authoritative discussion land in
[`PLAN.md`](./PLAN.md) first.

Status legend:

- **v0.2** — small additive items, no API break needed.
- **v2** — design changes large enough to warrant a major bump.
- **infra** — CI / tooling debt; non-functional.
- **cleanup** — internal refactor; no user-visible effect.

---

## v0.2 — next minor release

### `wanwatchctl` status CLI

Small CLI for status queries: `wanwatchctl status`, `wanwatchctl group <name>`.
Reads `state.json`; no privileged ops. PLAN §12 OQ #8.

### Configurable hook timeout

Currently hard-coded to 5 s in `state.DefaultHookTimeout`. Expose as
`services.wanwatch.global.hookTimeout`. PLAN §12 OQ #5;
`daemon/internal/state/hooks.go:61`.

### Per-family probe targets

Today the same `probe.targets` list feeds both families; the daemon
splits by IP literal at runtime. Some operators want disjoint target
lists per family (e.g. only ping their ISP's v4 anycast, not its
flaky v6 endpoint). Add `probe.targetsV4` / `probe.targetsV6` as
optional overrides of the merged list. `daemon/cmd/wanwatchd/daemon.go:101`.

### Conntrack flush on Decision

`apply.FlushBySource` is implemented + tested but not wired into the
per-Decision path. Wire it: on switch, flush entries on the old
active's interface so existing flows fail over instead of being
black-holed by stateful NAT. `docs/wan-monitoring.md:121`,
`daemon/internal/apply/conntrack.go`.

### Stale-route policy on family-set shrink

When a Decision moves to a WAN that serves fewer families than the
previous active (e.g. dual-stack → v4-only), the group's table
retains the old v6 route untouched. Decide: clear vs retain.
PLAN §6.1 (line 641); add a `services.wanwatch.global.staleRoutes`
enum once policy lands.

### Low-latency state subscription

State readers polling `state.json` at >1 Hz miss short-lived
states. Add a Unix-socket event stream consumers can subscribe to
for push notifications. Keep `state.json` as the snapshot of
record. PLAN §12 OQ #6.

### state.json schema-evolution discipline

Write `docs/specs/state-evolution.md` codifying the version-bump
rules (now applied informally — see `daemon-state.md`'s revised
"Compatibility policy" section). Will need refining once schema 3
lands. PLAN §12 OQ #1.

---

## v2 — major

### Per-(group, family) Selection

v1 produces one Selection per group; both families apply to the
same active WAN. v2 splits — v4 can route via primary while v6
routes via backup. State-space doubles per group with two families;
Decision metric labels gain a `family` dimension.
PLAN §12 OQ #4.

### `load-balance` strategy + multipath nexthops

Multi-active routing across healthy members. Requires
`apply.WriteDefault` to emit MultiPath nexthops and the metrics
catalog to allow multiple `1`s for `wanwatch_group_active{group,wan}`.
The `weight` field on Member is reserved for this.
`docs/selector.md:18,73`, `docs/specs/failover.md:85`,
`daemon/internal/selector/primarybackup.go:10`.

### Multi-state Health: degraded / unknown

v1 collapses Health to a boolean (`healthy` / `unhealthy`); the
glossary used to list `up`/`down`/`degraded`/`unknown` but the
code never modelled the latter two. The shape that would justify
the churn:

- `degraded` — loss-ratio between the up/down thresholds, or RTT
  between RttMs{Up,Down}. Currently the band-pass holds the
  previous verdict; a tri-state would expose "in-band" to consumers.
- `unknown` — `cooked == false` (no probe sample yet) and carrier
  unknown. Today this collapses into the cold-start "healthy".

Would change `state.FamilyHealth.Healthy bool` → an enum, the
state.json schema (bump on release), `wanwatch_wan_*_healthy`
metric gauges (label-encode the enum instead), and selector
inputs.

### MTU / link-speed-aware selection

Prefer the higher-bandwidth WAN even at slightly higher latency.
Out of scope for v1's pure-health-based selection.
PLAN §12 OQ #9.

### SIGHUP hot-reload

Currently restart-only. Hot-reload adds complexity: re-allocating
marks/tables would force kernel-state reconciliation. PLAN §12 OQ
#7 marks it deliberately deferred. Lives in v2 because the right
shape changes Selection / Apply semantics, not because the
implementation is mechanical.

---

## infra — CI / tooling

### Vulnerability scan workflow

`CLAUDE.md` line 184 promises `.github/workflows/audit.yml` running
`govulncheck` + `vulnix` weekly + per-release. File doesn't exist.
Either build it or soften the CLAUDE.md claim.

### golangci-lint config

`CLAUDE.md` line 175 references `.golangci.yml` with a curated
check set (`errcheck`, `gosec`, `revive`, …). File doesn't exist;
`golangci-lint run` in the devshell uses defaults. Either commit
the curated config or soften the claim.

### Coverage gates

`CLAUDE.md` says "Coverage gates per PLAN §9.2 — CI fails on
regression." No coverage-tracking infrastructure exists yet. The
new CI workflow runs tests but doesn't enforce a coverage floor.

---

## cleanup — internal refactors

Captured during reviews; deemed not worth blocking the original
work, but flagged for later.

### Per-family reapply on RouteEvent

`handleRouteEvent` rewrites both families of the active WAN on any
single-family route change. `RouteReplace` is idempotent so the
extra netlink syscall is harmless, but tracking per-family dirty
state would halve the syscalls under flap.
`daemon/cmd/wanwatchd/daemon.go:291` (applyRoutes loop),
`daemon.go:343` (reapply driver).

### Split `rtnl` into `rtnl/link` + `rtnl/route`

The two subscriber types share zero symbols. Splitting the package
would let the test helpers (`mkUpdate`, `mkSub`) keep clean names
without the `Route` prefix the route-side tests currently carry
to dodge collision. `daemon/internal/rtnl/route_test.go`.
