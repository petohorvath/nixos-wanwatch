# nixos-wanwatch

Multi-WAN monitoring and failover for NixOS. Probes WAN interfaces, decides which is healthy, selects an active member per group, and switches kernel routing state on health changes.

**Status**: v0.1.0 — feature-complete per [`PLAN.md`](./PLAN.md). Library, NixOS module, daemon, and full test tier (unit + integration + VM) are in place.

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

            groups.home-uplink.members = [
              { wan = "primary"; priority = 1; }
              { wan = "backup";  priority = 2; }
            ];
          };
        })
      ];
    };
  };
}
```

`config.services.wanwatch.marks.home-uplink` and `.tables.home-uplink` expose the auto-allocated fwmark / routing-table id for downstream firewall configs. See [`docs/nftzones-integration.md`](./docs/nftzones-integration.md).

## Components

| Layer | Where | Role |
|---|---|---|
| Pure-Nix library | `lib/` | Typed values (`wan`, `probe`, `group`, `member`), validation, mark/table allocators, pure selector. |
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

## Documentation

- [`PLAN.md`](./PLAN.md) — authoritative v1 design.
- [`docs/wan-monitoring.md`](./docs/wan-monitoring.md) — newcomer's introduction.
- [`docs/architecture.md`](./docs/architecture.md) — layering + data flow.
- [`docs/selector.md`](./docs/selector.md) — strategy + hysteresis algorithm.
- [`docs/nftzones-integration.md`](./docs/nftzones-integration.md) — wiring with the zone-based firewall.
- [`docs/metrics.md`](./docs/metrics.md) — Prometheus catalog.
- [`docs/specs/`](./docs/specs/) — frozen JSON contracts and prior-art distillation.
- [`docs/glossary.md`](./docs/glossary.md) — enforced terminology.

## License

MIT — see [`LICENSE`](./LICENSE).
