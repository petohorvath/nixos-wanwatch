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

  # Auto-IP: the NixOS test driver indexes nodes *globally*
  # (alphabetical), not per-VLAN, so each node carries the same
  # last octet on every VLAN it joins.
  #   isp1   → idx 1 → 192.168.1.1
  #   isp2   → idx 2 → 192.168.2.2
  #   router → idx 3 → 192.168.1.3 + 192.168.2.3
  # Probe targets below reflect that — primary aims at .1 (isp1's
  # VLAN-1 address) and backup at .2 (isp2's VLAN-2 address).
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
                targets.v4 = [ "192.168.2.2" ];
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

  testScript = (builtins.readFile ./observation.py) + ''
    observe = Observation(router, curl="${pkgs.curl}/bin/curl")

    start_all()
    isp1.wait_for_unit("multi-user.target")
    isp2.wait_for_unit("multi-user.target")
    router.wait_for_unit("wanwatch.service")

    # Bring both uplinks carrier-up — the test framework brings
    # them up by default, but pin it for clarity.
    router.succeed("ip link set eth1 up")
    router.succeed("ip link set eth2 up")

    # Pin the network setup before any probe assertion: wanwatchd
    # starts in parallel with networkd, so it can begin its probe
    # loop before the router's eth2 has its VLAN-2 IP or before
    # isp2 finishes booting. On a fast machine the sliding window
    # converges anyway; under GitHub-runner load the first dozen
    # probes can all be unanswered, and a hysteresis with
    # consecutiveUp=2 keeps the WAN unhealthy long enough that the
    # 15s probe-healthy gate below times out — a false negative
    # that looks like a daemon bug. Block here until *router →
    # both ISPs* L3 reachability is real, then let the gate
    # actually measure the daemon.
    # `timeout=30` is generous on a healthy run (the ping for the
    # working ISP returns in ~0.2s) but bounds the damage when the
    # ping target is genuinely wrong — without it, the test driver
    # falls back to its 900s default and one bad scenario burns 15
    # minutes of CI time before failing.
    router.wait_until_succeeds("ping -c 1 -W 1 192.168.1.1", timeout=30)
    router.wait_until_succeeds("ping -c 1 -W 1 192.168.2.2", timeout=30)

    # 1. Primary wins on cold-start carrier health, then the probe
    #    loop cooks both WANs as healthy. After convergence we
    #    expect Active=primary (lowest priority among healthy).
    observe.wait_active("home-uplink", "primary", timeout=10)

    # Pre-injection invariant: *both* WANs must be probe-healthy,
    # not just carrier-up. Without this gate the wait_active
    # above is satisfied by the cold-start carrier path long before
    # the backup probe Window cooks, and step 3 would then time out
    # failing over to a backup that was never reachable in the first
    # place — a noise failure that masquerades as a daemon bug.
    observe.wait_healthy("primary")
    observe.wait_healthy("backup")

    # Snapshot the health-decisions counter before we inject loss —
    # the assertion below is "counter advanced", not "counter equal
    # to N", so we don't have to track every Decision.
    before = observe.decisions("home-uplink", "health")

    # 2. 100% packet loss on the primary uplink. netem at the
    #    egress qdisc drops every outbound packet — ICMP echoes
    #    leave the daemon's WriteTo but never reach isp1, so no
    #    reply comes back and the cycle records Lost.
    router.succeed("tc qdisc add dev eth1 root netem loss 100%")

    # 3. Failover happens within ~consecutiveDown * intervalMs
    #    plus apply + state-write overhead. With 2 × 200ms that's
    #    ~400ms; 10s is generous.
    observe.wait_active("home-uplink", "backup", timeout=10)

    # 4. The Decision was probe/threshold-driven, not carrier-
    #    driven — assert the `reason="health"` counter advanced.
    observe.wait_decisions("home-uplink", "health", minimum=before + 1)

    # state.json is transition-driven: assert the Decision snapshot's
    # verdict and down threshold, then use Prometheus for later samples.
    failed_state = observe.state()
    failed_v4 = failed_state["wans"]["primary"]["families"]["v4"]
    assert failed_v4["healthy"] is False, f"failed family state = {failed_v4}"
    assert failed_v4["lossRatio"] >= 0.25, (
        f"transition lossRatio = {failed_v4['lossRatio']}, want ≥ 0.25"
    )
    observe.wait_probe_loss("primary", "v4", 0.5)

    # 5. Clear the netem rule; primary should recover after
    #    `consecutiveUp` good samples (2 × 200ms ≈ 400ms).
    router.succeed("tc qdisc del dev eth1 root")
    observe.wait_active("home-uplink", "primary", timeout=10)

    # Final sanity: primary's live loss has fallen to the recovery band.
    observe.wait_probe_loss("primary", "v4", 0.0, 0.10)
  '';
}
