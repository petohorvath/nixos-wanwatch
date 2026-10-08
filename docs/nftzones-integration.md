# nftzones integration

`nixos-wanwatch` and `nix-nftzones` compose through two published Nix values and a runtime convention. nftzones needs no changes: the contract is on the wanwatch side, and any nftables or iptables configuration can use it. See [ADR 0007](./adr/0007-nftzones-integration-via-published-values.md).

## Contract

| Step | Owner | Action |
|---|---|---|
| 1 | wanwatch (module) | Publishes each Group's user-declared `mark` and `table` as `config.services.wanwatch.marks.<group>` and `.tables.<group>`. |
| 2 | wanwatch (daemon) | Installs `ip rule add fwmark <mark> table <table>` and its IPv6 equivalent at startup. Idempotent. |
| 3 | wanwatch (daemon) | On every Decision, writes the active Member's default route into the Group's table for each Family. |
| 4 | nftzones (user) | Sets `meta mark set <mark>` in `sroute` or `droute` rules, referencing `config.services.wanwatch.marks.<group>`. |
| 5 | nftzones (user, optional) | Adds an `snat` rule for egress out of the WAN zone. It follows the active interface because the route does. |

One table ID serves both Families: the same `table <table>` exists in the IPv4 and IPv6 routing tables, and one Decision populates both.

## End-to-end example

```nix
{ config, ... }: {

  # wanwatch declarations
  services.wanwatch = {
    enable = true;
    wans.primary = {
      interface = "eth0";
      probe.targets = {
        v4 = [ "1.1.1.1" ];
        v6 = [ "2606:4700:4700::1111" ];
      };
    };
    wans.backup = {
      interface = "wwan0";
      pointToPoint = true;
      probe.targets.v4 = [ "8.8.8.8" ];
    };
    groups.home-uplink = {
      members = [
        { wan = "primary"; priority = 1; }
        { wan = "backup";  priority = 2; }
      ];
      mark  = 1000;
      table = 1000;
    };
  };

  # nftzones — references the user-declared mark by name
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

This configuration has the following effect:

1. `services.wanwatch.marks.home-uplink` is the integer declared in `services.wanwatch.groups.home-uplink.mark`, re-exposed read-only so downstream modules reference it by name.
2. The nftzones `mangle` rule renders to `meta mark set 0x3e8` (1000).
3. At startup, `wanwatchd` adds `ip rule add fwmark 0x3e8 table 1000` and its IPv6 equivalent.
4. On every Decision, `wanwatchd` rewrites table `1000`'s default route per Family for the active Member.
5. The nftzones `snat` rule masquerades LAN traffic leaving the `wan-home` zone. It applies to every interface in the zone, so it follows the daemon's route changes.

## What changes on a Decision

Failover from `primary` (dual-stack) to `backup` (point-to-point, IPv4 only) changes only the routes:

```text
Before failover:                 After failover:
ip rule:                         ip rule:                    (unchanged)
  fwmark 0x3e8 lookup 1000         fwmark 0x3e8 lookup 1000

ip route table 1000:             ip route table 1000:
  default via 192.0.2.1 eth0       default dev wwan0 scope link
ip -6 route table 1000:          ip -6 route table 1000:     (unchanged)
  default via 2001:db8::1 eth0     default via 2001:db8::1 eth0

nft rule (sroutes.lan-via-home): nft rule:                   (unchanged)
  meta mark set 0x3e8              meta mark set 0x3e8
```

The mark, the policy rule, and the nftables ruleset stay stable. A Family the new Member does not serve keeps its previous route; the stale-route policy is tracked in [`TODO.md`](../TODO.md).

## Why reference marks by name

Typical fwmark-routing recipes repeat the integer in every file:

```nft
table inet fw {
  chain forward { ... meta mark set 100 ... }
}
# ip rule add fwmark 100 table 100
# ip route add default via 192.0.2.1 dev eth0 table 100
```

That invites two failures:

1. **Drift**: one file changes the mark and another does not.
2. **Collision**: another configuration, such as WireGuard, Tailscale, or Calico, picks the same integer.

wanwatch keeps the integer in one place, `services.wanwatch.groups.<group>.mark`, and re-exposes it as `services.wanwatch.marks.<group>`:

- The same name always yields the same integer.
- `config.assertUniqueMarksAndTables` rejects two Groups sharing a `mark` or `table` at evaluation time.
- `wanwatch.types.fwmark` and `routingTableId` restrict values to `[1000, 32767]`, clear of the kernel-reserved tables `{253, 254, 255}` and the small integers ad hoc scripts tend to use.

## Source map

| File | Role |
|---|---|
| `lib/internal/config.nix:assertUniqueMarksAndTables` | Rejects duplicate marks and tables across Groups. |
| `daemon/internal/apply/rule.go:EnsureRule` | Installs the fwmark policy rules at startup. |
| `daemon/internal/apply/route.go:WriteDefault` | Rewrites the table's default route on every Decision. |
| `tests/vm/nftzones-integration.nix` | VM scenario asserting the contract on a live kernel. |

## SNAT and DNAT

SNAT in the WAN zone suits most home routers. The `sroute` mangle marks traffic, the policy rule and routing table pick the egress interface, and SNAT rewrites the source to the active interface's address. Failover changes only the route, so the SNAT rule never needs to know about it.

DNAT (inbound port forwarding) is asymmetric: inbound flows arrive on the active interface, so a mid-flow switch breaks existing connections. wanwatch has no DNAT policy; wire DNAT manually against the interfaces.

## Out of scope

- **Load balancing.** Selection is single-active; see [ADR 0001](./adr/0001-single-active-failover.md).
- **Per-flow policy.** The mark is per Group. To route two LAN flows through different WANs at once, declare two Groups (for example `home-uplink` and `voip-uplink`) and two `sroute` rules.
- **IPv6 prefix delegation.** wanwatch only writes default routes through discovered Gateways and never touches addressing. Prefix-delegation changes belong to the network manager, such as systemd-networkd.
