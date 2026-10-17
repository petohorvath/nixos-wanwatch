/*
  nftables-steering — the README's "Steer traffic into a Group" example
  on a live kernel. A plain `networking.nftables` table marks traffic
  from `br-lan` with `services.wanwatch.marks.<group>`; the ruleset
  must carry that mark, and a lookup with the mark must resolve through
  the Group's table while an unmarked lookup finds no route. Single
  node; nftzones-integration covers the nftzones variant.
*/
{ inputs, pkgs }:

let
  # The README example, verbatim; keep the two in sync.
  steering =
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
    };
in
pkgs.testers.runNixOSTest {
  name = "wanwatch-nftables-steering";

  nodes.router =
    { lib, pkgs, ... }:
    {
      imports = [
        inputs.self.nixosModules.default
        steering
      ];

      # Dummy interfaces stand in for the WAN and the README's LAN
      # bridge, so the rule's `iifname` matches a real interface name.
      boot.kernelModules = [ "dummy" ];
      systemd.network.netdevs = {
        "10-wan0".netdevConfig = {
          Kind = "dummy";
          Name = "wan0";
        };
        "10-br-lan".netdevConfig = {
          Kind = "dummy";
          Name = "br-lan";
        };
      };
      systemd.network.networks = {
        "20-wan0" = {
          matchConfig.Name = "wan0";
          networkConfig.LinkLocalAddressing = "no";
          linkConfig.RequiredForOnline = "no";
          address = [ "192.0.2.10/24" ];
        };
        "20-br-lan" = {
          matchConfig.Name = "br-lan";
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
      };

      environment.systemPackages = [
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
          members = {
            primary.priority = 1;
          };
          mark = 1000;
          table = 1000;
        };
      };
    };

  testScript = (builtins.readFile ./observation.py) + ''
    observe = Observation(router)

    import json
    import re


    router.wait_for_unit("wanwatch.service")
    router.wait_for_unit("nftables.service")
    router.wait_for_unit("systemd-networkd.service")
    router.succeed("ip link set wan0 up")
    router.succeed("ip link set br-lan up")

    rendered = json.loads(router.succeed("cat /etc/wanwatch/config.json"))
    mark = rendered["groups"]["home-uplink"]["mark"]

    # 1. The README rule loaded with the Group's mark. nft zero-pads hex
    #    marks, so compare parsed integers.
    chain = router.succeed("nft list chain inet wanwatch-steering prerouting")
    found = any(
        int(match.group(1), 16) == mark
        for match in re.finditer(
            r'iifname "br-lan" meta mark set 0x([0-9a-fA-F]+)', chain
        )
    )
    assert found, f"prerouting chain missing mark {mark} ({hex(mark)}):\n{chain}"

    # 2. The carrier-up Decision installs the Group's default route.
    observe.wait_default_route("v4", "wan0", group="home-uplink", timeout=15)

    # 3. The mark alone selects the Group's table: the main table has no
    #    default route, so only the marked lookup resolves.
    router.wait_until_succeeds(
        f"ip -4 route get 198.51.100.1 mark {mark} | grep -q 'dev wan0'",
        timeout=10,
    )
    router.fail("ip -4 route get 198.51.100.1")
  '';
}
