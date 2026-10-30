# nixos-wanwatch

`nixos-wanwatch` has no release yet: its options and contracts may still change, and [`CHANGELOG.md`](./CHANGELOG.md) records each change under Unreleased.

`nixos-wanwatch` monitors a host's WANs and fails traffic over to a healthy one. It provides a NixOS module, `services.wanwatch`, and a daemon, `wanwatchd`, that probes each WAN with ICMP, derives its Health, and rewrites policy-routing tables when the preferred WAN fails or recovers. It serves NixOS routers and gateways with a backup connection, such as an LTE modem behind a fixed-line link.

## Overview

A WAN is an egress interface plus a Probe: the Targets it pings, the interval, and the loss and RTT thresholds. The daemon keeps a sliding Window of Samples per Target and turns them into a healthy or unhealthy verdict per IP Family. Hysteresis requires several consecutive cycles before Health flips, and carrier loss reported by the kernel makes a WAN unhealthy immediately.

A Group holds WANs as Members, keyed by WAN name, each with a `priority`. The `primary-backup` Strategy selects the healthy Member with the lowest `priority` value, breaking ties by WAN name.

Each Group owns an fwmark and a routing table. At startup, the daemon installs `ip rule` entries that send marked packets to the table. When the Selection moves to a Member, the daemon writes that Member's default route into the table; on a switch between Members, it also flushes conntrack entries for the vacated WAN. It then publishes the new State and runs Hooks.

Only packets that carry a Group's mark follow its routing table. The daemon leaves the main table alone, so a firewall rule must mark the traffic to steer; see [Steer traffic into a Group](#steer-traffic-into-a-group).

[`docs/wan-monitoring.md`](./docs/wan-monitoring.md) introduces the concepts in more depth, and [`GLOSSARY.md`](./GLOSSARY.md) defines each term.

## Contents

The flake exposes these outputs:

| Output                                                          | Contents                                                                                                                                       |
| --------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| `nixosModules.default`, also `nixosModules.wanwatch`            | The `services.wanwatch` module: options, rendered daemon config, a `wanwatch` system user, and a hardened systemd service                      |
| `nixosModules.telegraf`                                         | Optional `services.wanwatch.telegraf` module that scrapes the daemon's metrics with Telegraf                                                   |
| `packages.<system>.wanwatchd`, also `packages.<system>.default` | The Go daemon, for `x86_64-linux` and `aarch64-linux`                                                                                          |
| `lib`                                                           | The pure-Nix library behind the module: validated WAN, Probe, Member, Group, and global-settings values, option types, and the config renderer |

The sources live in [`nixos/`](./nixos/), [`daemon/`](./daemon/), and [`lib/`](./lib/); [`docs/architecture.md`](./docs/architecture.md) describes how the layers fit together.

## Requirements

The module needs NixOS with flakes enabled. The daemon runs on Linux only, so the flake supports `x86_64-linux` and `aarch64-linux`; CI builds and tests both.

## Install

Add the flake as an input and import its module into the host's configuration:

```nix
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
    wanwatch = {
      url = "github:petohorvath/nixos-wanwatch";
      inputs = {
        nixpkgs.follows = "nixpkgs";
        treefmt-nix.follows = "";
        nix-unit.follows = "";
        nixos-nftzones.follows = "";
      };
    };
  };

  outputs =
    { nixpkgs, wanwatch, ... }:
    {
      nixosConfigurations.router = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        modules = [
          wanwatch.nixosModules.default
          ./configuration.nix
        ];
      };
    };
}
```

The `treefmt-nix`, `nix-unit`, and `nixos-nftzones` inputs serve only the flake's own checks. Setting their `follows` to `""` drops them, so they stay out of the host's `flake.lock`.

## Usage

Declare the WANs and a Group that fails over between them:

```nix
{
  services.wanwatch = {
    enable = true;

    wans.primary = {
      interface = "eth0";
      probe.targets = {
        v4 = [ "1.1.1.1" "8.8.8.8" ];
        v6 = [ "2606:4700:4700::1111" ];
      };
    };

    wans.backup = {
      interface = "wwan0";
      pointToPoint = true;
      probe.targets.v4 = [ "1.1.1.1" ];
    };

    groups.home-uplink = {
      members = {
        primary.priority = 1;
        backup.priority = 2;
      };
      mark = 1000;
      table = 1000;
    };
  };
}
```

