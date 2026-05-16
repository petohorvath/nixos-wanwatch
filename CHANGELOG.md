# Changelog

All notable changes to `nixos-wanwatch` are recorded here. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); version numbers follow [SemVer](https://semver.org/spec/v2.0.0.html). Schema versions for the daemon's JSON contracts (`config.json`, `state.json`) are independent — bumped only on incompatible shape changes; see [`docs/specs/daemon-config.md`](./docs/specs/daemon-config.md) and [`docs/specs/daemon-state.md`](./docs/specs/daemon-state.md).

## [Unreleased]

### Changed

- **API break — `groups.<name>.mark` and `groups.<name>.table` are now required.** The hash + linear-probe auto-allocator (`lib/internal/{allocator,marks,tables}.nix`) was removed; users must declare both integers explicitly per group. Both fields are typed as `wanwatch.types.fwmark` / `wanwatch.types.routingTableId` — `lib.types.ints.between 1000 32767`, so the kernel-reserved tables (`253`/`254`/`255`) and small-integer values commonly used by ad-hoc scripts are excluded by construction. The NixOS module asserts no duplicate marks or tables across groups at eval time. `services.wanwatch.marks.<group>` and `.tables.<group>` outputs remain (read-only echoes of the user's input) so downstream consumers (nftzones, hand-rolled nftables) keep referencing them by name. Migration: per group, add `mark = N; table = N;` with any value in `1000..32767`; collisions surface as eval errors and can be resolved by picking different integers. Rationale: the auto-allocator's "function of the full group-name set" semantic meant adding a new group could silently shift another group's mark, surprising operators downstream; explicit declaration eliminates this and removes ~200 LOC + tests.
- **API break — `wans.<name>.gateways` removed.** Replaced by a single `pointToPoint` bool (default `false`). For broadcast links the daemon now discovers the default-route next-hop dynamically via netlink (`RTNLGRP_IPV4_ROUTE` + `RTNLGRP_IPV6_ROUTE`) from the kernel's main routing table. For point-to-point links (PPP, WireGuard, GRE, tun) set `pointToPoint = true` and the daemon installs `scope link` default routes — no gateway needed.
- A WAN's served families are now derived solely from `probe.targets` (v4 literals ⇒ serves v4; v6 literals ⇒ serves v6). No separate gateway declaration to keep in sync; family-coupling validation is therefore retired.
- New per-WAN `gateways: { v4, v6 }` block in `state.json` reflecting the discovered next-hops.
- Hook env vars `WANWATCH_GATEWAY_V4/V6_OLD/NEW` now carry the discovered next-hop instead of the operator-typed value. Empty when the WAN is point-to-point or the cache has no entry yet.
- **`state.json` field renames** (no schema bump pre-release): `wans.<name>.families.<v4|v6>.rttMs` → `.rttSeconds`, `.jitterMs` → `.jitterSeconds`, `.lossPct` (0..100) → `.lossRatio` (0..1). Pulls the wire shape onto the same units the Prometheus gauges use.
- **Metric renames**: `wanwatch_probe_rtt_milliseconds` → `wanwatch_probe_rtt_seconds`, `wanwatch_probe_jitter_milliseconds` → `wanwatch_probe_jitter_seconds`. Prometheus convention is base units.
- `selector.Apply` (Go) renamed to `selector.Select` — `Apply` is reserved by the glossary for kernel-state mutation in `internal/apply`.
- `Gateway` is now a glossary term (was an unnamed runtime concept). Daemon-internal struct field abbreviations `GwV4Old` / `GwV4New` / `GwV6Old` / `GwV6New` on `state.HookContext` spelled out as `GatewayV4Old` / `GatewayV4New` / `GatewayV6Old` / `GatewayV6New` to match the env-var names they populate.
- **state.json is now republished on every observable state transition** (per-family Health flip, carrier/operstate change, Gateway cache mutation on a watched interface, route convergence) rather than only on Decisions. Consumers polling for a coherent runtime view stop seeing stale snapshots between Decisions.
- **Hysteresis now seeds from the first *full* probe Window** rather than from every Sample. A Lost first Sample no longer seeds the state unhealthy — eliminates the cold-start down→up flap PLAN §8 warned against, and a healthy WAN no longer fires a spurious Decision pair as probes converge.
- **state.json's `updatedAt` and the hook env var `WANWATCH_TS` now share one timestamp per Decision.** `state.Writer.Write` preserves a non-zero caller `UpdatedAt`; `daemon.commitDecision` captures `time.Now().UTC()` once and threads it into both writes. Consumers correlating the two surfaces see identical values instead of millisecond-drift twins.
- `wanwatch_group_decisions_total` and `wanwatch_apply_op_errors_total` help text + `docs/metrics.md` shrunk to the label values the daemon actually emits in v1. `reason ∈ {health, carrier}` (was claiming `startup`/`manual`); `op ∈ {conntrack_flush, ifindex_lookup, rule_install}` (was claiming `state_write`/`hook` too). Reserved values are now documented as reserved.

### Added

- `wanwatch.types.fwmark` and `wanwatch.types.routingTableId` (both `lib.types.ints.between 1000 32767`) — exposed under `lib/types/primitives.nix`. Planned to migrate into `nix-libnet` in a follow-up release.
- `daemon/internal/rtnl.RouteSubscriber` — emits per-`(iface, family)` default-route add/del events from the main RIB, filtered to WAN interfaces.
- `daemon/cmd/wanwatchd.gatewayCache` — mirrors the kernel's view; drives non-PtP route writes and re-applies on route flap.
- VM scenarios added since 0.1.0: `tests/vm/cold-start.nix`, `tests/vm/failover-probe-loss.nix` (v4 netem-driven), `tests/vm/failover-probe-loss-v6.nix` (v6 netem-driven; closes the v6 probe + threshold + hysteresis gap that the carrier-driven `failover-v6.nix` left unexercised), `tests/vm/gateway-discovery.nix`.
- `tests/integration/` split into `scenarios/` + `rejections/` per PLAN §9.3, with two rejection cases (`probe-no-targets`, `probe-family-mismatch`) proving the lib validators stay wired into the live module-eval path.
- `daemon/cmd/wanwatchd/daemon_test.go` — first unit coverage for the daemon's pipeline (`writeStateSnapshot`, `handleProbeResult`, `handleRouteEvent`). Lived as `state.go` before with no test file.
- `wanwatch_apply_op_errors_total{op="rule_install"}` is now incremented on bootstrap `EnsureRule` failure (was: silent from the metric's perspective).

### Internal

- VM tests hardened against intra-Decision write-ordering races: `gateway-discovery.nix` now polls `ip -4 route show default` (mirrors the v6 sibling's networkd-Gateway= guard); `failover-probe-loss-v6.nix` Phases A/D1/E poll the loss-ratio threshold via `wait_until_succeeds` on `state.json` (matches the v4 sibling and v6's own Phases B/D3) and the now-unused `loss_ratio` helper is deleted; `nftzones-integration.nix` polls `has_fwmark_rule` for both families (matches `smoke.nix`'s defensive bootstrap-order pattern); `family-health-policy.nix` polls the per-family Prometheus gauges instead of single-shot scraping after state.json convergence. Closes the "Audit VM tests for tight timing windows" item in TODO.
- Unified `probe.Family` and `apply.Family` onto a single enum (values now match `unix.AF_INET` / `AF_INET6` so netlink passthrough is a single cast). Old `state.Family` struct renamed to `state.FamilyHealth` to free up the name. `rtnl.RouteFamily` values aligned with the same encoding; `probeFamilyToRoute` shim deleted (a plain `rtnl.RouteFamily(probeFam)` cast replaces it).
- CI / tooling claims made good on: `.golangci.yml` now carries the curated check set referenced by `CLAUDE.md` §Lint (`bodyclose`, `errcheck`, `errorlint`, `goconst`, `gocritic`, `gosec`, `govet`, `prealloc`, `revive`, `staticcheck`, `unparam`, `unused`). `.github/workflows/audit.yml` runs `govulncheck` + `vulnix` weekly + on release tag. Per-package coverage gates from PLAN §9.2 land as `checks.<system>.coverage`; the CI's daemon job builds it.
- `selector.Selection.Active` is now a comparable `Active{Wan string, Has bool}` struct (was `*string`). Removes the `equalStringPtr` / `strPtr` helpers and the loop-local pointer trick in `primaryBackup`.
- `context.Context` propagated through `apply.WriteDefault` / `EnsureRule` / `FlushBySource` and the daemon's `bootstrap` / `handle*Event` / `recomputeGroup` / `applyRoutes` chain.
- `state.Writer` now serializes `Write` calls with a `sync.Mutex` (was "serialize at the caller" documentation).
- `state.Writer.Write` preserves a non-zero caller-supplied `UpdatedAt` (was: always overwritten with `time.Now().UTC()`).
- `daemon/cmd/wanwatchd.gatewayCache` and its `set` / `clear` / `get` / `string` methods are unexported — `package main` has no external consumers, so the capitalized names were promising a public surface that didn't exist.
- `probe.NewWindow` returns an error instead of panicking; `selector.NewHysteresisState(up, down)` takes thresholds at construction rather than per-call.
- `cmd/wanwatchd` file shuffle: the file holding the `daemon` struct is now `daemon.go` (was `state.go`), the subscriber wiring is `subscribers.go` (was `daemon.go`), free helpers extracted to `helpers.go`.
- `docs/glossary.md` row for `Health` now matches the v1 boolean shape; the four-state spec (`up`/`down`/`degraded`/`unknown`) deferred to v2 — see `TODO.md`.
- `state.SchemaVersion` pinned at 1 pre-release; the first tagged release freezes shape 1.

## [0.1.0] — 2026-05-12

Initial public release. Feature-complete per [`PLAN.md`](./PLAN.md) v1 scope.

### Added

**Pure-Nix library** (`lib/`)

- Value types `probe`, `wan`, `member`, `group` with the `make` / `tryMake` / `is<T>` / `eq` / `compare` / `toJSON` skeleton.
- IP / CIDR / interface-name validation via [`nix-libnet`](https://github.com/petohorvath/nix-libnet).
- Deterministic mark + table allocators (SHA-256 + linear probe) with cross-explicit collision detection.
- Pure-Nix selector mirroring the Go daemon's strategy registry.
- `wanwatch.config.render` / `.toJSON` — single source of truth for the daemon-config JSON shape.
- `wanwatch.types.<name>` flattened NixOS option types per value type.

**NixOS modules** (`modules/`)

- `nixosModules.default` (`services.wanwatch.*`) — option surface, JSON renderer, hardened systemd unit, dedicated `wanwatch:wanwatch` user.
- Read-only outputs `services.wanwatch.marks.<group>` and `.tables.<group>` for cross-module composition.
- `nixosModules.telegraf` (`services.wanwatch.telegraf.*`) — opt-in Prometheus scrape companion; joins the telegraf account to the wanwatch group for socket access.

**Go daemon** (`daemon/`, `pkgs/wanwatchd.nix`)

- `wanwatchd` single binary, `CGO_ENABLED=0`, vendored deps. Capabilities: `CAP_NET_ADMIN`, `CAP_NET_RAW`.
- Packages: `config`, `probe`, `rtnl`, `selector`, `apply`, `state`, `metrics`.
- ICMP / ICMPv6 probing with `SO_BINDTODEVICE` per WAN, stable per-(WAN, family) identifiers.
- rtnetlink subscriber with carrier + operstate dedup, `ListExisting=true` for cold-start.
- Selector with `primary-backup` strategy + two-stage hysteresis (band-pass thresholds + consecutive-cycle filter).
- Netlink apply: `RouteReplace` (idempotent default route), `RuleAdd` (idempotent fwmark rule, EEXIST-swallowed), `ConntrackDeleteFilter` (best-effort source-IP flush).
- Atomic `state.json` writer + run-parts hook dispatcher with 5 s per-hook timeout and the `WANWATCH_*` env-var contract.
- Prometheus registry over a 0660 Unix socket; 16 metrics per PLAN §7.2 (catalog drops `wanwatch_probe_samples_total` and `wanwatch_apply_op_duration_seconds` — see `TODO.md` for the re-add work).

**Tests**

- Unit tier (`tests/unit/`) — `lib.runTests` shape; per-value-type skeleton meta-test; Go table-driven tests with package coverage gates (config ≥95%, state ≥85%, apply/rtnl/metrics ≥70%).
- Integration tier (`tests/integration/`) — full module-eval against `nixos/lib/eval-config.nix`; asserts rendered config, capabilities, and telegraf wiring.
- VM tier (`tests/vm/`, 9 scenarios) — smoke, failover (v4 / v6 / dual-stack), recovery, hooks, metrics (Telegraf round-trip), family-health-policy (two-node real-ICMP), nftzones-integration. Linux + KVM only.

**Documentation**

- `README.md` quickstart.
- `docs/wan-monitoring.md` newcomer intro.
- `docs/architecture.md` layering + Decision pipeline.
- `docs/selector.md` strategy + hysteresis spec.
- `docs/metrics.md` Prometheus catalog with PromQL examples.
- `docs/nftzones-integration.md` end-to-end firewall composition.
- `docs/specs/` — frozen contracts: `daemon-config.md`, `daemon-state.md`, `failover.md`, `probe-algorithm.md`, `prior-art.md`.
- `docs/glossary.md` enforced terminology.

### Schemas

- `config.json` schema version: **1**.
- `state.json` schema version: **1**. (Stays at 1 in [Unreleased] — pre-release we don't bump for in-tree refactors. The first tagged release freezes shape 1.)

[Unreleased]: https://github.com/petohorvath/nixos-wanwatch/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/petohorvath/nixos-wanwatch/releases/tag/v0.1.0
