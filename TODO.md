# TODO

Deferred work — items intentionally out of scope for the current release,
plus accumulated cleanups. Closed items move to [`CHANGELOG.md`](./CHANGELOG.md);
design changes large enough to warrant authoritative discussion land in
an ADR under [`docs/adr/`](./docs/adr/) first.

Status legend:

- **v0.2** — small additive items, no API break needed.
- **v2** — design changes large enough to warrant a major bump.
- **infra** — CI / tooling debt; non-functional.
- **cleanup** — internal refactor; no user-visible effect.
- **considered** — evaluated and not currently planned; revisit only if conditions change.

---

## v0.2 — next minor release

### `wanwatchctl` status CLI

Small CLI for status queries: `wanwatchctl status`, `wanwatchctl group <name>`.
Reads `state.json`; no privileged ops.

### Build `wanwatch.types.{fwmark,routingTableId}` on nix-libnet

Fwmarks and routing-table IDs are kernel networking primitives that
nftzones (currently validating `meta mark set N` as a raw int),
systemd-networkd modules, and hand-rolled rule configs could share, so
nix-libnet may gain generic types for them. A generic type would likely
accept the full 32-bit range, while `[1000, 32767]` is wanwatch's own
policy. Widening wanwatch's types would also change what the
`services.wanwatch` mark and table options accept, which is a module
decision, and [ADR-0018](./docs/adr/0018-0x-stability-promise.md) lets a
stable type grow but never shrink.

So wanwatch's types keep wanwatch's range. If libnet gains these types,
`lib/types/primitives.nix` builds on them only as a restriction of the
libnet type to `[1000, 32767]`, and `wanwatch.types.fwmark` and
`wanwatch.types.routingTableId` keep accepting the same values.

### Declarative Hooks option

Add `services.wanwatch.hooks.<event>.<name>` (a path or package)
that the module links to `/etc/wanwatch/hooks/<event>.d/<name>`,
replacing hand-written `environment.etc` entries. The Hook tree
stays at its fixed path ([ADR-0014](./docs/adr/0014-module-owns-identity-and-paths.md)).

### Pre-apply hooks

Add a `<hooksDir>/pre-{up,down,switch}.d/*` tier that fires _before_
the route apply, complementing the existing hooks (which fire after
the apply converges). Useful for "about to switch" notifications or
staging work.

Pre-hooks stay notification-only: a pre-hook that _gates_ or _blocks_
the apply would make hooks part of the apply transaction, which
[ADR-0009](./docs/adr/0009-publish-after-apply-converges.md) rules
out — promoting them would need a new ADR first. `daemon/cmd/wanwatchd/daemon.go` (notifyHooks / publishDecision),
docs/specs/daemon-state.md env-var contract.

### Stale-route policy on family-set shrink

When a Decision moves to a WAN that serves fewer families than the
previous active (e.g. dual-stack → v4-only), the group's table
retains the old v6 route untouched. Decide: clear vs retain.
docs/nftzones-integration.md; add a `services.wanwatch.global.staleRoutes`
enum once policy lands.

### Remove stale fwmark rules

`EnsureRule` removes copies of a Group's current fwmark rule, but a
rule for a removed Group, or for a Group's old mark or table, stays
until reboot. The daemon cannot tell its own rules from the user's or
another daemon's: it stamps them `protocol static`, which isn't unique
to wanwatch. Removing them safely needs an ownership marker, such as
a dedicated rule `protocol` value, and that marker is a
firewall-composition contract decision (docs/nftzones-integration.md).

### Conntrack flush across Groups that share a WAN

Conntrack flushing runs per Group, but source addresses belong to
WANs. When two Groups share a WAN and only one switches off it, the
flush also resets the other Group's flows, which still leave through
that WAN. They reconnect at once, so this is disruptive but
recoverable.

Skipping the flush while another Group still selects the WAN is no
fix: the switched Group's SNATted flows would keep the old source
address and be black-holed until their entries time out. Group marks
are packet marks (`meta mark set`), so a conntrack entry does not
record its Group. A fix needs the firewall to copy the mark into the
connection (`ct mark set meta mark`) so the flush can match address
and mark, which changes the firewall composition contract. Worth it
only once a deployment runs several Groups on one WAN.
`daemon/cmd/wanwatchd/daemon.go` (`flushSwitchedConntrack`),
`daemon/internal/apply/conntrack.go`,
[`docs/nftzones-integration.md`](./docs/nftzones-integration.md).

### Kernel-side conntrack flush for IPv6

`apply.FlushBySource` asks the kernel to delete the matching IPv4
entries in one request, but always dumps the table for IPv6: every
released kernel up to at least 7.3 inverts the IPv6 address match in
`ctnetlink_filter_match_tuple`, so a filtered delete would remove every
other IPv6 entry. Upstream fixed it in `cd37ab22a74e`, marked for
stable. Once the kernels nixpkgs ships carry the fix, send the filtered
delete for IPv6 too. A kernel version cannot tell a fixed stable kernel
from an unfixed one, so this needs a safe runtime probe or a minimum
kernel. Without NAT66 an IPv6 flush matches only the router's own
flows, so the dump costs little more than reading the table.
`daemon/internal/apply/conntrack.go`.

