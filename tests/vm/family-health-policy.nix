/*
  family-health-policy — a dual-stack WAN whose v4 target answers
  while v6 is unreachable (no v6 address on the link). State must
  report v4 healthy, v6 unhealthy, and the WAN unhealthy under
  `familyHealthPolicy = "all"`.
*/
{
  pkgs,
  nixosModule,
}:

pkgs.testers.runNixOSTest {
  name = "wanwatch-family-health-policy";

  # The test driver numbers nodes alphabetically: isp is 192.168.1.1
  # and router is 192.168.1.2. pointToPoint needs no gateway.
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

    # All verdicts and the Selection must agree in one State snapshot;
    # a per-family flip alone does not prove the aggregate policy.
    observe.state({
        "wans": {"uplink": {
            "healthy": False,
            "families": {"v4": {"healthy": True}, "v6": {"healthy": False}},
        }},
        "groups": {"home": {"active": None}},
    }, timeout=20)

    # Wait for both live gauges; absence must not count as false.
    observe.wait_family_metrics("uplink", {"v4": True, "v6": False})
  '';
}
