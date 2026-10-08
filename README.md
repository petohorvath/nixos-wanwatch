# nixos-wanwatch

Multi-WAN monitoring and failover for NixOS. Probes WAN interfaces, decides which is healthy, selects an active member per group, and switches kernel routing state on health changes.

**Status**: v0.1.0 shipped the v1 design: library, NixOS module, daemon, and unit, integration, and VM test tiers. See [`CHANGELOG.md`](./CHANGELOG.md) for unreleased changes.

## What it does

- ICMP / ICMPv6 probes per declared WAN, per family.
- Sliding-window RTT / jitter / loss with hysteresis.
- Carrier / operstate via rtnetlink — carrier-down fast-tracks to unhealthy.
- Per-Group Strategy (v1: `primary-backup`) maps Health to a Selection.
- Atomic apply: route + fwmark rule via netlink, conntrack flush, state snapshot, hook dispatch.
- Prometheus metrics over a Unix socket. Optional Telegraf companion module.

## Quickstart

```nix
{
  inputs.wanwatch.url = "github:petohorvath/nixos-wanwatch";

  outputs = { self, nixpkgs, wanwatch }: {
    nixosConfigurations.router = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        wanwatch.nixosModules.default
        ({ ... }: {
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
              pointToPoint = true;     # LTE / PPP / WireGuard / tun
              probe.targets.v4 = [ "1.1.1.1" ];
            };

            groups.home-uplink = {
              members = [
                { wan = "primary"; priority = 1; }
                { wan = "backup";  priority = 2; }
              ];
              mark  = 1000;   # required — wanwatch.types.fwmark (1000..32767)
              table = 1000;   # required — wanwatch.types.routingTableId (1000..32767)
            };
          };
        })
      ];
    };
  };
}
```

`config.services.wanwatch.marks.home-uplink` and `.tables.home-uplink` re-expose the user-declared fwmark / routing-table id as read-only attributes so downstream firewall configs can reference them by name. See [`docs/nftzones-integration.md`](./docs/nftzones-integration.md).

## Components

| Layer | Where | Role |
|---|---|---|
| Pure-Nix library | `lib/` | Typed values (`wan`, `probe`, `group`, `member`), validation, typed fwmark/routing-table-id primitives, pure selector. |
| NixOS module | `modules/` | `services.wanwatch.*` option surface, JSON renderer, hardened systemd unit. |
| Go daemon | `daemon/` | `wanwatchd` — probe goroutines, rtnl subscriber, selector + hysteresis, netlink apply, state writer, hook runner, Prometheus endpoint. |

## Composition with sibling projects

- **[`nix-libnet`](../nix-libnet)** — IP / CIDR / interface-name validators used throughout the lib.
- **[`nix-nftzones`](../nix-nftzones)** — zone-based nftables firewall. References `services.wanwatch.marks.<group>` in `sroute` rules to direct traffic to the active member.

## Commands

```sh
nix flake check       # unit + integration + VM tier (VM needs /dev/kvm)
nix fmt               # nixfmt + gofumpt + goimports
nix build .#wanwatchd # build the daemon binary
nix develop           # devshell with go, gopls, golangci-lint
```

For executable reviews in a minimal Linux sandbox, use `bash tooling/review-env.sh COMMAND [ARG...]`. It provides the locked Go toolchain and GCC with vendored dependencies. See [review environment setup](.greptile/rules.md) for the optional upstream Nix bootstrap and focused validation commands.

## Documentation

- [`GLOSSARY.md`](./GLOSSARY.md): terminology.
- [`docs/wan-monitoring.md`](./docs/wan-monitoring.md): introduction for newcomers.
- [`docs/architecture.md`](./docs/architecture.md): layers and data flow.
- [`docs/selector.md`](./docs/selector.md): Strategy and Hysteresis.
- [`docs/nftzones-integration.md`](./docs/nftzones-integration.md): wiring with the zone-based firewall.
- [`docs/metrics.md`](./docs/metrics.md): Prometheus catalog.
- [`docs/specs/`](./docs/specs/): frozen JSON contracts, failover and probe semantics, and prior art.
- [`docs/adr/`](./docs/adr/): architectural decisions.

## License

MIT — see [`LICENSE`](./LICENSE).
