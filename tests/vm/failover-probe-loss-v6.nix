/*
  failover-probe-loss-v6 — IPv6 counterpart of failover-probe-loss.
  Same VLAN-on-the-test-driver topology and same netem-driven loss
  injection, but the probes ride ICMPv6 over manually-assigned ULA
  addresses (the test driver only auto-IPs v4).

  Exists because the other v6 scenarios (failover-v6, recovery,
  failover-dual-stack) all use dummy interfaces with
  intervalMs=600000 — they are carrier-driven, so they never
  exercise the v6 probe + threshold + hysteresis chain on a real
  packet path. Without this scenario, a regression in the
  golang.org/x/net/icmp v6 socket bind, the v6 RTT statistics, or
  the v6 branch of combineFamilies would slip through CI.

  Topology mirrors failover-probe-loss.nix:

    isp1 ─── VLAN 1 ─── eth1 ┐
                              ├── router
    isp2 ─── VLAN 2 ─── eth2 ┘

  v6 plan (added imperatively after boot — the test driver does not
  auto-assign v6):

    isp1   eth1   fd00:1::1/64
    isp2   eth1   fd00:2::1/64
    router eth1   fd00:1::3/64
    router eth2   fd00:2::3/64

  Sequence: identical to failover-probe-loss but on v6 — cook both
  probes healthy, netem 100 % loss on eth1, assert switch to backup
  with reason="health", clear netem, assert primary takes back over.
*/
{
  pkgs,
  nixosModule,
}:

