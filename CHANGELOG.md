# Changelog

All notable changes to `nixos-wanwatch` are recorded here. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); version numbers follow [SemVer](https://semver.org/spec/v2.0.0.html). Schema versions for the daemon's JSON contracts (`config.json`, `state.json`) are independent — bumped only on incompatible shape changes; see [`docs/specs/daemon-config.md`](./docs/specs/daemon-config.md) and [`docs/specs/daemon-state.md`](./docs/specs/daemon-state.md).

## [Unreleased]

## [0.1.0] - 2026-10-31

The first release: multi-WAN monitoring and single-active failover for NixOS.

The README's [Limitations](./README.md#limitations) lists what this release does not do.

### Added

- `nixosModules.default`, the `services.wanwatch` module. It declares WANs with their Probes and Groups of Members with a `primary-backup` Strategy, validates them at evaluation, renders `/etc/wanwatch/config.json`, and runs the daemon as `wanwatch:wanwatch` in a hardened systemd service that restarts when the rendered configuration changes.
- Each Group's user-declared fwmark and routing table, published as the read-only options `services.wanwatch.marks.<group>` and `services.wanwatch.tables.<group>` for firewall modules such as [nixos-nftzones](https://github.com/petohorvath/nixos-nftzones).
- `nixosModules.telegraf`, which scrapes the daemon's metrics with Telegraf.
- `wanwatchd`, the Go daemon, as `packages.<system>.wanwatchd` for `x86_64-linux` and `aarch64-linux`. It probes each WAN with ICMP and ICMPv6 over a sliding Window per Target, derives Health per Family with thresholds and Hysteresis, and treats carrier loss as immediately unhealthy. It discovers each Family's Gateway from the main routing table, or installs a `scope link` route for a point-to-point WAN. On a Decision, it writes the Group's default routes into the Group's table and, when the Group's traffic moves to another WAN, flushes the vacated WAN's conntrack entries.
- State in `/run/wanwatch/state.json`, `state.json` schema 1 ([daemon-state](./docs/specs/daemon-state.md)).
- Hooks in `/etc/wanwatch/hooks/{up,down,switch}.d/`, run after each Decision with the `WANWATCH_*` variables and an allowlisted environment ([daemon-state](./docs/specs/daemon-state.md#hook-env-var-contract)).
- Prometheus metrics on the Unix socket `/run/wanwatch/metrics.sock` ([catalog](./docs/metrics.md)).
- `config.json` schema 1, the daemon's configuration format ([daemon-config](./docs/specs/daemon-config.md)).
- `lib`, the pure-Nix library behind the module: validated WAN, Probe, Member, Group, and global-settings values, option types, the selector, and the config renderer. Of these, `lib.types.fwmark`, `lib.types.routingTableId`, `lib.config.paths`, and `lib.version` are stable.
- The 0.x stability promise ([ADR-0018](./docs/adr/0018-0x-stability-promise.md)): every 0.x release keeps both schema 1 contracts, the Hook variables, and the metric catalog compatible. The `services.wanwatch` options and the rest of `lib` may break in a minor release, with a migration note here.

### Security

- `wanwatchd` builds with Go 1.26.10 from the locked nixos-26.05. It fixes eight standard-library vulnerabilities, in `net/http`, `crypto/tls`, `crypto/x509`, and `net/textproto`, that `govulncheck` found reachable from the daemon under Go 1.26.7: GO-2026-6603, GO-2026-6607, GO-2026-6608, GO-2026-6611, GO-2026-6612, GO-2026-6613, GO-2026-6617, and GO-2026-6809. `govulncheck` reports none against this release.

[unreleased]: https://github.com/petohorvath/nixos-wanwatch/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/petohorvath/nixos-wanwatch/releases/tag/v0.1.0
