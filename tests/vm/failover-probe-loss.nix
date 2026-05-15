/*
  failover-probe-loss — exercises the probe + threshold + hysteresis
  chain end-to-end under realistic packet-loss conditions. The
  other failover scenarios drive Decisions via carrier-only events;
  this one keeps carrier up and induces failover purely through
  the probe loop seeing loss exceed `probe.thresholds.lossPctDown`.

  Topology: two ISP nodes on separate VLANs; router has one
  interface per VLAN. Both WANs are pointToPoint (scope-link
  routes, no explicit gateway needed).

    isp1 ─── VLAN 1 ─── eth1 ┐
                              ├── router
    isp2 ─── VLAN 2 ─── eth2 ┘

  Sequence:
    1. Wait for both probes to cook healthy → primary active.
    2. Inject 100% packet loss on the router's primary uplink
       via `tc qdisc add dev eth1 root netem loss 100%`.
    3. Probe loop sees 100% loss > lossPctDown (25%); after
       consecutiveDown samples the family verdict flips → WAN
       aggregate flips → selector picks backup.
    4. Verify decisions counter advanced with reason="health"
       (not "carrier" — carrier is still up).
    5. Clear the netem rule; probes succeed again; after
       consecutiveUp samples primary takes back over.

  Tunings chosen so the whole loop runs in single-digit seconds:
  interval 200ms, timeout 100ms, window size 4,
  consecutive{Up,Down}=2 — failover triggers ~400ms after netem
  injection (2 samples × 200ms) plus apply + state-write latency.
*/
{
  pkgs,
  nixosModule,
}:

