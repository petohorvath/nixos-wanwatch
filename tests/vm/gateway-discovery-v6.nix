/*
  gateway-discovery-v6 — IPv6 counterpart of gateway-discovery,
  covering the v6 route subscription, decoding, and Apply branch on a
  real packet path. The test driver assigns only v4 addresses, so
  both nodes declare their fd00:1::/64 addresses through networkd.
*/
{
  pkgs,
  nixosModule,
}:

pkgs.testers.runNixOSTest {
  name = "wanwatch-gateway-discovery-v6";

  # isp (fd00:1::1) is the next hop that router (fd00:1::2) learns.
  nodes.isp =
    { lib, ... }:
    {
      virtualisation.vlans = [ 1 ];
      networking.firewall.enable = lib.mkForce false;
      networking.useNetworkd = true;
      systemd.network.networks."01-eth1" = lib.mkForce {
        matchConfig.Name = "eth1";
        address = [ "fd00:1::1/64" ];
        networkConfig.IPv6AcceptRA = false;
        linkConfig.RequiredForOnline = "no";
      };
    };

  nodes.router =
    { lib, ... }:
    {
      imports = [ nixosModule ];
      virtualisation.vlans = [ 1 ];
      networking.firewall.enable = lib.mkForce false;

      # Replace the driver's networkd config with a v6 Gateway= that
      # the kernel installs in the main RIB.
      networking.useNetworkd = true;
      systemd.network.networks."01-eth1" = lib.mkForce {
        matchConfig.Name = "eth1";
        address = [ "fd00:1::2/64" ];
        networkConfig.Gateway = "fd00:1::1";
        networkConfig.IPv6AcceptRA = false;
        linkConfig.RequiredForOnline = "no";
      };

      environment.systemPackages = [
        pkgs.jq
        pkgs.iproute2
      ];

      services.wanwatch = {
        enable = true;
        wans.uplink = {
          interface = "eth1";
          # The default pointToPoint = false discovers the gateway.
          probe = {
            targets.v6 = [ "fd00:1::1" ];
            intervalMs = 600000;
            timeoutMs = 30000;
            hysteresis = {
              consecutiveDown = 10;
              consecutiveUp = 10;
            };
          };
        };
        groups.home = {
          members = [
            {
              wan = "uplink";
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

    start_all()
    isp.wait_for_unit("multi-user.target")
    router.wait_for_unit("wanwatch.service")
    router.wait_for_unit("systemd-networkd.service")

    # networkd leaves accept_dad=1, so disable DAD imperatively to
    # skip the one-second tentative window racing route observation.
    isp.succeed("sysctl -w net.ipv6.conf.eth1.accept_dad=0")
    router.succeed("sysctl -w net.ipv6.conf.eth1.accept_dad=0")

    router.succeed("ip link set eth1 up")
    isp.succeed("ip link set eth1 up")

    # networkd must first install the main-table default to discover.
    observe.wait_default_route(
        "v6", "eth1", gateway="fd00:1::1", timeout=15
    )

    # State must publish the discovered next-hop under schema 1.
    observe.wait_gateway("uplink", "v6", "fd00:1::1")

    # Verify the gateway Apply path in the Group's kernel table.
    observe.wait_default_route(
        "v6", "eth1", group="home", gateway="fd00:1::1", timeout=15
    )
  '';
}
