/*
  family-health-policy — dual-stack WAN where the v4 target
  responds and the v6 target doesn't exist. Asserts that the
  per-family Healthy verdicts in state.json reflect reality (v4 =
  true, v6 = false) and that the WAN's aggregate Healthy under
  `familyHealthPolicy = "all"` follows the unhealthy family down.

  Needs real probe traffic — dummy interfaces can't simulate
  "v4 works, v6 doesn't". A two-node setup over VLAN 1 supplies
  it: the `isp` node responds to v4 ICMP echoes; v6 is unreachable
  by construction (no v6 address on either side of the link, so
  the daemon's v6 WriteTo returns ENETUNREACH, which the probe
  loop records as Lost).
*/
{
  pkgs,
  nixosModule,
}:

pkgs.testers.runNixOSTest {
  name = "wanwatch-family-health-policy";

  # The nixos-test framework's auto-IP assignment puts each node
  # at 192.168.<vlan>.<idx+1> in attribute-name order. With
  # nodes = { isp; router; } sorted alphabetically:
  #   isp    → 192.168.1.1
  #   router → 192.168.1.2
  # The router probes 192.168.1.1 (isp) over the on-link /24; no
  # default-route gateway is needed because pointToPoint installs
  # a scope-link route out of eth1.
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

      environment.systemPackages = [ pkgs.jq ];

      services.wanwatch = {
        enable = true;
        wans.uplink = {
          interface = "eth1";
          pointToPoint = true;
          probe = {
            targets = {
              v4 = [ "192.168.1.1" ];
              v6 = [ "fc00::1" ];
            };
            intervalMs = 1000;
            timeoutMs = 500;
            hysteresis = {
              consecutiveDown = 2;
              consecutiveUp = 2;
            };
            # Explicit "all" so the assertion below is meaningful.
            familyHealthPolicy = "all";
          };
        };
        groups.home = {
          members = [
            {
              wan = "uplink";
              priority = 1;
            }
          ];
          mark = 1000;
          table = 1000;
        };
      };
    };

  testScript = ''
    import json


    start_all()
    isp.wait_for_unit("multi-user.target")
    router.wait_for_unit("wanwatch.service")

    # Wait for both families to cook: v4 needs consecutiveUp=2
    # successful samples to flip to healthy (~2s); v6 cooks
    # as unhealthy on its first Lost sample.
    converged = False
    for _ in range(40):
        state = json.loads(router.succeed("cat /run/wanwatch/state.json"))
        fams = state["wans"]["uplink"]["families"]
        if (
            fams.get("v4", {}).get("healthy") is True
            and fams.get("v6", {}).get("healthy") is False
        ):
            converged = True
            break
        router.execute("sleep 0.5")
    assert converged, f"v4=healthy/v6=unhealthy never converged; last:\n{state}"

    # With familyHealthPolicy="all", the WAN aggregate Healthy
    # follows the unhealthy family down. The group then has no
    # active member.
    assert state["wans"]["uplink"]["healthy"] is False, (
        f"wan.healthy under policy=all should be false: {state['wans']['uplink']}"
    )
    assert state["groups"]["home"]["active"] is None, (
        f"group.active should be null when wan unhealthy: {state['groups']['home']}"
    )

    # The per-family Prometheus gauges should agree.
    body = router.succeed(
        "${pkgs.curl}/bin/curl -s --unix-socket /run/wanwatch/metrics.sock "
        "http://wanwatch/metrics"
    )
    assert (
        'wanwatch_wan_family_healthy{family="v4",wan="uplink"} 1' in body
    ), f"v4 gauge != 1; scrape:\n{body}"
    assert (
        'wanwatch_wan_family_healthy{family="v6",wan="uplink"} 0' in body
    ), f"v6 gauge != 0; scrape:\n{body}"
  '';
}
