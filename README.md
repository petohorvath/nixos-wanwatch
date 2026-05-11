# nixos-wanwatch

Multi-WAN monitoring and failover for NixOS — probe WAN interfaces, decide
which is healthy, select an active member per group, and switch kernel
routing state on health changes.

**Status**: pre-release. No tagged version yet. The shape of the public
surface is locked in [`PLAN.md`](./PLAN.md); implementation is in progress.

## What it does

- Probes each declared WAN interface with ICMP / ICMPv6 (per family).
- Tracks RTT, jitter, and loss via a sliding-window algorithm.
- Combines kernel carrier / operstate events with probe results into a
  per-WAN Health verdict (`up` / `down` / `degraded` / `unknown`).
- Selects an active Member per Group under a configurable Strategy
  (v1: `primary-backup`, single-active).
- Applies the decision to the kernel — rewrites the default route in
  the group's routing table per family via netlink, flushes conntrack
  entries on the dead path.
- Publishes state at `/run/wanwatch/state.json` and runs hook scripts on
  every Decision.
- Exposes Prometheus metrics over a Unix socket for Telegraf /
  Prometheus / any scraper.

## Components

| Layer | Where | Role |
|---|---|---|
| Pure-Nix library | `lib/` | Types (`wan`, `probe`, `group`, `member`), validation, allocators, selection logic. Zero `nixpkgs` dependency in the core. |
| NixOS module | `modules/` | `services.wanwatch.*` — renders daemon config, emits systemd unit, optional Telegraf integration. |
| Go daemon | `daemon/` | `wanwatchd` — probing, decision, netlink-based apply, state publication, hook runner, Prometheus endpoint. |

## Composition with sibling projects

- **[`nix-libnet`](../nix-libnet)** — IP/CIDR/interface validation used
  throughout the lib.
- **[`nix-nftzones`](../nix-nftzones)** — zone-based nftables firewall.
  wanwatch publishes per-group fwmarks; nftzones references them in
  `sroute` / `droute` rules. No nftzones changes required.

## Documentation

- [`PLAN.md`](./PLAN.md) — the v1 design plan; authoritative on scope,
  API surface, build order, and conventions.
- [`docs/glossary.md`](./docs/glossary.md) — terminology used across
  code, comments, commits, and docs.
- [`CLAUDE.md`](./CLAUDE.md) — conventions for AI-assisted contributions
  and human contributors alike.

## Quick check

```sh
nix flake check     # all unit + integration + vm tests
nix fmt             # nixfmt + gofumpt + goimports via treefmt
```

## Local development against sibling projects

The flake pins `nix-libnet` via a github URL by default. For local
development against an unpushed checkout, override the input:

```sh
nix flake check --override-input libnet path:../nix-libnet
```

## License

MIT — see [`LICENSE`](./LICENSE).
