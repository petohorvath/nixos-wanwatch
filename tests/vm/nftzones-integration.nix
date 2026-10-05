/*
  nftzones-integration — the rule-installation contract of PLAN §6.1.
  An nftzones sroute references `services.wanwatch.marks.<group>`;
  the live nftables ruleset must contain that mark, and the daemon
  must install the fwmark rules and the Group's default route.
  Single node; failover-* covers route rewrites.
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
          # Mark LAN-sourced forwarded traffic for the Group (the
          # PLAN §6.2 example).
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

  testScript = (builtins.readFile ./observation.py) + ''
    observe = Observation(router, curl="${pkgs.curl}/bin/curl")

    import json
    import re


    router.wait_for_unit("wanwatch.service")
    router.wait_for_unit("nftables.service")
    router.wait_for_unit("systemd-networkd.service")
    router.succeed("ip link set wan0 up")
    router.succeed("ip link set lan0 up")

    # The mark and table values rendered from the module.
    rendered = json.loads(router.succeed("cat /etc/wanwatch/config.json"))
    group_config = rendered["groups"]["home-uplink"]
    mark = group_config["mark"]
    table = group_config["table"]
    assert isinstance(mark, int) and mark > 0, f"mark = {mark!r}"
    assert isinstance(table, int) and table > 0, f"table = {table!r}"

    # 1. The mark reached the compiled ruleset. nft zero-pads hex
    #    marks, so compare parsed integers.
    ruleset = router.succeed("nft list ruleset")
    found = any(
        int(match.group(1), 16) == mark
        for match in re.finditer(r"meta mark set 0x([0-9a-fA-F]+)", ruleset)
    ) or any(
        int(match.group(1)) == mark
        for match in re.finditer(r"meta mark set (\d+)\b", ruleset)
    )
    assert found, (
        f"nftables ruleset missing mark {mark} ({hex(mark)}):\n{ruleset}"
    )

    # 2. The daemon installed the fwmark rule for both families
    #    (PLAN §6.1). `ip rule show fwmark X` filtering varies across
    #    iproute2 releases, so grep the full listing. Poll as smoke.nix
    #    does in case bootstrap ordering regresses.
    def wait_for_fwmark_rule(family_flag, mark, timeout=10):
        pattern = f"fwmark 0x{mark:x}|fwmark 0x{mark:08x}"
        router.wait_until_succeeds(
            f"ip {family_flag} rule show | grep -Eq '{pattern}'",
            timeout=timeout,
        )


    wait_for_fwmark_rule("-4", mark)
    wait_for_fwmark_rule("-6", mark)

    # The carrier-up Decision installs the Group's default route; the
    # FIB table may be absent until then.
    observe.wait_default_route("v4", "wan0", group="home-uplink", timeout=15)
  '';
}