### Low-latency state subscription

State readers polling `state.json` at >1 Hz miss short-lived
states. Add a Unix-socket event stream consumers can subscribe to
for push notifications. Keep `state.json` as the snapshot of
record.

### Gauge for a due Group-table repair

A failed route refresh leaves a Group due for a Repair
([ADR-0017](./docs/adr/0017-repair-committed-group-table.md)), which
only `wanwatch_apply_route_errors_total` and the logs reveal. A gauge
such as `wanwatch_group_routes_converged{group}` would let an alert
fire while the Group table lags its Selection, not only when a write
fails. Adding it changes the metrics catalog in
[`docs/metrics.md`](./docs/metrics.md).
`daemon/internal/decision/group.go` (`repairDue`).

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

### Resync from a fresh dump on netlink `ENOBUFS`

A link or route subscription whose receive buffer overflows reports
`ENOBUFS`, which is fatal: the daemon exits and systemd restarts it.
The unit's start limit (5 starts in 300 s) means a sustained flap
storm can leave it `failed`, with no failover at all. The forced
1 MiB buffer makes overflow rarer but not impossible. Instead of
exiting, the subscriber could reopen its subscription and reconcile
from a fresh dump, as it already does at startup.
`daemon/internal/rtnl/subscriber.go`, `daemon/internal/rtnl/route.go`.

### Cheaper Gateway reconcile on a route notification

The route subscriber re-reads the whole main table for the Family on
every default-route notification (`reconcileVia`). That is cheap on a
typical router but costly on a host that carries a full BGP table,
where each notification dumps hundreds of thousands of routes. A
filtered dump of default routes only, or a narrower reconcile, would
bound the cost.
`daemon/internal/rtnl/route.go`.

### Reject non-unicast and special-purpose Targets

Both layers accept any unzoned IP literal of the bucket's Family,
apart from an IPv4-mapped address in `v6` and duplicates. An
unspecified (`0.0.0.0`, `::`), multicast, loopback, broadcast, or
link-local Target doesn't crash the daemon, but it gives constant
loss or a reply that says nothing about the WAN. Reject these classes
in `probe.tryMake` and in `config.Validate`, with a new error kind.
This tightens the `targets` options, so it needs a `CHANGELOG.md`
migration note and a minor bump (ADR-0018).

---

## v2 — major

### Per-(group, family) Selection

v1 produces one Selection per group; both families apply to the
same active WAN. v2 splits — v4 can route via primary while v6
routes via backup. State-space doubles per group with two families;
Decision metric labels gain a `family` dimension.
See [ADR-0002](./docs/adr/0002-one-selection-per-group.md).

### `load-balance` strategy + multipath nexthops

Multi-active routing across healthy members. Requires
`apply.WriteDefault` to emit MultiPath nexthops and the metrics
catalog to allow multiple `1`s for `wanwatch_group_selected{group,wan}`.
Member gains the `weight` it needs then; v1 has none.
`docs/selector.md`, `docs/specs/failover.md` (single-active invariant),
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
`wanwatch_wan_*_healthy` metric gauges (label-encode the enum
instead), and selector inputs. The State and metric changes are
incompatible, so they need an ADR superseding
[ADR-0018](./docs/adr/0018-0x-stability-promise.md) and a state.json
schema bump.

### MTU / link-speed-aware selection

Prefer the higher-bandwidth WAN even at slightly higher latency.
Out of scope for v1's pure-health-based selection.

### SIGHUP hot-reload

Currently restart-only. Hot-reload adds complexity: re-allocating
marks/tables would force kernel-state reconciliation.
[ADR-0006](./docs/adr/0006-restart-only-reconfiguration.md) defers it. Lives in v2 because the right
shape changes Selection / Apply semantics, not because the
implementation is mechanical.

---

## infra — CI / tooling debt

### Restore the vulnerability audit

ADR-0012 removed `.github/workflows/audit.yml`, which ran
`govulncheck` against the daemon binary and `vulnix` against its
runtime derivers weekly and on release tags. Its `audit`
development shell took both scanners from the removed
`nixpkgs-unstable` input. Restoring the audit needs a new scanner
source: either stable `pkgs`, or the nixos-unstable override
applied to the audit shell but not to the daemon build. The
workflow, the `dev/audit-shell.nix` shell, the fail-closed
`.github/scripts/retry-vulnix.sh` wrapper, and its
`tests/ci/vulnix-retry.sh` contract test are recoverable from
commit a6612d7; restore the contract test beside its wrapper, as
`tooling/review-env-test.sh` sits beside `tooling/review-env.sh`. Until then, `AGENTS.md` lists the vulnerability
audit as paused.

---

## cleanup — internal refactors

