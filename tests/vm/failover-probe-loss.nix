/*
  failover-probe-loss — drive failover through the probe, threshold,
  and hysteresis chain while carrier stays up. netem loss on the
  primary uplink must switch the Group to the backup with a
  health-reason Decision; clearing it must restore the primary.

    isp1 ─── VLAN 1 ─── eth1 ┐
                              ├── router
    isp2 ─── VLAN 2 ─── eth2 ┘

  Both WANs are pointToPoint. A 200 ms interval and
  consecutive{Up,Down} = 2 keep each transition under a second.
*/
{
  pkgs,
  nixosModule,
}:

pkgs.testers.runNixOSTest {
  name = "wanwatch-failover-probe-loss";

  /*
    The test driver numbers nodes globally in alphabetical order, so
    each node keeps its last octet on every VLAN: isp1 is 192.168.1.1,
    isp2 is 192.168.2.2, and router is 192.168.1.3 and 192.168.2.3.
    The probe targets below follow that numbering.
  */
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
                # Loose RTT bounds keep this scenario on the
                # loss-driven path.
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

    # The test driver brings uplinks up already; pin it for clarity.
    router.succeed("ip link set eth1 up")
    router.succeed("ip link set eth2 up")

    # wanwatchd starts alongside networkd and can probe before the
    # uplinks have addresses; on a loaded runner the unanswered
    # probes keep a WAN unhealthy past the health gates below. Wait
    # for reachability so the gates measure the daemon. The explicit
    # timeout replaces the driver's 900 s default.
    router.wait_until_succeeds("ping -c 1 -W 1 192.168.1.1", timeout=30)
    router.wait_until_succeeds("ping -c 1 -W 1 192.168.2.2", timeout=30)

    # 1. Primary wins on cold-start carrier health and stays active
    #    once the probes cook both WANs healthy.
    observe.wait_active("home-uplink", "primary", timeout=10)

    # Carrier alone satisfies the wait above; require probe Health on
    # both WANs so step 3 cannot wait on an unreachable backup.
    observe.wait_healthy("primary")
    observe.wait_healthy("backup")

    # Step 4 asserts that the counter advanced, not an exact value.
    before = observe.decisions("home-uplink", "health")

    # 2. Egress netem drops every echo request on the primary
    #    uplink, so each cycle records Lost.
    router.succeed("tc qdisc add dev eth1 root netem loss 100%")

    # 3. Failover takes about consecutiveDown × intervalMs (400 ms)
    #    plus Apply and State latency.
    observe.wait_active("home-uplink", "backup", timeout=10)

    # 4. The Decision was health-driven; carrier stayed up.
    observe.wait_decisions("home-uplink", "health", minimum=before + 1)

    # state.json changes only on transitions: assert the Decision
    # snapshot, then use Prometheus for later Samples.
    failed_state = observe.state()
    failed_v4 = failed_state["wans"]["primary"]["families"]["v4"]
    assert failed_v4["healthy"] is False, (
        f"failed family state = {failed_v4}"
    )
    assert failed_v4["lossRatio"] >= 0.25, (
        f"transition lossRatio = {failed_v4['lossRatio']}, want ≥ 0.25"
    )
    observe.wait_probe_loss("primary", "v4", 0.5)

    # 5. Clearing netem restores primary after consecutiveUp good
    #    cycles.
    router.succeed("tc qdisc del dev eth1 root")
    observe.wait_active("home-uplink", "primary", timeout=10)

    # Primary's live loss falls back to the recovery band.
    observe.wait_probe_loss("primary", "v4", 0.0, 0.10)
  '';
}
