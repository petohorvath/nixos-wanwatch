/*
  failover-dual-stack — both v4 and v6 gateways per WAN. Carrier
  down on primary triggers the switch; both family routes in the
  group's table update atomically (one Decision, two route writes).
*/
{
  pkgs,
  nixosModule,
}:

pkgs.testers.runNixOSTest {
  name = "wanwatch-failover-dual-stack";

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
          address = [
            "192.0.2.10/24"
            "2001:db8::10/64"
          ];
        };
        "20-wan1" = {
          matchConfig.Name = "wan1";
          networkConfig.LinkLocalAddressing = "ipv6";
          linkConfig.RequiredForOnline = "no";
          address = [
            "100.64.0.10/24"
            "2001:db8:1::10/64"
          ];
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
            gateways = {
              v4 = "192.0.2.1";
              v6 = "2001:db8::1";
            };
            probe = {
              targets = [
                "192.0.2.1"
                "2001:db8::1"
              ];
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
            gateways = {
              v4 = "100.64.0.1";
              v6 = "2001:db8:1::1";
            };
            probe = {
              targets = [
                "100.64.0.1"
                "2001:db8:1::1"
              ];
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
    v4 = router.succeed(f"ip -4 route show table {table}")
    v6 = router.succeed(f"ip -6 route show table {table}")
    assert "192.0.2.1" in v4 and "wan0" in v4, f"initial v4 table:\n{v4}"
    assert "2001:db8::1" in v6 and "wan0" in v6, f"initial v6 table:\n{v6}"

    if router.execute("ip link set wan0 carrier off")[0] != 0:
        router.succeed("ip link set wan0 down")

    wait_for_active(router, "backup")

    v4 = router.succeed(f"ip -4 route show table {table}")
    v6 = router.succeed(f"ip -6 route show table {table}")
    assert "100.64.0.1" in v4 and "wan1" in v4, f"failover v4 table:\n{v4}"
    assert "2001:db8:1::1" in v6 and "wan1" in v6, f"failover v6 table:\n{v6}"
  '';
}
