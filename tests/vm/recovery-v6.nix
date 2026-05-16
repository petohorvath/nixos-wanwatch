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


    def carrier(router, iface, state):
        if router.execute(f"ip link set {iface} carrier {state}")[0] != 0:
            # Fallback for kernels without `carrier` on dummy.
            router.succeed(
                f"ip link set {iface} {'up' if state == 'on' else 'down'}"
            )


    def wait_for_v6_default(router, table, iface, timeout=10):
        """Block until table <table> in the v6 RIB has a scope-link
        default route out of <iface>. Asserting on the kernel rather
        than state.json bounds the daemon's apply path, not just its
        state-publication path."""
        router.wait_until_succeeds(
            f"ip -6 route show table {table} | grep -q 'default dev {iface}'",
            timeout=timeout,
        )


    router.wait_for_unit("wanwatch.service")
    router.wait_for_unit("systemd-networkd.service")

    table = router.succeed(
        "jq -r '.groups.\"home-uplink\".table' /etc/wanwatch/config.json"
    ).strip()

    router.succeed("ip link set wan0 up")
    router.succeed("ip link set wan1 up")
    wait_for_active(router, "primary")
    wait_for_v6_default(router, table, "wan0")

    # Failover arm: carrier loss on primary.
    carrier(router, "wan0", "off")
    wait_for_active(router, "backup")
    wait_for_v6_default(router, table, "wan1")

    # The recovery arm: bring carrier back on. With cold-start
    # carrier-only health, restoring carrier on the higher-priority
    # member should flip the Selection back without waiting for
    # any probe sample. The v6 default route in the per-group table
    # must follow — a regression in apply.WriteDefault's v6 branch
    # or in the daemon's per-member route cleanup would let
    # state.json report active=primary while the kernel kept the
    # wan1 route, silently breaking forwarding.
    carrier(router, "wan0", "on")
    wait_for_active(router, "primary")
    wait_for_v6_default(router, table, "wan0")

    # Decisions counter should show at least two carrier-driven
    # changes (down→backup, up→primary). wait_for_active proves
    # state.json updated; this poll proves the metric Inc on the
    # second Decision actually surfaced. awk note: `exit N` inside
    # a pattern action transfers control to END, whose `exit 1`
    # would override — use a flag and let the END block be the
    # only exit point.
    router.wait_until_succeeds(
        "${pkgs.curl}/bin/curl -s --unix-socket /run/wanwatch/metrics.sock "
        "http://wanwatch/metrics | "
        "awk '/^wanwatch_group_decisions_total\\{group=\"home-uplink\",reason=\"carrier\"\\}/ "
        "{ if ($2+0 >= 2) found=1 } END { exit !found }'",
        timeout=10,
    )
  '';
}