pkgs.testers.runNixOSTest {
  name = "wanwatch-failover-probe-loss-v6";

  nodes = {
    isp1 =
      { lib, ... }:
      {
        virtualisation.vlans = [ 1 ];
        networking.firewall.enable = lib.mkForce false;
      };
    isp2 =
      { lib, ... }:
      {
        virtualisation.vlans = [ 2 ];
        networking.firewall.enable = lib.mkForce false;
      };

    router =
      { lib, ... }:
      {
        imports = [ nixosModule ];
        virtualisation.vlans = [
          1
          2
        ];
        networking.firewall.enable = lib.mkForce false;

        environment.systemPackages = [
          pkgs.jq
          pkgs.iproute2
        ];

        services.wanwatch = {
          enable = true;
          wans = {
            primary = {
              interface = "eth1";
              pointToPoint = true;
              probe = {
                targets.v6 = [ "fd00:1::1" ];
                intervalMs = 200;
                timeoutMs = 100;
                windowSize = 4;
                thresholds = {
                  lossPctDown = 25;
                  lossPctUp = 5;
                  rttMsDown = 5000;
                  rttMsUp = 4000;
                };
                hysteresis = {
                  consecutiveDown = 2;
                  consecutiveUp = 2;
                };
              };
            };
            backup = {
              interface = "eth2";
              pointToPoint = true;
              probe = {
                targets.v6 = [ "fd00:2::1" ];
                intervalMs = 200;
                timeoutMs = 100;
                windowSize = 4;
                thresholds = {
                  lossPctDown = 25;
                  lossPctUp = 5;
                  rttMsDown = 5000;
                  rttMsUp = 4000;
                };
                hysteresis = {
                  consecutiveDown = 2;
                  consecutiveUp = 2;
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
  };

  testScript = ''
    import json


    def wait_for_active(router, want, timeout=10):
        for _ in range(timeout * 10):
            out = router.succeed("cat /run/wanwatch/state.json")
            active = json.loads(out)["groups"]["home-uplink"]["active"]
            if active == want:
                return
            router.execute("sleep 0.1")
        raise AssertionError(
            f"active never reached {want!r}; last state =\n{out}"
        )


    def wait_for_wan_healthy(router, wan, timeout=15):
        for _ in range(timeout * 10):
            out = router.succeed("cat /run/wanwatch/state.json")
            if json.loads(out)["wans"][wan]["healthy"]:
                return
            router.execute("sleep 0.1")
        raise AssertionError(
            f"wan {wan!r} never became probe-healthy; last state =\n{out}"
        )


    def scrape(router):
        return router.succeed(
            "${pkgs.curl}/bin/curl -s --unix-socket "
            "/run/wanwatch/metrics.sock http://wanwatch/metrics"
        )


    def metric(body, series):
        prefix = series + " "
        for line in body.splitlines():
            if line.startswith(prefix):
                return float(line[len(prefix):])
        return 0.0


    def health_decisions(router, group):
        series = (
            'wanwatch_group_decisions_total{group="'
            + group + '",reason="health"}'
        )
        return metric(scrape(router), series)


    start_all()
    isp1.wait_for_unit("multi-user.target")
    isp2.wait_for_unit("multi-user.target")
    router.wait_for_unit("wanwatch.service")

    # The test driver only auto-IPs v4; add ULA addresses on each
    # VLAN endpoint so v6 ICMP probes have a real path. Disable DAD
    # for instant availability — a stock duplicate-address-detection
    # delay (1s) would race the wait_until_succeeds below on slow
    # runners.
    isp1.succeed("sysctl -w net.ipv6.conf.eth1.accept_dad=0")
    isp2.succeed("sysctl -w net.ipv6.conf.eth1.accept_dad=0")
    router.succeed("sysctl -w net.ipv6.conf.eth1.accept_dad=0")
    router.succeed("sysctl -w net.ipv6.conf.eth2.accept_dad=0")

    isp1.succeed("ip -6 addr add fd00:1::1/64 dev eth1")
    isp2.succeed("ip -6 addr add fd00:2::1/64 dev eth1")
    router.succeed("ip -6 addr add fd00:1::3/64 dev eth1")
    router.succeed("ip -6 addr add fd00:2::3/64 dev eth2")

    router.succeed("ip link set eth1 up")
    router.succeed("ip link set eth2 up")

    # Pin L3 reachability before any probe assertion (see the v4
    # scenario for the long-form rationale — same race here on v6).
    router.wait_until_succeeds("ping -6 -c 1 -W 1 fd00:1::1", timeout=30)
    router.wait_until_succeeds("ping -6 -c 1 -W 1 fd00:2::1", timeout=30)

    # 1. Primary wins on cold-start carrier health.
    wait_for_active(router, "primary")

    # 2. Pre-injection: both WANs must be probe-healthy on v6, not
    #    just carrier-up — otherwise step 4's "failover to backup"
    #    races a backup whose v6 Window hasn't cooked yet.
    wait_for_wan_healthy(router, "primary")
    wait_for_wan_healthy(router, "backup")

    before = health_decisions(router, "home-uplink")

    # 3. 100 % packet loss on the primary uplink — netem at the
    #    egress qdisc drops every outbound packet (family-agnostic),
    #    so v6 ICMP echoes never reach isp1.
    router.succeed("tc qdisc add dev eth1 root netem loss 100%")

    # 4. Failover within ~consecutiveDown * intervalMs + apply
    #    overhead; 10s is generous.
    wait_for_active(router, "backup")

    # 5. The Decision was probe-driven, not carrier-driven.
    after = health_decisions(router, "home-uplink")
    assert after > before, (
        f"health-reason decisions did not advance: {before} → {after}\n"
        f"(failover may have fired via carrier instead — that would "
        f"indicate a regression in the v6 probe path)"
    )

    # state.json should reflect the per-family observation: v6
    # lossRatio at or near 1.0, primary's v6 family healthy=false.
    state = json.loads(router.succeed("cat /run/wanwatch/state.json"))
    v6 = state["wans"]["primary"]["families"]["v6"]
    assert v6["healthy"] is False, (
        f"primary v6 still healthy after 100% loss: {v6}"
    )
    assert v6["lossRatio"] >= 0.5, (
        f"primary v6 lossRatio = {v6['lossRatio']}, want ≥ 0.5"
    )

    # 6. Recovery: clear netem and assert primary takes back over.
    #    Pins the v6 consecutiveUp arm of the hysteresis state
    #    machine — the gap PLAN §9.4 promises recovery.nix covers,
    #    but recovery.nix is carrier-driven and v4-only.
    router.succeed("tc qdisc del dev eth1 root")
    wait_for_active(router, "primary")

    state = json.loads(router.succeed("cat /run/wanwatch/state.json"))
    v6 = state["wans"]["primary"]["families"]["v6"]
    assert v6["healthy"] is True, (
        f"primary v6 didn't recover after netem cleared: {v6}"
    )
    assert v6["lossRatio"] <= 0.10, (
        f"primary v6 lossRatio = {v6['lossRatio']}, want ≤ 0.10 post-recovery"
    )
  '';
}
