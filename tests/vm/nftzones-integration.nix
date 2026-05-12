/*
  nftzones-integration — verifies PLAN §6.1: wanwatch publishes
  `services.wanwatch.marks.<group>`; an nftzones table references
  that value in a sroute rule; the compiled nftables ruleset on
  the live kernel contains the same mark; the daemon's fwmark
  policy rule + table default route are in place, atomically
  followed by route writes on failover (covered by failover-*).

  Single-node — the LAN client + multi-ISP traffic-level
  topology PLAN §9.4 describes is best left to a dedicated
  end-to-end scenario once the topology pieces (DHCP, NAT, real
  TCP responders) settle. This scenario focuses on the
  rule-installation contract; failover-* already covers route
  rewrites under switch.
*/
{
  pkgs,
  nixosModule,
  nftzonesModule,
  nftypes,
}:

let
  inherit (nftypes.dsl) mangle;
  inherit (nftypes.dsl.fields) meta;
in
pkgs.testers.runNixOSTest {
  name = "wanwatch-nftzones-integration";

  nodes.router =
    { config, lib, ... }:
    {
      imports = [
        nixosModule
        nftzonesModule
      ];

      boot.kernelModules = [ "dummy" ];

      systemd.network.netdevs = {
        "10-wan0".netdevConfig = {
          Kind = "dummy";
          Name = "wan0";
        };
        "10-lan0".netdevConfig = {
          Kind = "dummy";
          Name = "lan0";
        };
      };
      systemd.network.networks = {
        "20-wan0" = {
          matchConfig.Name = "wan0";
          networkConfig.LinkLocalAddressing = "no";
          linkConfig.RequiredForOnline = "no";
          address = [ "192.0.2.10/24" ];
        };
        "20-lan0" = {
          matchConfig.Name = "lan0";
          networkConfig.LinkLocalAddressing = "no";
          linkConfig.RequiredForOnline = "no";
          address = [ "192.168.10.1/24" ];
        };
      };
      networking.useNetworkd = true;
      networking.useDHCP = false;
      networking.firewall.enable = lib.mkForce false;

      environment.systemPackages = [
        pkgs.jq
        pkgs.iproute2
        pkgs.nftables
      ];

      services.wanwatch = {
        enable = true;
        wans.primary = {
          interface = "wan0";
          gateways.v4 = "192.0.2.1";
          probe = {
            targets = [ "192.0.2.1" ];
            intervalMs = 600000;
            timeoutMs = 30000;
            hysteresis = {
              consecutiveDown = 10;
              consecutiveUp = 10;
            };
          };
        };
        groups.home-uplink.members = [
          {
            wan = "primary";
            priority = 1;
          }
        ];
      };

      networking.nftables.enable = true;
      networking.nftzones.tables.fw = {
        family = "inet";
        zones = {
          lan.interfaces = [ "lan0" ];
          wan-home.interfaces = [ "wan0" ];
        };
        # Stamp the wanwatch-allocated mark on LAN-sourced
        # forwarded traffic. PLAN §6.2's canonical example.
        sroutes.lan-via-home = {
          from = [ "lan" ];
          rule = [ (mangle meta.mark config.services.wanwatch.marks.home-uplink) ];
        };
      };
    };

  testScript = ''
    import json


    router.wait_for_unit("wanwatch.service")
    router.wait_for_unit("nftables.service")
    router.succeed("ip link set wan0 up")
    router.succeed("ip link set lan0 up")

    # The mark + table values the module exposed as outputs.
    cfg = json.loads(router.succeed("cat /etc/wanwatch/config.json"))
    grp = cfg["groups"]["home-uplink"]
    mark = grp["mark"]
    table = grp["table"]
    assert isinstance(mark, int) and mark > 0, f"mark = {mark!r}"
    assert isinstance(table, int) and table > 0, f"table = {table!r}"

    # 1. The mark value reached the compiled nftables ruleset.
    ruleset = router.succeed("nft list ruleset")
    assert f"meta mark set 0x{mark:x}" in ruleset or f"meta mark set {mark}" in ruleset, (
        f"nftables ruleset missing mark {mark} ({hex(mark)}):\n{ruleset}"
    )

    # 2. The daemon installed the fwmark policy-routing rule for
    # both families (PLAN §6.1 step 2).
    v4_rules = router.succeed(f"ip -4 rule show fwmark 0x{mark:x}")
    assert v4_rules.strip(), f"v4 ip rule for fwmark {mark} missing"
    v6_rules = router.succeed(f"ip -6 rule show fwmark 0x{mark:x}")
    assert v6_rules.strip(), f"v6 ip rule for fwmark {mark} missing"

    # 3. The daemon wrote the default route into the group's table
    # via primary's gateway (cold-start carrier-driven Selection).
    route = router.succeed(f"ip -4 route show table {table}")
    assert "192.0.2.1" in route and "wan0" in route, (
        f"table {table} default route mismatch:\n{route}"
    )
  '';
}