A WAN serves the Families that have Targets, so `primary` is dual-stack and `backup` is IPv4-only. For broadcast links, the daemon discovers each Family's Gateway from the main routing table. Point-to-point links such as PPP, WireGuard, GRE, and tun set `pointToPoint = true` and get a `scope link` default route instead. Each Group needs a `mark` and a `table` in `1000..32767`, unique across Groups.

After `nixos-rebuild switch`, the daemon publishes its State to `/run/wanwatch/state.json`:

```sh
jq '.groups' /run/wanwatch/state.json
```

```json
{
  "home-uplink": {
    "selection": "primary",
    "selectionSince": "2026-10-13T09:12:04.118204511Z",
    "decisionsTotal": 1,
    "strategy": "primary-backup"
  }
}
```

`ip rule show` includes a `fwmark 0x3e8 lookup 1000` rule, and `ip route show table 1000` holds the default route through `eth0`. When `primary` fails, the daemon replaces that route with `default dev wwan0 scope link`.

[`docs/wan-monitoring.md`](./docs/wan-monitoring.md) covers every Probe option and its default, and [`docs/selector.md`](./docs/selector.md) explains thresholds, Hysteresis, and the Strategy.

## Steer traffic into a Group

The module publishes each Group's mark and table as the read-only options `services.wanwatch.marks.<group>` and `services.wanwatch.tables.<group>`. Firewall rules reference these names instead of repeating the integers.

With plain nftables, mark the forwarded LAN traffic before the routing decision:

```nix
{ config, ... }:
let
  mark = config.services.wanwatch.marks.home-uplink;
in
{
  networking.nftables.tables.wanwatch-steering = {
    family = "inet";
    content = ''
      chain prerouting {
        type filter hook prerouting priority mangle; policy accept;
        iifname "br-lan" meta mark set ${toString mark}
      }
    '';
  };
}
```

Most routers also apply source NAT on the Group's WAN interfaces; because it matches every interface in the Group, it follows the route to whichever Member is selected. [`docs/nftzones-integration.md`](./docs/nftzones-integration.md) shows the complete setup with [nixos-nftzones](https://github.com/petohorvath/nixos-nftzones), including SNAT and what changes on a failover.

## Hooks

After each Decision, the daemon runs the executables in `/etc/wanwatch/hooks/{up,down,switch}.d/` in lexicographic order, passing the Group, the old and new WAN, their interfaces, Gateways, and Families, and a timestamp in `WANWATCH_*` environment variables:

```nix
{ pkgs, ... }:
{
  environment.etc."wanwatch/hooks/switch.d/log".source =
    pkgs.writeShellScript "wanwatch-log" ''
      ${pkgs.util-linux}/bin/logger -t wanwatch \
        "$WANWATCH_GROUP: $WANWATCH_WAN_OLD -> $WANWATCH_WAN_NEW on $WANWATCH_INTERFACE_NEW"
    '';
}
```

Hooks are notifications: a failing or timed-out Hook is logged and counted but never undoes a Decision. `services.wanwatch.global.hookTimeoutMs` sets the deadline. [`docs/specs/daemon-state.md`](./docs/specs/daemon-state.md) lists every variable and the execution rules.

Hooks run as the `wanwatch` user inside the daemon's systemd sandbox, which allows writes only under `/run/wanwatch` and a private `/tmp`. They keep the daemon's `CAP_NET_ADMIN` and `CAP_NET_RAW` capabilities, so a Hook can change routes, rules, and nft sets; treat every installed Hook as privileged. Hooks inherit only an allowlisted part of the daemon's environment, which the spec lists. To let Hooks reach another service, such as a socket owned by its group, add that group to `users.users.wanwatch.extraGroups`.

## Metrics

The daemon serves Prometheus metrics on the Unix socket `/run/wanwatch/metrics.sock`, readable by members of the `wanwatch` group. To scrape them with Telegraf, add `wanwatch.nixosModules.telegraf` to the host's modules next to `wanwatch.nixosModules.default`, then enable it:

```nix
{
  services.telegraf.enable = true;
  services.wanwatch.telegraf.enable = true;
}
```

[`docs/metrics.md`](./docs/metrics.md) catalogs the series and gives example PromQL queries.

## Global settings