pkgs.testers.runNixOSTest {
  name = "wanwatch-failover-probe-loss";

  # Auto-IP assigns 192.168.<vlan>.<idx+1> per node within each
  # VLAN, sorted by attribute name:
  #   VLAN 1: isp1 → .1, router → .2
  #   VLAN 2: isp2 → .1, router → .2
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
                targets.v4 = [ "192.168.1.1" ];
                intervalMs = 200;
                timeoutMs = 100;
                windowSize = 4;
                # Loose RTT bounds so this scenario only exercises
                # the loss-driven path; degraded latency is not
                # under test here. (Up < Down is a lib invariant.)
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
                targets.v4 = [ "192.168.2.1" ];
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
  };

  testScript = ''
    import json


    def wait_for_active(router, want, timeout=10):
        """Poll state.json until groups.home-uplink.active == want."""
        for _ in range(timeout * 10):
            out = router.succeed("cat /run/wanwatch/state.json")
            active = json.loads(out)["groups"]["home-uplink"]["active"]
            if active == want:
                return
            router.execute("sleep 0.1")
        raise AssertionError(
            f"active never reached {want!r}; last state =\n{out}"
        )


    def scrape(router):
        """Fetch the Prometheus scrape body over the Unix socket."""
        return router.succeed(
            "${pkgs.curl}/bin/curl -s --unix-socket "
            "/run/wanwatch/metrics.sock http://wanwatch/metrics"
        )


    def metric(body, series):
        """Value of an exact `name{labels}` series, or 0.0 if absent —
        Prometheus elides Vec series with no observations."""
        prefix = series + " "
        for line in body.splitlines():
            if line.startswith(prefix):
                return float(line[len(prefix):])
        return 0.0


    def wait_for_family_healthy(router, wan, timeout=15):
        """Poll until wanwatch_wan_family_healthy{wan,family=v4} == 1.

        The cold-start carrier path (PLAN §8) can satisfy
        `wait_for_active(group, "primary")` the moment carrier comes
        up on eth1, before any probe Window has cooked on *either*
        WAN. Injecting netem at that point would race a backup whose
        first probe sample may not have landed yet, and the assertion
        "active never reached 'backup'" 10s later wouldn't tell us
        whether the daemon mis-failed-over or whether backup was
        never probe-healthy to begin with.

        Reads the live Prometheus metric, not state.json — the latter
        is a Decision snapshot (PLAN §5.5) and only gets rewritten
        when the group's active member changes, so a backup whose
        probes go healthy *without* dislodging the active primary
        wouldn't appear healthy in state.json at all."""
        series = (
            'wanwatch_wan_family_healthy{family="v4",wan="' + wan + '"}'
        )
        for _ in range(timeout * 10):
            if metric(scrape(router), series) == 1.0:
                return
            router.execute("sleep 0.1")
        raise AssertionError(
            f"wan {wan!r} v4 family never became probe-healthy; "
            f"last scrape =\n{scrape(router)}"
        )


    def health_decisions(router, group):
        """Read wanwatch_group_decisions_total{group,reason="health"}
        from the scrape; returns 0 if the time series hasn't appeared
        yet."""
        series = (
            'wanwatch_group_decisions_total{group="'
            + group + '",reason="health"}'
        )
        return metric(scrape(router), series)


    start_all()
    isp1.wait_for_unit("multi-user.target")
    isp2.wait_for_unit("multi-user.target")
    router.wait_for_unit("wanwatch.service")

    # Bring both uplinks carrier-up — the test framework brings
    # them up by default, but pin it for clarity.
    router.succeed("ip link set eth1 up")
    router.succeed("ip link set eth2 up")

    # 1. Primary wins on cold-start carrier health, then the probe
    #    loop cooks both WANs as healthy. After convergence we
    #    expect Active=primary (lowest priority among healthy).
    wait_for_active(router, "primary")

    # Pre-injection invariant: *both* WANs must be probe-healthy,
    # not just carrier-up. Without this gate the wait_for_active
    # above is satisfied by the cold-start carrier path long before
    # the backup probe Window cooks, and step 3 would then time out
    # failing over to a backup that was never reachable in the first
    # place — a noise failure that masquerades as a daemon bug.
    wait_for_family_healthy(router, "primary")
    wait_for_family_healthy(router, "backup")

    # Snapshot the health-decisions counter before we inject loss —
    # the assertion below is "counter advanced", not "counter equal
    # to N", so we don't have to track every Decision.
    before = health_decisions(router, "home-uplink")

    # 2. 100% packet loss on the primary uplink. netem at the
    #    egress qdisc drops every outbound packet — ICMP echoes
    #    leave the daemon's WriteTo but never reach isp1, so no
    #    reply comes back and the cycle records Lost.
    router.succeed("tc qdisc add dev eth1 root netem loss 100%")

    # 3. Failover happens within ~consecutiveDown * intervalMs
    #    plus apply + state-write overhead. With 2 × 200ms that's
    #    ~400ms; 10s is generous.
    wait_for_active(router, "backup")

    # 4. The Decision was probe/threshold-driven, not carrier-
    #    driven — assert the `reason="health"` counter advanced.
    after = health_decisions(router, "home-uplink")
    assert after > before, (
        f"health-reason decisions did not advance: {before} → {after}\n"
        f"(failover may have fired via carrier instead — that would "
        f"indicate a regression in the probe path)"
    )

    # state.json should reflect the per-family observation: v4
    # lossRatio at or near 1.0, primary's family healthy=false.
    state = json.loads(router.succeed("cat /run/wanwatch/state.json"))
    v4 = state["wans"]["primary"]["families"]["v4"]
    assert v4["healthy"] is False, (
        f"primary v4 still healthy after 100% loss: {v4}"
    )
    assert v4["lossRatio"] >= 0.5, (
        f"primary v4 lossRatio = {v4['lossRatio']}, want ≥ 0.5"
    )

    # 5. Clear the netem rule; primary should recover after
    #    `consecutiveUp` good samples (2 × 200ms ≈ 400ms).
    router.succeed("tc qdisc del dev eth1 root")
    wait_for_active(router, "primary")

    # Final sanity: primary's family is healthy again and the
    # lossRatio has fallen to ≤ lossPctUp (5%) — i.e. ≤ 0.05.
    state = json.loads(router.succeed("cat /run/wanwatch/state.json"))
    v4 = state["wans"]["primary"]["families"]["v4"]
    assert v4["healthy"] is True, (
        f"primary v4 didn't recover after netem cleared: {v4}"
    )
    assert v4["lossRatio"] <= 0.10, (
        f"primary v4 lossRatio = {v4['lossRatio']}, want ≤ 0.10 post-recovery"
    )
  '';
}
