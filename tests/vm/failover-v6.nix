/*
  failover-v6 — IPv6 counterpart of failover-v4. Same single-node
  topology, same carrier-driven cold-start path; the assertions
  walk the v6 RIB (`ip -6 route show table <T>`) instead of v4.
*/
{
  pkgs,
  nixosModule,
}:

pkgs.testers.runNixOSTest {
  name = "wanwatch-failover-v6";

  nodes.router =
    { lib, ... }:
    {
      imports = [ nixosModule ];

      boot.kernelModules = [ "dummy" ];

      systemd.network.netdevs = {
        "10-wan0" = {
          netdevConfig = {
            Kind = "dummy";
            Name = "wan0";
          };
        };
        "10-wan1" = {
          netdevConfig = {
            Kind = "dummy";
            Name = "wan1";
          };
        };
      };
      systemd.network.networks = {
        "20-wan0" = {
          matchConfig.Name = "wan0";
          networkConfig.LinkLocalAddressing = "ipv6";
          linkConfig.RequiredForOnline = "no";
          address = [ "2001:db8::10/64" ];
        };
        "20-wan1" = {
          matchConfig.Name = "wan1";
          networkConfig.LinkLocalAddressing = "ipv6";
          linkConfig.RequiredForOnline = "no";
          address = [ "2001:db8:1::10/64" ];
        };
      };
      networking = {
        useNetworkd = true;
        useDHCP = false;
        firewall.enable = lib.mkForce false;
      };

      environment.systemPackages = [
        pkgs.jq
        pkgs.iproute2
      ];

      services.wanwatch = {
        enable = true;
        wans = {
          primary = {
            interface = "wan0";
            pointToPoint = true;
            probe = {
              targets.v6 = [ "2001:db8::1" ];
              intervalMs = 600000;
              timeoutMs = 30000;
              hysteresis = {
                consecutiveDown = 10;
                consecutiveUp = 10;
              };
            };
          };
          backup = {
            interface = "wan1";
            pointToPoint = true;
            probe = {
              targets.v6 = [ "2001:db8:1::1" ];
              intervalMs = 600000;
              timeoutMs = 30000;
              hysteresis = {
                consecutiveDown = 10;
                consecutiveUp = 10;
              };
            };
          };
        };
        groups.home-uplink = {
          members = [
            {
              wan = "primary";
              priority = 1;
            }
            {
              wan = "backup";
              priority = 2;
            }
          ];
          mark = 1000;
          table = 1000;
        };
      };
    };

  testScript = (builtins.readFile ./observation.py) + ''
    observe = Observation(router, curl="${pkgs.curl}/bin/curl")

    router.wait_for_unit("wanwatch.service")
    router.wait_for_unit("systemd-networkd.service")

    router.succeed("ip link set wan0 up")
    router.succeed("ip link set wan1 up")

    observe.wait_active("home-uplink", "primary")

    # Verify the direct default route in the kernel, allowing networkd
    # reconfiguration to settle after the link comes up.
    observe.wait_default_route("v6", "wan0", group="home-uplink")

    if router.execute("ip link set wan0 carrier off")[0] != 0:
        router.succeed("ip link set wan0 down")

    observe.wait_active("home-uplink", "backup")

    observe.wait_default_route("v6", "wan1", group="home-uplink")
  '';
}
