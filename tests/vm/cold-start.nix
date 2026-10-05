/*
  cold-start — a WAN whose first probe Window lands healthy must not
  flap during the cold-to-warm handoff.

  failover-v4 covers carrier-only cold-start health. Here a router's
  lone WAN probes an ISP node successfully, and the scenario asserts
  that the first Window seeds the hysteresis (PLAN §8) rather than
  ramping it from false. Without the seed, a WAN with
  consecutiveUp > 1 is dropped and re-selected on every daemon start:
  a spurious health-reason down + up Decision pair.
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
            # lands before the first probe Window.
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
        groups.home-uplink = {
          members = [
            {
              wan = "primary";
              priority = 1;
            }
          ];
          mark = 1000;
          table = 1000;
        };
      };
    };

  testScript = (builtins.readFile ./observation.py) + ''
    observe = Observation(router, curl="${pkgs.curl}/bin/curl")

    start_all()
    isp.wait_for_unit("multi-user.target")
    router.wait_for_unit("wanwatch.service")

    # The test driver brings VLAN interfaces up during boot; pin
    # eth1's carrier explicitly so the daemon's cold-start
    # Selection is unambiguous.
    router.succeed("ip link set eth1 up")

    # wanwatch.service starts on network-pre.target, so on a slow
    # runner it can probe eth1 before networkd assigns its address;
    # the all-Lost first Window then flaps the WAN for reasons
    # unrelated to the daemon. Wait for reachability and restart so
    # the measurement starts from a known-good network.
    router.wait_until_succeeds("ping -c 1 -W 1 192.168.1.1", timeout=30)
    router.systemctl("restart wanwatch.service")
    router.wait_for_unit("wanwatch.service")

    # 1. Cold start: primary is Selected on carrier alone, before
    #    any probe has cooked (PLAN §8 cold-start carrier health).
    observe.wait_active("home-uplink", "primary")

    # 2. Let the first good probe Window land and seed the
    #    hysteresis. wanwatch_wan_family_healthy reaching 1 proves a
    #    ProbeResult has been folded in with a healthy verdict.
    observe.wait_family_metrics("primary", {"v4": True}, timeout=30)

    # 3. The seeded hysteresis (PLAN §8) keeps the WAN's effective
    #    health unchanged, so no health-reason Decision is emitted.
    health = observe.decisions("home-uplink", "health")
    assert health == 0, (
        f"health-reason Decisions = {health}, want 0 — a healthy WAN "
        f"flapped during cold-start warm-up (hysteresis ramped "
        f"instead of seeding)."
    )

    # 4. The cold-start carrier Selection did register as a Decision
    #    — the foil the anti-flap check above is measured against.
    carrier = observe.decisions("home-uplink", "carrier")
    assert carrier >= 1, (
        f"carrier-reason Decisions = {carrier}, want >= 1 (the "
        f"cold-start Selection)"
    )

    # 5. Sanity: the Selection held throughout.
    state = observe.state()
    active = state["groups"]["home-uplink"]["active"]
    assert active == "primary", f"active = {active!r}, want 'primary'"
  '';
}
