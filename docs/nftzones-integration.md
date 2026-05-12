# nftzones integration

`nixos-wanwatch` and `nix-nftzones` compose via two attribute outputs and a runtime convention. **No nftzones changes are required** — the integration is a wanwatch-side contract that any firewall consumer can use.

## The contract

| Step | Owner | What |
|---|---|---|
| 1 | wanwatch | Allocates a deterministic fwmark + routing-table id per Group. Exposes them as `config.services.wanwatch.marks.<group>` and `.tables.<group>`. |
| 2 | wanwatch (daemon) | Installs `ip rule add fwmark <mark> table <table>` and the v6 equivalent at startup. Idempotent. |
| 3 | wanwatch (daemon) | Writes `default via <gw> dev <iface>` into the group's table per family on every Decision. |
| 4 | nftzones (user) | Sets `meta mark set <mark>` in `sroute` / `droute` rules, referencing `config.services.wanwatch.marks.<group>` by name. |
| 5 | nftzones (user, optional) | Adds an `snat` rule masquerading egress out of the WAN zone — the SNAT automatically follows the active interface because the route does. |

The table id is *shared* across families: v4 uses `table <table>` in the v4 RIB; v6 uses the same `table <table>` in the v6 RIB. Both populated by the same daemon Decision.

## End-to-end example

```nix
{ config, ... }: {

  # wanwatch declarations
  services.wanwatch = {
    enable = true;
    wans.primary = {
      interface = "eth0";
      gateways.v4 = "192.0.2.1";
      gateways.v6 = "2001:db8::1";
      probe.targets = [ "1.1.1.1" "2606:4700:4700::1111" ];
    };
    wans.backup = {
      interface = "wwan0";
      gateways.v4 = "100.64.0.1";
      probe.targets = [ "8.8.8.8" ];
    };
    groups.home-uplink.members = [
      { wan = "primary"; priority = 1; }
      { wan = "backup";  priority = 2; }
    ];
  };

  # nftzones — references the allocated mark by name
  networking.nftzones.tables.fw = {
    family = "inet";
    zones = {
      lan      = { interfaces = [ "br-lan" ]; };
      wan-home = { interfaces = [ "eth0" "wwan0" ]; };
    };

    sroutes.lan-via-home = {
      from = [ "lan" ];
      rule = [
        (nftypes.dsl.mangle nftypes.dsl.fields.meta.mark
          config.services.wanwatch.marks.home-uplink)
      ];
    };

    snats.wan-home = {
      from = [ "lan" ];
      to   = [ "wan-home" ];
    };
  };
}
```

What this configures:

1. `wanwatch.marks.home-uplink` evaluates to an int (e.g. `100`) deterministically from the group name.
2. The `mangle` rule in nftzones renders to `meta mark set 0x64` in the loaded nftables ruleset.
3. The wanwatchd daemon, at startup, adds `ip rule add fwmark 0x64 table 100` and the v6 equivalent.
4. On every Decision, wanwatchd rewrites table `100`'s default route per family to the active member's gateway.
5. The nftzones snat rule masquerades LAN-origin traffic in the `wan-home` zone. Because the SNAT applies in the wan zone (regardless of which interface in that zone), it follows the daemon's route changes for free.

## What changes on a Decision

```
Before failover:                After failover:
ip rule:                        ip rule:                  (unchanged)
  fwmark 0x64 lookup 100          fwmark 0x64 lookup 100

ip route table 100:             ip route table 100:
  default via 192.0.2.1 eth0      default via 100.64.0.1 wwan0
ip -6 route table 100:          ip -6 route table 100:
  default via 2001:db8::1 eth0    default via 2001:db8::1 wwan0  (if backup has v6)
                                  (no v6 default at all)         (if backup has only v4)

nft rule (sroutes.lan-via-home): nft rule:                 (unchanged)
  meta mark set 0x64              meta mark set 0x64
```

Only the route changes. The mark, the rule, and the nftables ruleset are all stable.

## Why per-name, not per-int

A typical "fwmark routing" how-to assigns marks manually (`100` for one uplink, `200` for another) and references them as integers across files. Two failure modes:

1. **Drift**: someone bumps the mark in one file and forgets the other.
2. **Collision**: a third config (e.g. WireGuard, Tailscale, Calico) picks the same integer.

wanwatch sidesteps both:

- The mark is generated from the Group's name via SHA-256. Same name everywhere → same mark, by construction.
- `config.resolveAllocations` detects collisions between auto-allocated and user-explicit values at module-eval time and refuses to render.

## Cross-references

| File | What |
|---|---|
| `lib/internal/config.nix:resolveAllocations` | Mark / table auto-assignment + collision detection. |
| `lib/internal/marks.nix` / `tables.nix` | Allocators (hash + linear probe). |
| `daemon/internal/apply/rule.go:EnsureRule` | Installs the fwmark policy rules at startup. |
| `daemon/internal/apply/route.go:WriteDefault` | Rewrites the table's default on every Decision. |
| `tests/vm/nftzones-integration.nix` | VM scenario asserting the full contract on a live kernel. |

## Decision policy: SNAT vs DNAT

For most home-router cases, **SNAT in the WAN zone is the right tool**. The `sroute` mangle marks the traffic; the policy rule + routing table picks the egress interface; SNAT in the WAN zone rewrites the source IP to the active interface's address. Failover changes only the route; the SNAT rule never needs to know about it.

DNAT (port forwarding inbound) is asymmetric — inbound flows arrive on the *active* interface. If the active changes mid-flow, existing connections break (no graceful path without conntrack helpers). v1 has no opinion here; users wire DNAT manually against the interfaces.

## What this doesn't cover

- **Multi-WAN load balancing** — v1 is single-active. The `load-balance` strategy arrives in v2 with multipath nexthops.
- **Per-flow policy** — the mark is per-Group. If two flows out of the LAN should go through different WANs simultaneously, declare two Groups (`home-uplink` and `voip-uplink`) and two sroute rules.
- **IPv6 prefix delegation** — wanwatch doesn't touch addressing; it only writes default routes via gateways the user declared. PD changes are the user's problem (or systemd-networkd's).
