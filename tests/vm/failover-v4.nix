/*
  failover-v4 — boot a single-node router with two v4-only WANs
  (dummy0, dummy1), assert the daemon picks the highest-priority
  member, induce carrier-down on the primary, and assert the
  Selection (plus the default route in the group's table) switches
  to the backup.

  The daemon's probe targets here are unreachable on purpose —
  dummy interfaces drop transmitted packets. PLAN §8's
  cold-start carrier-only health (commit "Cold-start health
  follows carrier alone") is what lets the test fire a Decision
  without working ICMP. Long probe interval keeps the cooked
  verdict from kicking in during the test window.
*/
{
  pkgs,
  nixosModule,
}:

pkgs.testers.runNixOSTest {
  name = "wanwatch-failover-v4";

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
              # Stretch the probe loop so cooked verdicts don't
              # land during the test — carrier alone drives the
              # Decision under PLAN §8 cold-start.
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

    router.wait_for_unit("wanwatch.service")
    router.wait_for_unit("systemd-networkd.service")

    # The networkd unit puts dummies "up" — bring the carrier on
    # explicitly so rtnetlink fires the events the daemon needs.
    router.succeed("ip link set wan0 up")
    router.succeed("ip link set wan1 up")

    # 1. Initial Selection: primary (lowest priority among
    #    carrier-up members). Cold-start health is carrier-only.
    observe.wait_active("home-uplink", "primary")

    # 2. Verify the direct default route in the kernel, allowing networkd
    # reconfiguration to settle after the link comes up.
    observe.wait_default_route("v4", "wan0", group="home-uplink")

    # 3. Induce carrier-down on the primary. ip link set <if>
    #    carrier off is supported on dummy in modern kernels;
    #    fall back to `down` if the kernel rejects it.
    if router.execute("ip link set wan0 carrier off")[0] != 0:
        router.succeed("ip link set wan0 down")

    # 4. Daemon switches to backup. Decision is rtnl-driven so
    #    the switch should happen within rtnl propagation +
    #    apply latency — single-digit seconds.
    observe.wait_active("home-uplink", "backup")

    # 5. The default route must now use the backup interface.
    observe.wait_default_route("v4", "wan1", group="home-uplink")

    # 6. The carrier-reason counter must appear on the live endpoint.
    observe.wait_decisions("home-uplink", "carrier", minimum=1)
  '';
}
