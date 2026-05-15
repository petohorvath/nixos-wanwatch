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
        """Poll state.json until groups.home-uplink.active == want."""
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

    # The networkd unit puts dummies "up" — bring the carrier on
    # explicitly so rtnetlink fires the events the daemon needs.
    router.succeed("ip link set wan0 up")
    router.succeed("ip link set wan1 up")

    # 1. Initial Selection: primary (lowest priority among
    #    carrier-up members). Cold-start health is carrier-only.
    wait_for_active(router, "primary")

    # 2. Default route in the group's table is a scope-link route
    #    out of wan0 (point-to-point: no gateway, no `via`).
    #
    #    wait_for_active fires the moment state.json shows
    #    active=primary, but `ip link set wan0 up` also kicks
    #    systemd-networkd into reconfiguring wan0; the kernel-side
    #    route can briefly disappear and re-appear around that
    #    reconfigure. Poll for the route to be in place rather
    #    than racing networkd.
    table = router.succeed(
        "jq -r '.groups.\"home-uplink\".table' /etc/wanwatch/config.json"
    ).strip()
    router.wait_until_succeeds(
        f"ip -4 route show table {table} | grep -q ' dev wan0'", timeout=10
    )
    route = router.succeed(f"ip -4 route show table {table}")
    assert "wan0" in route and "via" not in route, (
        f"initial table {table} route mismatch (want scope-link via wan0):\n{route}"
    )

    # 3. Induce carrier-down on the primary. ip link set <if>
    #    carrier off is supported on dummy in modern kernels;
    #    fall back to `down` if the kernel rejects it.
    if router.execute("ip link set wan0 carrier off")[0] != 0:
        router.succeed("ip link set wan0 down")

    # 4. Daemon switches to backup. Decision is rtnl-driven so
    #    the switch should happen within rtnl propagation +
    #    apply latency — single-digit seconds.
    wait_for_active(router, "backup")

    # 5. New default route is a scope-link route out of wan1.
    router.wait_until_succeeds(
        f"ip -4 route show table {table} | grep -q ' dev wan1'", timeout=10
    )
    route = router.succeed(f"ip -4 route show table {table}")
    assert "wan1" in route and "via" not in route, (
        f"failover-table {table} route mismatch (want scope-link via wan1):\n{route}"
    )

    # 6. wanwatch_group_decisions_total has incremented for the
    #    "carrier" reason — proves the metrics path also reacted.
    body = router.succeed(
        "${pkgs.curl}/bin/curl -s --unix-socket /run/wanwatch/metrics.sock "
        "http://wanwatch/metrics"
    )
    assert 'wanwatch_group_decisions_total{group="home-uplink",reason="carrier"}' in body, (
        "decisions counter missing the 'carrier' reason; scrape:\n" + body
    )
  '';
}
