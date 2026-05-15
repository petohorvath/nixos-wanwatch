/*
  cold-start — the cold→warm handoff: a healthy WAN whose first
  probe Window lands healthy must not flap.

  failover-v4 covers cold-start *carrier-only* health — it sets a
  10-minute probe interval so probes never land. This scenario is
  the complement: it lets a real probe Window cook and asserts the
  hysteresis is *seeded* from that first Window (PLAN §8) rather
  than ramped up from false. Without the seed, a WAN with
  consecutiveUp > 1 spends its first `consecutiveUp - 1` cycles
  below the up-threshold — so a perfectly healthy WAN is briefly
  dropped and re-selected, a spurious down+up Decision pair on
  every daemon start.

  Topology: one ISP node and a router on a single VLAN. The
  router's lone WAN probes the ISP; probes succeed throughout.

  Sequence:
    1. Cold start — primary is Selected on carrier alone, before
       any probe has cooked.
    2. The first good probe Window lands and seeds the hysteresis.
    3. Assert wanwatch_group_decisions_total{reason="health"} is 0:
       the seeded WAN's effective health never changed, so no
       health Decision fired. Pre-fix this reads 2 — the spurious
       down then up.

  intervalMs is 1000ms — generous enough that the cold-start
  carrier Selection reliably completes before the first probe
  Window lands, the ordering this scenario depends on.
*/
{
  pkgs,
  nixosModule,
}:

pkgs.testers.runNixOSTest {
  name = "wanwatch-cold-start";

  nodes.isp =
    { lib, ... }:
    {
      virtualisation.vlans = [ 1 ];
      networking.firewall.enable = lib.mkForce false;
    };

  nodes.router =
    { lib, ... }:
    {
      imports = [ nixosModule ];
      virtualisation.vlans = [ 1 ];
      networking.firewall.enable = lib.mkForce false;

      environment.systemPackages = [ pkgs.iproute2 ];

      services.wanwatch = {
        enable = true;
        wans.primary = {
          interface = "eth1";
          pointToPoint = true;
          probe = {
            targets.v4 = [ "192.168.1.1" ];
            # Long enough that the cold-start carrier Selection
            # lands before the first probe Window — see header.
            intervalMs = 1000;
            timeoutMs = 500;
            windowSize = 4;
            thresholds = {
              lossPctDown = 25;
              lossPctUp = 5;
              rttMsDown = 5000;
              rttMsUp = 4000;
            };
            # consecutiveUp > 1 is what makes the pre-fix ramp
            # observable: one good probe is not enough to flip the
            # hysteresis, so an unseeded WAN dips before recovering.
            hysteresis = {
              consecutiveDown = 2;
              consecutiveUp = 2;
            };
          };
        };
        groups.home-uplink.members = [
          {
            wan = "primary";
            priority = 1;
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


    def scrape(router):
        """Fetch the Prometheus scrape body over the Unix socket."""
        return router.succeed(
            "${pkgs.curl}/bin/curl -s --unix-socket "
            "/run/wanwatch/metrics.sock http://wanwatch/metrics"
        )


    def metric(body, series):
        """Value of an exact `name{labels}` series, or 0.0 if absent
        — Prometheus elides Vec series with no observations."""
        prefix = series + " "
        for line in body.splitlines():
            if line.startswith(prefix):
                return float(line[len(prefix):])
        return 0.0


    def wait_for_metric(router, series, want, timeout=30):
        """Poll the scrape until `series` reaches `want`."""
        for _ in range(timeout * 4):
            if metric(scrape(router), series) == want:
                return
            router.execute("sleep 0.25")
        raise AssertionError(
            f"{series} never reached {want}; last scrape =\n"
            + scrape(router)
        )


    start_all()
    isp.wait_for_unit("multi-user.target")
    router.wait_for_unit("wanwatch.service")

    # The test driver brings VLAN interfaces up during boot; pin
    # eth1's carrier explicitly so the daemon's cold-start
    # Selection is unambiguous.
    router.succeed("ip link set eth1 up")

    # Pre-warm the network before the cold-start measurement.
    # `wanwatch.service` starts on `network-pre.target`, so on a
    # slow runner (especially the unstable kernel/networkd matrix)
    # the daemon can begin probing eth1 before networkd has
    # assigned its VLAN-1 IP — the first probe Window then lands
    # all-Lost, hysteresis seeds unhealthy, and the WAN flaps
    # once probes catch up. That flap is exactly what this test
    # is supposed to detect *as a daemon bug*, so let it measure
    # the daemon's behavior, not the runner's networkd race:
    # block on router→isp reachability, then restart wanwatchd
    # so its probe loop starts from a known-good network state.
    router.wait_until_succeeds("ping -c 1 -W 1 192.168.1.1", timeout=30)
    router.systemctl("restart wanwatch.service")
    router.wait_for_unit("wanwatch.service")

    # 1. Cold start: primary is Selected on carrier alone, before
    #    any probe has cooked (PLAN §8 cold-start carrier health).
    wait_for_active(router, "primary")

    # 2. Let the first good probe Window land and seed the
    #    hysteresis. wanwatch_wan_family_healthy reaching 1 proves a
    #    ProbeResult has been folded in with a healthy verdict.
    #    (client_golang sorts label pairs alphabetically in the
    #    scrape, so it's `family` before `wan`.)
    wait_for_metric(
        router,
        'wanwatch_wan_family_healthy{family="v4",wan="primary"}',
        1.0,
    )

    # 3. The load-bearing assertion. The first probe Window seeds
    #    the hysteresis (PLAN §8) instead of ramping it from false,
    #    so the WAN's effective health never changes and no
    #    health-reason Decision is emitted. Pre-fix the ramp drops
    #    the WAN for consecutiveUp-1 cycles → a spurious down + up.
    body = scrape(router)
    health = metric(
        body,
        'wanwatch_group_decisions_total'
        '{group="home-uplink",reason="health"}',
    )
    assert health == 0, (
        f"health-reason Decisions = {health}, want 0 — a healthy WAN "
        f"flapped during cold-start warm-up (hysteresis ramped "
        f"instead of seeding). Scrape:\n{body}"
    )

    # 4. The cold-start carrier Selection did register as a Decision
    #    — the foil the anti-flap check above is measured against.
    carrier = metric(
        body,
        'wanwatch_group_decisions_total'
        '{group="home-uplink",reason="carrier"}',
    )
    assert carrier >= 1, (
        f"carrier-reason Decisions = {carrier}, want >= 1 (the "
        f"cold-start Selection)"
    )

    # 5. Sanity: the Selection held throughout.
    state = json.loads(router.succeed("cat /run/wanwatch/state.json"))
    active = state["groups"]["home-uplink"]["active"]
    assert active == "primary", f"active = {active!r}, want 'primary'"
  '';
}
