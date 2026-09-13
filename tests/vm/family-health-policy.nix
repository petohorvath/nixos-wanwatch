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

  testScript = (builtins.readFile ./observation.py) + ''
    observe = Observation(router, curl="${pkgs.curl}/bin/curl")

    start_all()
    isp.wait_for_unit("multi-user.target")
    router.wait_for_unit("wanwatch.service")

    # All verdicts and the Selection must agree in one State snapshot.
    # A per-family flip alone does not prove the aggregate policy was applied.
    observe.state({
        "wans": {"uplink": {
            "healthy": False,
            "families": {"v4": {"healthy": True}, "v6": {"healthy": False}},
        }},
        "groups": {"home": {"active": None}},
    }, timeout=20)

    # Independently wait for both live gauges; absence must not count as false.
    observe.wait_family_metrics("uplink", {"v4": True, "v6": False})
  '';
}
