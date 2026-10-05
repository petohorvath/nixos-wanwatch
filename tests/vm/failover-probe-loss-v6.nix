/*
  failover-probe-loss-v6 — exercise the v6 probe, threshold,
  hysteresis, and per-target aggregation chain on a real packet path.
  The other v6 scenarios are carrier-driven on dummy interfaces.

    isp1 ─── VLAN 1 ─── eth1 ┐
                              ├── router
    isp2 ─── VLAN 2 ─── eth2 ┘

  The test driver assigns only v4 addresses, so the script adds ULA
  addresses: two per ISP (fd00:1::1-2 and fd00:2::1-2) so one target
  can fail without losing the WAN, and fd00:1::3 / fd00:2::3 on the
  router. Every phase shares one tuning (200 ms interval, 10-Sample
  Window, consecutive{Up,Down} = 3) and leaves a known state for the
  next:

    A  100% netem loss fails over to the backup.
    B  Clearing netem restores the primary.
    D  50% loss fails over; clearing it recovers.
    E  Removing one primary target averages loss to ~50% and fails
       over; restoring it recovers.
    F  A daemon restart keeps the primary active on carrier.
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

  testScript = (builtins.readFile ./observation.py) + ''
    observe = Observation(router, curl="${pkgs.curl}/bin/curl")

    start_all()
    isp1.wait_for_unit("multi-user.target")
    isp2.wait_for_unit("multi-user.target")
    router.wait_for_unit("wanwatch.service")

    # Add ULA addresses for the v6 probes. Disabling DAD avoids its
    # one-second delay racing the reachability waits on slow runners.
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

    # Wait for every target before any probe assertion; see
    # failover-probe-loss.nix for the networkd race.
    for target in ("fd00:1::1", "fd00:1::2", "fd00:2::1", "fd00:2::2"):
        router.wait_until_succeeds(f"ping -6 -c 1 -W 1 {target}", timeout=30)

    # ==== Phase A — cold-start cook + 100% loss ⇒ failover ====

    observe.wait_active("home-uplink", "primary", timeout=10)
    observe.wait_healthy("primary")
    observe.wait_healthy("backup")

    before_a = observe.decisions("home-uplink", "health")
    router.succeed("tc qdisc add dev eth1 root netem loss 100%")
    observe.wait_active("home-uplink", "backup", timeout=10)
    observe.wait_decisions("home-uplink", "health", minimum=before_a + 1)
    # state.json changes only on transitions, so the Decision snapshot
    # can freeze below 50% loss when hysteresis flips before the
    # Window converges; assert only the 25% down threshold.
    state_a = observe.state()
    family_a = state_a["wans"]["primary"]["families"]["v6"]
    assert family_a["healthy"] is False, (
        f"phase A family state = {family_a}"
    )
    assert family_a["lossRatio"] >= 0.25, (
        f"phase A transition lossRatio = {family_a['lossRatio']}, "
        "want ≥ 0.25"
    )
    # Per-sample stats continue updating only on the Prometheus surface.
    observe.wait_probe_loss("primary", "v6", 0.5)

    # ==== Phase B — clear netem ⇒ recovery ====

    router.succeed("tc qdisc del dev eth1 root")
    observe.wait_active("home-uplink", "primary", timeout=10)
    # Poll live probe stats; state.json changes only on transitions.
    observe.wait_probe_loss("primary", "v6", 0.0, 0.10)

    # ==== Phase C — blip suppression (removed) ====
    #
    # Tested that a two-cycle (400 ms) netem blip leaves the Window at
    # 20% loss, below lossPctDown=25, so no Decision fires. It was too
    # timing-fragile for the VM tier:
    #
    #   - `tc qdisc add` can take 200-400 ms to apply on a loaded
    #     runner, stretching the blip past two cycles.
    #   - `sleep 0.4` measures wall-clock time, not the daemon's cycle
    #     phase, so a slow runner can fit a third cycle in the blip.
    #   - Three Lost Samples per target make 30% loss, and after
    #     consecutiveDown=3 cycles a Decision lands.
    #
    # Fixing it needs sub-cycle timing the driver lacks, a larger
    # windowSize that slows every phase, or a daemon test hook. Go unit
    # tests in internal/probe/ and internal/selector/ cover Window
    # damping and hysteresis deterministically.

    # ==== Phase D — band-pass threshold ====
    #
    # D1: 50% netem (above lossPctDown=25) ⇒ failover.
    # D3: 0% netem (below lossPctUp) ⇒ recovery.

    # D1 — fail
    router.succeed("tc qdisc add dev eth1 root netem loss 50%")
    observe.wait_active("home-uplink", "backup", timeout=15)
    # 50% netem loss varies widely over 10 Samples, so assert only
    # that live loss exceeds the 25% threshold.
    observe.wait_probe_loss("primary", "v6", 0.25)

    # ==== Phase D2 — band-pass hold (removed) ====
    #
    # Tested that 15% netem loss, between lossPctUp=5 and
    # lossPctDown=25, holds the unhealthy verdict: active stays
    # "backup" with no Decision for 3 seconds. Sample variance made it
    # flaky against unstable nixpkgs:
    #
    #   - With 10 Samples per target, P(no loss on one target) =
    #     0.85^10 ≈ 0.197, and on both targets ≈ 0.039. Such a Window
    #     falls below lossPctUp and reads healthy.
    #   - consecutiveUp=3 needs three such Windows in a row. Sliding
    #     Windows share all but the newest Sample, so given the first,
    #     three in a row has P ≈ 0.85^4 ≈ 0.522. Per cycle that is
    #     ≈ 0.020, or ~23% over the 13 cycles in 3 seconds:
    #     https://github.com/petohorvath/nixos-wanwatch/actions/runs/25958981356
    #
    # Fixing it needs windowSize ≈ 25 or per-test tuning, both slowing
    # every phase. internal/selector/hysteresis_test.go covers the hold
    # deterministically.

    # D3 — clear ⇒ recovery
    router.succeed("tc qdisc del dev eth1 root")
    observe.wait_active("home-uplink", "primary", timeout=15)
    observe.wait_probe_loss("primary", "v6", 0.0, 0.10)

    # ==== Phase E — per-target aggregation ====
    #
    # Removing fd00:1::2 leaves one of two targets answering; the
    # unweighted per-family mean (~50%) must exceed lossPctDown.

    isp1.succeed("ip -6 addr del fd00:1::2/64 dev eth1")
    observe.wait_active("home-uplink", "backup", timeout=15)
    # One target at ~0% and one at ~100% average into [0.3, 0.7].
    observe.wait_probe_loss("primary", "v6", 0.3, 0.7)

    isp1.succeed("ip -6 addr add fd00:1::2/64 dev eth1")
    router.wait_until_succeeds("ping -6 -c 1 -W 1 fd00:1::2", timeout=10)
    observe.wait_active("home-uplink", "primary", timeout=15)

    # ==== Phase F — daemon restart ====
    #
    # After a restart, cold-start carrier health keeps primary active
    # until the probes converge healthy again.

    router.succeed("systemctl restart wanwatch.service")
    router.wait_for_unit("wanwatch.service")
    # READY gates the new bootstrap State; wait for its Selection.
    observe.wait_active("home-uplink", "primary", timeout=15)

    # Probe Health returns within the Window cook time (~2 s) plus
    # consecutiveUp cycles.
    observe.wait_healthy("primary", timeout=15)
    observe.wait_healthy("backup", timeout=15)
  '';
}
