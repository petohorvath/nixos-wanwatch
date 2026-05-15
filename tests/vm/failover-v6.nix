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
      networking.useNetworkd = true;
      networking.useDHCP = false;
      networking.firewall.enable = lib.mkForce false;

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
        groups.home-uplink.members = [
          {
            wan = "primary";
            priority = 1;
          }
          {
            wan = "backup";
            priority = 2;
          }
        ];
      };
    };

  testScript = ''
    import json


    def wait_for_active(router, want, timeout=15):
        for _ in range(timeout * 4):
            out = router.succeed("cat /run/wanwatch/state.json")
            active = json.loads(out)["groups"]["home-uplink"]["active"]
            if active == want:
                return
            router.execute("sleep 0.25")
        raise AssertionError(
            f"active never reached {want!r}; last state =\n{out}"
        )


    router.wait_for_unit("wanwatch.service")
    router.wait_for_unit("systemd-networkd.service")

    router.succeed("ip link set wan0 up")
    router.succeed("ip link set wan1 up")

    wait_for_active(router, "primary")

    table = router.succeed(
        "jq -r '.groups.\"home-uplink\".table' /etc/wanwatch/config.json"
    ).strip()
    route = router.succeed(f"ip -6 route show table {table}")
    assert "wan0" in route and "via" not in route, (
        f"initial v6 table {table} route mismatch (want scope-link via wan0):\n{route}"
    )

    if router.execute("ip link set wan0 carrier off")[0] != 0:
        router.succeed("ip link set wan0 down")

    wait_for_active(router, "backup")

    route = router.succeed(f"ip -6 route show table {table}")
    assert "wan1" in route and "via" not in route, (
        f"failover v6 table {table} route mismatch (want scope-link via wan1):\n{route}"
    )
  '';
}
