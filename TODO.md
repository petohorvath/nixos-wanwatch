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

### Pre-apply hooks

Add a `<hooksDir>/pre-{up,down,switch}.d/*` tier that fires *before*
the route apply, complementing the existing hooks (which fire after
the apply converges). Useful for "about to switch" notifications or
staging work.

Pre-hooks stay notification-only: a pre-hook that *gates* or *blocks*
the apply would make hooks part of the apply transaction, which PLAN
§5.5 explicitly rules out — promoting them would need a PLAN change
first. `daemon/cmd/wanwatchd/daemon.go` (runHooks / commitDecision),
PLAN §5.5 env-var contract.

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

### Re-add `wanwatch_probe_samples_total`

Cut from the v1 catalog — it was registered but never fed, because
the data model can't supply it. The catalog shape is
`{wan,target,family,result}` with `result ∈ {success,timeout,error}`,
but the daemon's `Sample` (`daemon/internal/probe/stats.go`) carries a
single `Lost bool` that collapses timeout and transport error into one
bit, and the aggregated `ProbeResult` keeps no per-sample counts.
Re-adding needs a tri-state sample result plus a path threading
per-sample counts from the prober to the metrics layer.

### Re-add `wanwatch_apply_op_duration_seconds`

Cut from the v1 catalog — it was registered but never fed, because
one label value doesn't fit. The catalog shape is `{group,op}` with
`op ∈ {conntrack_flush,state_write,hook,rule_install}`, but
`state_write` is not per-group: `state.json` is one file written once
per Decision across all groups, so a `group` label on its duration is
a category error. Resolve before re-adding — drop `state_write` from
the op set, or split state-write timing into a separate group-less
metric.

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

## tracking — waiting on upstream

### Bump nixpkgs once Go stdlib `net` fix lands

`.github/workflows/audit.yml`'s `govulncheck` job stays red on
GO-2026-4971 — a Windows-only panic in `net.Dial` and
`net.LookupPort` when handling NUL bytes. govulncheck reports
`Found in: net@go1.25.9`, `Fixed in: net@go1.25.10` against our
nixos-25.11 pin (stable's current Go is 1.25.9); the `nixos-unstable`
channel ships Go 1.26.2 which is also flagged (fixed somewhere on the
1.26.x branch). The daemon is Linux-only so the call paths are
unreachable in practice, but govulncheck flags any reachable import
of the affected functions regardless of build target.

Resolution is a `nix flake update nixpkgs` once the channel we pin
ships the fixed point release (1.25.10+ on stable, or 1.26.3+ on
unstable). No code change on our side; verify by re-dispatching
the audit workflow and watching the govulncheck job go green.

---

## cleanup — internal refactors

Captured during reviews; deemed not worth blocking the original
work, but flagged for later.

### Split `rtnl` into `rtnl/link` + `rtnl/route`

The two subscriber types share zero symbols. Splitting the package
would let the test helpers (`mkUpdate`, `mkSub`) keep clean names
without the `Route` prefix the route-side tests currently carry
to dodge collision. `daemon/internal/rtnl/route_test.go`.
