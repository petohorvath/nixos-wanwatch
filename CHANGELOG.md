# Changelog

All notable changes to `nixos-wanwatch` are recorded here. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); version numbers follow [SemVer](https://semver.org/spec/v2.0.0.html). Schema versions for the daemon's JSON contracts (`config.json`, `state.json`) are independent — bumped only on incompatible shape changes; see [`docs/specs/daemon-config.md`](./docs/specs/daemon-config.md) and [`docs/specs/daemon-state.md`](./docs/specs/daemon-state.md).

## [Unreleased]

### Changed

- Update `nix-libnet` to v0.2.0 and `nixos-nftzones` to v0.1.0, whose `nix-nftypes` input now follows a new root `nix-nftypes` input at v0.2.0. Like `nixos-nftzones`, it serves only the flake's checks; a host drops it with `inputs.wanwatch.inputs.nix-nftypes.follows = "";`, as the README shows.

### Fixed

- The daemon's link and route subscriptions now get their full 1 MiB netlink receive buffer regardless of `net.core.rmem_max`, so a burst of link or route changes is less likely to overflow it and restart the daemon. The daemon forces the size with `SO_RCVBUFFORCE`, which needs the `CAP_NET_ADMIN` capability the unit already grants.
- The daemon no longer adds another copy of each Group's fwmark rule, per Family, on every start. It installs a rule only when no rule with the same mark, mask, and table and no other selector exists, and removes the copies earlier builds left behind. A narrower rule, such as one that also matches an `iif`, is neither counted as a copy nor removed.
- Loss thresholds compare exactly and independently of Target order. A Family's loss stays the unweighted mean of its Targets' loss ratios, now computed as an exact rational, so a WAN at exactly `lossPctUp` or `lossPctDown` gets the documented verdict, and replaying the same Samples gives the same Decisions. RTT thresholds compare in integer microseconds. `lossRatio` in `state.json` and `wanwatch_probe_loss_ratio` report the float nearest the exact loss, so three Targets that each lost 1 of 10 Samples report `0.1` rather than `0.10000000000000002`.
- The daemon rejects an invalid, zoned, wrong-Family, IPv4-mapped, or duplicate Target when it loads `config.json`, with a `config: invalid` error that names the field. Before, a Pinger failed on such a Target, possibly after routes and rules were written, and its exit stopped failover for every Group; a duplicate halved the Hysteresis timing instead. A non-canonical literal is still accepted.
- `services.wanwatch` and `wanwatch.probe.make` reject an IPv4-mapped address (`::ffff:0:0/96`) in `targets.v6`, as `probeTargetIpv4Mapped`, and an address listed more than once in one Target bucket, as `probeDuplicateTarget`. Addresses compare in canonical form. The daemon read a mapped address as IPv4 and exited, and a duplicate put two Samples per cycle into one Window, halving the Hysteresis timing. NixOS concatenates list definitions, so two modules that list the same Target now fail evaluation; remove one copy or set the list with `lib.mkForce`.
- Gateway discovery now selects the preferred default route: the lowest metric on the WAN's interface, ties going to the first one listed. It used to take the last default listed, so with two defaults on one interface, marked traffic could leave through a next-hop that the Probes did not measure. Discovery also finds the Gateway of a multipath default, from its next-hop on the WAN's interface; such a default used to leave the WAN without a Gateway.
- A prober, subscriber, or the metrics server that returns while the daemon is running now stops the daemon so systemd restarts it, even when it returns no error. Before, a subsystem that returned `nil` or a `context.Canceled` of its own was ignored, and the daemon kept running without it.
- A handler still running when the daemon is told to stop no longer writes routes or `state.json` with the cancelled context. Each such write used to fail, log a `route write` error, and increment `wanwatch_apply_route_errors_total` during an orderly shutdown.
- `wanwatch.config.render` rejects a WAN or Group whose attribute key differs from its `name`, naming every such key. The daemon refuses such a `config.json` at startup, so the mismatch used to surface as a failed unit instead of an evaluation error. `services.wanwatch` was not affected, because each `name` defaults to its key.
- `tryMake` and `make` of every value type (`wan`, `probe`, `group`, `member`, `global`) reject an input that isn't an attribute set, as `<type>NotAnAttrset`. `wanwatch.global.tryMake` used to accept an integer, a string, or `null` as the defaults, so `wanwatch.config.render { global = null; }` rendered, and the other types reported a misleading kind, such as `probeNoTargets` for `probe = null`.

## [0.1.0] - 2026-10-30

The first release: multi-WAN monitoring and single-active failover for NixOS.

### Added

- `nixosModules.default`, the `services.wanwatch` module. It declares WANs with their Probes and Groups of Members with a `primary-backup` Strategy, validates them at evaluation, renders `/etc/wanwatch/config.json`, and runs the daemon as `wanwatch:wanwatch` in a hardened systemd service that restarts when the rendered configuration changes.
- Each Group's user-declared fwmark and routing table, published as the read-only options `services.wanwatch.marks.<group>` and `services.wanwatch.tables.<group>` for firewall modules such as [nixos-nftzones](https://github.com/petohorvath/nixos-nftzones).
- `nixosModules.telegraf`, which scrapes the daemon's metrics with Telegraf.
- `wanwatchd`, the Go daemon, as `packages.<system>.wanwatchd` for `x86_64-linux` and `aarch64-linux`. It probes each WAN with ICMP and ICMPv6 over a sliding Window per Target, derives Health per Family with thresholds and Hysteresis, and treats carrier loss as immediately unhealthy. It discovers each Family's Gateway from the main routing table, or installs a `scope link` route for a point-to-point WAN. On a Decision, it writes the Group's default routes into the Group's table and, on a switch, flushes the vacated WAN's conntrack entries.
- State in `/run/wanwatch/state.json`, `state.json` schema 1 ([daemon-state](./docs/specs/daemon-state.md)).
- Hooks in `/etc/wanwatch/hooks/{up,down,switch}.d/`, run after each Decision with the `WANWATCH_*` variables and an allowlisted environment ([daemon-state](./docs/specs/daemon-state.md#hook-env-var-contract)).
- Prometheus metrics on the Unix socket `/run/wanwatch/metrics.sock` ([catalog](./docs/metrics.md)).
- `config.json` schema 1, the daemon's configuration format ([daemon-config](./docs/specs/daemon-config.md)).
- `lib`, the pure-Nix library behind the module: validated WAN, Probe, Member, Group, and global-settings values, option types, the selector, and the config renderer. Of these, `lib.types.fwmark`, `lib.types.routingTableId`, `lib.config.paths`, and `lib.version` are stable.
- The 0.x stability promise ([ADR-0018](./docs/adr/0018-0x-stability-promise.md)): every 0.x release keeps both schema 1 contracts, the Hook variables, and the metric catalog compatible. The `services.wanwatch` options and the rest of `lib` may break in a minor release, with a migration note here.

The README's [Limitations](./README.md#limitations) lists what this release does not do.

[unreleased]: https://github.com/petohorvath/nixos-wanwatch/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/petohorvath/nixos-wanwatch/releases/tag/v0.1.0