`services.wanwatch.global` sets the daemon's log level and Hook timeout:

| Option          | Default |
| --------------- | ------- |
| `logLevel`      | `info`  |
| `hookTimeoutMs` | `5000`  |

## Paths

The daemon runs as the `wanwatch` user and group, which the module creates. Its systemd sandbox makes the file system read-only apart from `/run/wanwatch` and a private `/tmp`, so the module fixes the daemon's paths ([ADR-0014](./docs/adr/0014-module-owns-identity-and-paths.md)):

| Path                         | Contents                                                                                                        |
| ---------------------------- | --------------------------------------------------------------------------------------------------------------- |
| `/etc/wanwatch/config.json`  | The rendered configuration; [`docs/specs/daemon-config.md`](./docs/specs/daemon-config.md) specifies its format |
| `/etc/wanwatch/hooks/`       | Hook scripts in `up.d/`, `down.d/`, and `switch.d/`                                                             |
| `/run/wanwatch/state.json`   | State                                                                                                           |
| `/run/wanwatch/metrics.sock` | Prometheus metrics                                                                                              |

## Documentation

- [`GLOSSARY.md`](./GLOSSARY.md): the terms used in options, State, metrics, and logs.
- [`docs/wan-monitoring.md`](./docs/wan-monitoring.md): WANs, Probes, Health, Groups, and Decisions, with the Probe defaults.
- [`docs/architecture.md`](./docs/architecture.md): layers, data flow, Families, and Gateways.
- [`docs/selector.md`](./docs/selector.md): thresholds, Hysteresis, and Strategy.
- [`docs/nftzones-integration.md`](./docs/nftzones-integration.md): firewall integration.
- [`docs/metrics.md`](./docs/metrics.md): the Prometheus catalog and Telegraf.
- [`docs/specs/`](./docs/specs/): the daemon config, State, failover, and probe-algorithm contracts.
- [`docs/adr/`](./docs/adr/): architectural decisions.

## Limitations

- Failover is single-active: each Group routes through one Member at a time, and `primary-backup` is the only Strategy. Load balancing is not supported.
- Probes use ICMP and ICMPv6 echo only; TCP, HTTP, and DNS probes are not available.
- The daemon does not reload its configuration; `nixos-rebuild switch` restarts the service whenever the rendered configuration changes ([ADR-0006](./docs/adr/0006-restart-only-reconfiguration.md)). During the restart, `/run/wanwatch`, with `state.json` and the metrics socket, is briefly absent.
- When no Member of a Group is healthy, the daemon leaves the Group's last default route in place.
- When the Selection moves to a WAN that serves fewer Families, the dropped Family keeps its previous route.
- The conntrack flush on a switch blocks the daemon's other work while it runs. On Linux 6.12 and later the kernel deletes the vacated WAN's IPv4 entries in one request, which took about 0.2 s for a full table on a fast x86 server. On older kernels, and for IPv6 on every kernel, the daemon reads the table and deletes each matching entry, which can take seconds on a large table.
- On a switch, the daemon flushes conntrack entries by the vacated WAN's addresses. When several Groups share that WAN, the flush also resets the flows of the Groups that still route through it; those flows reconnect through the same WAN.
- On hosts that run systemd-networkd, the module defaults `ManageForeignRoutingPolicyRules` and `ManageForeignRoutes` to `false`, so networkd stops removing routes and rules it did not create.

## Stability

From v0.1.0, every 0.x release keeps these contracts compatible: `config.json` and `state.json` schema 1, the Hook variables, and the metric catalog. A release may add State fields, Hook variables, and metrics, so readers of `state.json` ignore the fields they do not know.

The `services.wanwatch` options and `lib` may still break in a 0.x minor release, with a migration note in [`CHANGELOG.md`](./CHANGELOG.md). In `lib`, only `lib.types`, `lib.config.paths`, and `lib.version` are stable; every other name is an unstable internal of the module.

[ADR-0018](./docs/adr/0018-0x-stability-promise.md) records the promise and its reasoning.

## Contributing

Ask questions and report bugs in [GitHub issues](https://github.com/petohorvath/nixos-wanwatch/issues). [`AGENTS.md`](./AGENTS.md) describes the development shell, checks, conventions, and commit format that pull requests follow.

## License

MIT; see [`LICENSE`](./LICENSE).
