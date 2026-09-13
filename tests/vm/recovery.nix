/*
  recovery — primary's carrier goes down then comes back. The
  daemon must switch out of primary on carrier-loss AND switch
  back when carrier returns. Without the second arm, a "blip" on
  the better link would leave traffic permanently on backup.
*/
{
  pkgs,
  nixosModule,
}:

pkgs.testers.runNixOSTest {
  name = "wanwatch-recovery";

  nodes.router =
    { lib, ... }:
    {
      imports = [ nixosModule ];

      boot.kernelModules = [ "dummy" ];

      systemd.network.netdevs = {
        "10-wan0".netdevConfig = {
          Kind = "dummy";
          Name = "wan0";
        };
        "10-wan1".netdevConfig = {
          Kind = "dummy";
          Name = "wan1";
        };
      };
      systemd.network.networks = {
        "20-wan0" = {
          matchConfig.Name = "wan0";
          networkConfig.LinkLocalAddressing = "no";
          linkConfig.RequiredForOnline = "no";
          address = [ "192.0.2.10/24" ];
        };
        "20-wan1" = {
          matchConfig.Name = "wan1";
          networkConfig.LinkLocalAddressing = "no";
          linkConfig.RequiredForOnline = "no";
          address = [ "100.64.0.10/24" ];
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
              targets.v4 = [ "192.0.2.1" ];
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
              targets.v4 = [ "100.64.0.1" ];
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

    def carrier(router, iface, state):
        if router.execute(f"ip link set {iface} carrier {state}")[0] != 0:
            # Fallback for kernels without `carrier` on dummy.
            router.succeed(
                f"ip link set {iface} {'up' if state == 'on' else 'down'}"
            )


    router.wait_for_unit("wanwatch.service")
    router.wait_for_unit("systemd-networkd.service")

    router.succeed("ip link set wan0 up")
    router.succeed("ip link set wan1 up")
    observe.wait_active("home-uplink", "primary")

    carrier(router, "wan0", "off")
    observe.wait_active("home-uplink", "backup")

    # The recovery arm: bring carrier back on. With cold-start
    # carrier-only health, restoring carrier on the higher-priority
    # member should flip the Selection back without waiting for
    # any probe sample.
    carrier(router, "wan0", "on")
    observe.wait_active("home-uplink", "primary")

    # State publication and the live counter can become visible separately.
    # Both carrier-driven changes (down→backup, up→primary) must be counted.
    observe.wait_decisions("home-uplink", "carrier", minimum=2)
  '';
}
