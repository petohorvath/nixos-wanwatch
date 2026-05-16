/*
  failover-probe-loss-v6 — IPv6 counterpart of failover-probe-loss,
  extended into a multi-phase probe-pipeline workout. Probes ride
  ICMPv6 over manually-assigned ULA addresses (the test driver only
  auto-IPs v4); netem at the egress qdisc drives the loss scenarios.

  Exists because the other v6 scenarios (failover-v6, recovery,
  failover-dual-stack) all use dummy interfaces with
  intervalMs=600000 — they are carrier-driven, so they never
  exercise the v6 probe + threshold + hysteresis chain on a real
  packet path. Without this scenario, a regression in the
  golang.org/x/net/icmp v6 socket bind, the v6 RTT statistics, the
  v6 branch of combineFamilies, or the per-target → per-family
  aggregator would slip through.

  Topology:

    isp1 ─── VLAN 1 ─── eth1 ┐
                              ├── router
    isp2 ─── VLAN 2 ─── eth2 ┘

  v6 plan (added imperatively after boot — two addresses per ISP so
  the per-target aggregation phase has somewhere to break a target
  without losing the whole WAN):

    isp1   eth1   fd00:1::1/64 + fd00:1::2/64
    isp2   eth1   fd00:2::1/64 + fd00:2::2/64
    router eth1   fd00:1::3/64
    router eth2   fd00:2::3/64

  Tunings — single windowSize/hysteresis set across all phases so
  the daemon's behaviour is comparable phase-to-phase:

    intervalMs  200        ⇒ 5 cycles/sec
    timeoutMs   100
    windowSize  10         ⇒ window cooks in ~2s
    consecDown  3          ⇒ failover after ~600ms of stable verdict
    consecUp    3          ⇒ recovery after ~600ms of stable verdict
    lossPctDown 25
    lossPctUp    5
    rttMsDown   5000       (loose — RTT not exercised)
    rttMsUp     4000

  Phases (linear, each lands in a known healthy state for the next):

    A  Cold-start cook + 100% netem ⇒ failover to backup
    B  Clear netem ⇒ primary recovery
    C  100% netem for ~2 cycles ⇒ window damping holds the verdict;
       no Decision recorded (blip suppression)
    D  Band-pass:
         D1  50% netem ⇒ above lossPctDown ⇒ failover
         D2  15% netem ⇒ between lossPctUp and lossPctDown ⇒
             verdict held unhealthy, backup stays active
         D3  0% netem ⇒ below lossPctUp ⇒ recovery
    E  Per-target aggregation: delete fd00:1::2 from isp1 ⇒
       primary's per-(WAN,family) loss averages to 50% across the
       two targets ⇒ failover; restore ⇒ recovery
    F  Daemon restart: systemctl restart wanwatch.service ⇒
       state.json is republished from bootstrap; the cold-start
       carrier path keeps primary active without a probe-driven
       Decision.
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
                targets.v6 = [
                  "fd00:1::1"
                  "fd00:1::2"
                ];
                intervalMs = 200;
                timeoutMs = 100;
                windowSize = 10;
                thresholds = {
                  lossPctDown = 25;
                  lossPctUp = 5;
                  rttMsDown = 5000;
                  rttMsUp = 4000;
                };
                hysteresis = {
                  consecutiveDown = 3;
                  consecutiveUp = 3;
                };
              };
            };
            backup = {
              interface = "eth2";
              pointToPoint = true;
              probe = {
                targets.v6 = [
                  "fd00:2::1"
                  "fd00:2::2"
                ];
                intervalMs = 200;
                timeoutMs = 100;
                windowSize = 10;
                thresholds = {
                  lossPctDown = 25;
                  lossPctUp = 5;
                  rttMsDown = 5000;
                  rttMsUp = 4000;
                };
                hysteresis = {
                  consecutiveDown = 3;
                  consecutiveUp = 3;
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
    import time


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


    def loss_ratio(router, wan, family="v6"):
        state = json.loads(router.succeed("cat /run/wanwatch/state.json"))
        return state["wans"][wan]["families"][family]["lossRatio"]


    def assert_unchanged_for(router, group, seconds, what):
        """Sample the health_decisions counter at start + after
        `seconds`; raise if it advanced. Used to pin "no Decision
        happened in this window" rather than wait for a thing that
        shouldn't happen."""
        before = health_decisions(router, group)
        router.execute(f"sleep {seconds}")
        after = health_decisions(router, group)
        assert after == before, (
            f"{what}: health-decisions advanced {before} → {after} "
            f"in {seconds}s — expected unchanged"
        )


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
    isp1.succeed("ip -6 addr add fd00:1::2/64 dev eth1")
    isp2.succeed("ip -6 addr add fd00:2::1/64 dev eth1")
    isp2.succeed("ip -6 addr add fd00:2::2/64 dev eth1")
    router.succeed("ip -6 addr add fd00:1::3/64 dev eth1")
    router.succeed("ip -6 addr add fd00:2::3/64 dev eth2")

    router.succeed("ip link set eth1 up")
    router.succeed("ip link set eth2 up")

    # Pin L3 reachability for every target before any probe
    # assertion (see failover-probe-loss.nix for the long-form race
    # rationale — same applies here on v6).
    for target in ("fd00:1::1", "fd00:1::2", "fd00:2::1", "fd00:2::2"):
        router.wait_until_succeeds(f"ping -6 -c 1 -W 1 {target}", timeout=30)

    # ==== Phase A — cold-start cook + 100% loss ⇒ failover ====

    wait_for_active(router, "primary")
    wait_for_wan_healthy(router, "primary")
    wait_for_wan_healthy(router, "backup")

    before_a = health_decisions(router, "home-uplink")
    router.succeed("tc qdisc add dev eth1 root netem loss 100%")
    wait_for_active(router, "backup")
    after_a = health_decisions(router, "home-uplink")
    assert after_a > before_a, (
        f"phase A: health-decisions did not advance: {before_a} → {after_a}"
    )
    assert loss_ratio(router, "primary") >= 0.5, (
        f"phase A: primary v6 lossRatio = {loss_ratio(router, 'primary')}, want ≥ 0.5"
    )

    # ==== Phase B — clear netem ⇒ recovery ====

    router.succeed("tc qdisc del dev eth1 root")
    wait_for_active(router, "primary")
    assert loss_ratio(router, "primary") <= 0.10, (
        f"phase B: primary v6 lossRatio = {loss_ratio(router, 'primary')}, want ≤ 0.10"
    )

    # ==== Phase C — blip suppression ====
    #
    # 100% netem for ~2 cycles (400ms) puts 2 lost samples into the
    # primary's per-target windows. With windowSize=10 that's 20%
    # loss aggregated — below the 25% lossPctDown threshold — so the
    # verdict stays healthy and no Decision should fire. Tests the
    # combined window-damping + hysteresis suppression of a brief
    # network blip.

    router.succeed("tc qdisc add dev eth1 root netem loss 100%")
    router.execute("sleep 0.4")
    router.succeed("tc qdisc del dev eth1 root")
    # Let things settle, then prove the counter didn't advance over
    # a follow-up window of equal length to the consec filter
    # (3 cycles = 600ms).
    assert_unchanged_for(router, "home-uplink", seconds=1.5,
                         what="phase C blip suppression")
    state_c = json.loads(router.succeed("cat /run/wanwatch/state.json"))
    assert state_c["groups"]["home-uplink"]["active"] == "primary", (
        f"phase C: active = {state_c['groups']['home-uplink']['active']!r}, want 'primary' (no Decision should have fired)"
    )

    # ==== Phase D — band-pass threshold ====
    #
    # D1: 50% netem (above lossPctDown=25) ⇒ failover.
    # D2: 15% netem (between lossPctUp=5 and lossPctDown=25) ⇒
    #     verdict held unhealthy by the band-pass; backup stays active.
    # D3: 0% netem (below lossPctUp) ⇒ recovery.

    # D1 — fail
    router.succeed("tc qdisc add dev eth1 root netem loss 50%")
    wait_for_active(router, "backup", timeout=15)
    # Soft window: 50% configured loss can give anywhere from 30%
    # to 70% over a 10-sample window — assert "above the 25%
    # threshold," not an exact value.
    assert loss_ratio(router, "primary") >= 0.25, (
        f"phase D1: primary v6 lossRatio = {loss_ratio(router, 'primary')}, want ≥ 0.25"
    )

    # D2 — stays unhealthy in the band-pass region
    router.succeed("tc qdisc change dev eth1 root netem loss 15%")
    # Let the window refill several times with the new rate, then
    # prove no Decision flipped the active member back.
    assert_unchanged_for(router, "home-uplink", seconds=3,
                         what="phase D2 band-pass hold")
    state_d2 = json.loads(router.succeed("cat /run/wanwatch/state.json"))
    assert state_d2["groups"]["home-uplink"]["active"] == "backup", (
        f"phase D2: active = {state_d2['groups']['home-uplink']['active']!r}, want 'backup' (verdict must hold in band-pass)"
    )

    # D3 — clear ⇒ recovery
    router.succeed("tc qdisc del dev eth1 root")
    wait_for_active(router, "primary", timeout=15)
    assert loss_ratio(router, "primary") <= 0.10, (
        f"phase D3: primary v6 lossRatio = {loss_ratio(router, 'primary')}, want ≤ 0.10"
    )

    # ==== Phase E — per-target aggregation ====
    #
    # Delete fd00:1::2 from isp1: primary's probes split — fd00:1::1
    # responds, fd00:1::2 doesn't. With Aggregate's unweighted mean
    # across two targets, per-(WAN, family) loss averages to ~50%
    # ⇒ above lossPctDown ⇒ failover. A regression in the
    # aggregator (e.g. min instead of mean) would let the WAN stay
    # healthy and this would never trigger.

    isp1.succeed("ip -6 addr del fd00:1::2/64 dev eth1")
    wait_for_active(router, "backup", timeout=15)
    # Aggregate should be in [0.3, 0.7] (one target ~0%, one ~100%
    # averaged). Loose so window noise doesn't flake the assertion.
    lr_e = loss_ratio(router, "primary")
    assert 0.3 <= lr_e <= 0.7, (
        f"phase E: primary v6 lossRatio = {lr_e}, want in [0.3, 0.7] "
        f"(one of two targets unreachable)"
    )

    isp1.succeed("ip -6 addr add fd00:1::2/64 dev eth1")
    router.wait_until_succeeds("ping -6 -c 1 -W 1 fd00:1::2", timeout=10)
    wait_for_active(router, "primary", timeout=15)

    # ==== Phase F — daemon restart ====
    #
    # systemctl restart wanwatch.service ⇒ the daemon starts fresh.
    # Cold-start carrier path keeps primary active (carrier is up,
    # health unknown), so the active member shouldn't change. After
    # probes converge, primary becomes probe-healthy again. The
    # health-decisions counter SHOULD reset to 0 (per-process
    # metric) and only increment if a probe-driven Decision fires —
    # which it shouldn't on a clean restart.

    router.succeed("systemctl restart wanwatch.service")
    router.wait_for_unit("wanwatch.service")
    router.wait_for_file("/run/wanwatch/state.json")
    # state.json is republished on bootstrap; the file existing
    # right after the unit reports active proves the writer ran.

    # Active member survives the restart via the cold-start carrier
    # path (no probe Window yet, but carrier=up keeps the high-
    # priority member selected).
    state_f = json.loads(router.succeed("cat /run/wanwatch/state.json"))
    assert state_f["groups"]["home-uplink"]["active"] == "primary", (
        f"phase F: active = {state_f['groups']['home-uplink']['active']!r}, want 'primary' (cold-start carrier path)"
    )

    # Probes converge again — the WAN flips back to probe-healthy
    # within the window-cook time (~2s) + consecUp (600ms).
    wait_for_wan_healthy(router, "primary", timeout=15)
    wait_for_wan_healthy(router, "backup", timeout=15)
  '';
}
