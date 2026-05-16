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
      networking = {
        useNetworkd = true;
        useDHCP = false;
        firewall.enable = lib.mkForce false;
        nftables.enable = true;
        nftzones.enable = true;
        nftzones.tables.fw = {
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

      environment.systemPackages = [
        pkgs.jq
        pkgs.iproute2
        pkgs.nftables
      ];

      services.wanwatch = {
        enable = true;
        wans.primary = {
          interface = "wan0";
          pointToPoint = true;
          probe = {
            targets.v4 = [ "192.0.2.1" ];
            intervalMs = 600000;
            timeoutMs = 30000;
            hysteresis = {
              consecutiveDown = 10;
              consecutiveUp = 10;
            };
          };
        };
        groups.home-uplink = {
          members = [
            {
              wan = "primary";
              priority = 1;
            }
          ];
          mark = 1000;
          table = 1000;
        };
      };
    };

  testScript = ''
    import json


    router.wait_for_unit("wanwatch.service")
    router.wait_for_unit("nftables.service")
    router.wait_for_unit("systemd-networkd.service")
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
    #    nft pretty-prints marks zero-padded to 8 hex digits
    #    (e.g. 6320 → "0x000018b0"), so match on the int value
    #    after stripping the prefix + leading zeros rather than
    #    on a hand-rolled hex string.
    ruleset = router.succeed("nft list ruleset")
    import re

    found = any(
        int(m.group(1), 16) == mark
        for m in re.finditer(r"meta mark set 0x([0-9a-fA-F]+)", ruleset)
    ) or any(
        int(m.group(1)) == mark
        for m in re.finditer(r"meta mark set (\d+)\b", ruleset)
    )
    assert found, (
        f"nftables ruleset missing mark {mark} ({hex(mark)}):\n{ruleset}"
    )

    # 2. The daemon installed the fwmark policy-routing rule for
    # both families (PLAN §6.1 step 2). `ip rule show fwmark X`
    # filtering proved brittle across iproute2 versions (newer
    # releases want `fwmark X/MASK`); list the full set and grep
    # the printed mark instead.
    #
    # Poll defensively: bootstrap → EnsureRule → first state.json
    # publish → sd_notify READY, so `wait_for_unit` SHOULD gate
    # these — but a future bootstrap-order regression would silently
    # re-introduce a race. Cheap on the happy path. Matches
    # smoke.nix.
    def wait_for_fwmark_rule(family_flag, mark, timeout=10):
        pattern = f"fwmark 0x{mark:x}|fwmark 0x{mark:08x}"
        router.wait_until_succeeds(
            f"ip {family_flag} rule show | grep -Eq '{pattern}'",
            timeout=timeout,
        )


    wait_for_fwmark_rule("-4", mark)
    wait_for_fwmark_rule("-6", mark)

    # 3. The daemon wrote the default route into the group's table
    # — a scope-link route out of wan0 (point-to-point, no gateway).
    # The route lands after the daemon observes the carrier-up event
    # we triggered above and the cold-start Decision pipeline
    # commits; under runner CPU pressure (e.g. when neighbouring
    # heavy VM tests are scheduled on the same host) that takes
    # longer than the four assertions above. Poll explicitly rather
    # than racing the kernel's empty-FIB-table error.
    router.wait_until_succeeds(
        f"ip -4 route show table {table} | grep -q ' dev wan0'", timeout=15
    )
    route = router.succeed(f"ip -4 route show table {table}")
    assert "wan0" in route and "via" not in route, (
        f"table {table} default route mismatch (want scope-link via wan0):\n{route}"
    )
  '';
}