Captured during reviews; deemed not worth blocking the original
work, but flagged for later.

### Split `rtnl` into `rtnl/link` + `rtnl/route`

The two subscriber types share zero symbols. Splitting the package
would let the test helpers (`mkUpdate`, `mkSub`) keep clean names
without the `Route` prefix the route-side tests currently carry
to dodge collision. `daemon/internal/rtnl/route_test.go`.

### `gateway-discovery-v6` flake: networkd-install vs Prime race

The v6 gateway-discovery scenario can flake on slower CI runners.
Pattern: systemd-networkd installs the v6 default route some
milliseconds _after_ wanwatchd's `rtnl.RouteSubscriber.Prime`
returns (`primed=0` in the daemon log), then the subscription's
`Run` is supposed to catch the `RTM_NEWROUTE` — and it _usually_
does, but on at least one observed CI run it didn't, and
`state.json`'s `wans.uplink.gateways.v6` never surfaced
`fd00:1::1`.

Root cause analysis is incomplete. Three plausible mechanisms:

1. **Prime/Run gap.** Between `RouteList(family=v6)` returning and
   `RouteSubscribeWithOptions` actually wiring the netlink
   subscription, an RTM_NEWROUTE could fire and be missed. The
   netlink library's `Subscribe` should be synchronous w.r.t. its
   bind, but a v6-only race here would explain the symptom.
2. **netlink decoder.** The library decodes the v6 default
   route's `Gw` as `nil` with unstable nixpkgs' newer kernel,
   so `gatewayCache.address(...)` returns nil and the test
   predicate (`== "fd00:1::1"`) never matches. Newer kernels'
   RTA_VIA / RTA_MULTIPATH encoding for v6 defaults is a plausible
   trigger.
3. **Subscription doesn't cover RTNLGRP_IPV6_ROUTE.** The library
   defaults seem to cover both v4 and v6 multicast groups but I
   haven't read the code to confirm. A regression in the library
   pinning could narrow that silently.

Diagnostic is already in place: the shared `tests/vm/observation.py`
waits used by `gateway-discovery-v6.nix` dump `state.json`, live
metrics, both families' kernel routes, and the last 50 lines of the
daemon journal on timeout. Next time it flakes, that evidence distinguishes:

- null `state.json` v6 gateway + kernel route present + no
  daemon "route event" log → option 1 or 3 (subscription gap)
- `gateways.v6 == null` in state.json + daemon log shows
  the route event with `gw=<nil>` → option 2 (decoder)

Fix candidates depending on which leg:

- Option 1: after `Prime` returns, immediately do a second
  `RouteList` and emit any routes that weren't in the first dump
  — closes the gap at the cost of a second syscall.
- Option 2: replace the netlink library's Gw decode with a
  manual RTA_GATEWAY parse for v6, or update the library pin.
- Option 3: explicitly set `Groups = RTNLGRP_IPV4_ROUTE |
RTNLGRP_IPV6_ROUTE` in `RouteSubscribeOptions` rather than
  relying on the default.

---

## considered — not currently planned

### Unprivileged-ICMP path

The v1 design originally promised `SOCK_DGRAM+IPPROTO_ICMP` as the
preferred probe socket, with `CAP_NET_RAW` dropped at runtime —
the daemon (`daemon/internal/probe/pinger.go:209`) ships
raw-socket only.

**What it would change.** Two Linux paths exist for sending ICMP
echo from userspace:

- `SOCK_RAW + IPPROTO_ICMP` — current. App sees every ICMP packet
  the kernel receives, builds the full ICMP message itself,
  requires `CAP_NET_RAW`.
- `SOCK_DGRAM + IPPROTO_ICMP` — alternative. Kernel demuxes echo
  replies by identifier, no capability required as long as the
  process's GID is covered by `net.ipv4.ping_group_range`. Linux
  3.0+ for v4, 3.11+ for v6.

**Why probably not.** The security delta is bounded:

- `CAP_NET_ADMIN` is required either way — route mutation in
  `apply.WriteDefault`, rule install in `apply.EnsureRule`,
  conntrack flush in `apply.FlushBySource`. The daemon stays
  privileged regardless.
- `SO_BINDTODEVICE` (used per WAN to pin each probe's egress)
  required `CAP_NET_RAW` itself pre-Linux 5.7; the 5.7 relaxation
  has conditions that may or may not cover the daemon's usage.
- Real cost: a `dialUnprivileged` codepath with raw-socket
  fallback, module-side
  `boot.kernel.sysctl."net.ipv4.ping_group_range"`, and runtime
  `prctl(PR_CAPBSET_DROP, CAP_NET_RAW)` to shed the cap.

**Revisit conditions.** Worth implementing if all three hold:
(a) the daemon is deployed somewhere a compromised-with-
`CAP_NET_RAW` process is a named threat, (b) all target kernels
ship ≥ 5.7, (c) `SO_BINDTODEVICE` is confirmed working without
the cap in the daemon's specific bind pattern.
