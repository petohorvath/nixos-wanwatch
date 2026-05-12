# Changelog

All notable changes to `nixos-wanwatch` are recorded here. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); version numbers follow [SemVer](https://semver.org/spec/v2.0.0.html). Schema versions for the daemon's JSON contracts (`config.json`, `state.json`) are independent — bumped only on incompatible shape changes; see [`docs/specs/daemon-config.md`](./docs/specs/daemon-config.md) and [`docs/specs/daemon-state.md`](./docs/specs/daemon-state.md).

## [Unreleased]

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
- Prometheus registry over a 0660 Unix socket; 18 metrics per PLAN §7.2.

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
- `state.json` schema version: **1**.

[Unreleased]: https://github.com/petohorvath/nixos-wanwatch/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/petohorvath/nixos-wanwatch/releases/tag/v0.1.0
