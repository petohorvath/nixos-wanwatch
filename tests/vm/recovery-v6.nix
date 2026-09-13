/*
  recovery-v6 — IPv6 counterpart of recovery. Audit gap G1: PLAN
  §9.4 promises "After primary recovers, switch back within
  consecutiveUp cycles in both families," but `recovery.nix` is
  carrier-driven + v4-only. Recovery on v6 currently appears only
  as a side-effect of failover-probe-loss-v6 Phase B; this
  scenario makes it a first-class focused test.

  Same single-node dummy-interface topology as recovery — probes
  are scheduled at intervalMs=600000 so they never fire within
  the test window; failover and recovery flow purely through
  carrier events. Adds explicit assertions on the v6 routing
  table at every transition so a regression in the daemon's
  per-family apply path (the v6 branch of WriteDefault, or the
  v6 RIB cleanup on member-out) surfaces immediately rather than
  hiding behind a state.json that says "active=X" without the
  kernel actually following.
*/
{
  pkgs,
  nixosModule,
}:

pkgs.testers.runNixOSTest {
  name = "wanwatch-recovery-v6";

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
    observe.wait_default_route("v6", "wan0", group="home-uplink")

    # Failover arm: carrier loss on primary.
    carrier(router, "wan0", "off")
    observe.wait_active("home-uplink", "backup")
    observe.wait_default_route("v6", "wan1", group="home-uplink")

    # The recovery arm: bring carrier back on. With cold-start
    # carrier-only health, restoring carrier on the higher-priority
    # member should flip the Selection back without waiting for
    # any probe sample. The v6 default route in the per-group table
    # must follow — a regression in apply.WriteDefault's v6 branch
    # or in the daemon's per-member route cleanup would let
    # state.json report active=primary while the kernel kept the
    # wan1 route, silently breaking forwarding.
    carrier(router, "wan0", "on")
    observe.wait_active("home-uplink", "primary")
    observe.wait_default_route("v6", "wan0", group="home-uplink")

    # State publication and the live counter can become visible separately.
    # Both carrier-driven changes (down→backup, up→primary) must be counted.
    observe.wait_decisions("home-uplink", "carrier", minimum=2)
  '';
}
