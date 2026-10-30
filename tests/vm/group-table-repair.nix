/*
  group-table-repair — the Group-table route disappears without a
  notification the daemon reacts to, and the main-table default comes
  back with the same Gateway. The daemon must rewrite the Group-table
  route without a Decision (docs/adr/0017-repair-committed-group-table.md).
*/
{ inputs, pkgs }:

pkgs.testers.runNixOSTest {
  name = "wanwatch-group-table-repair";

  # isp (192.168.1.1) is the next hop that router (192.168.1.2) learns.
  nodes.isp =
    { lib, ... }:
    {
      virtualisation.vlans = [ 1 ];
      networking.firewall.enable = lib.mkForce false;
    };

  nodes.router =
    { lib, pkgs, ... }:
    {
      imports = [ inputs.self.nixosModules.default ];
      virtualisation.vlans = [ 1 ];
      networking.firewall.enable = lib.mkForce false;

      # Replace the driver's networkd config with a Gateway= that the
      # kernel installs in the main RIB.
      networking.useNetworkd = true;
      systemd.network.networks."01-eth1" = lib.mkForce {
        matchConfig.Name = "eth1";
        networkConfig.Gateway = "192.168.1.1";
        # Pin the address so it agrees with the test driver's.
        address = [ "192.168.1.2/24" ];
      };

      environment.systemPackages = [
        pkgs.jq
        pkgs.iproute2
      ];

      services.wanwatch = {
        enable = true;
        wans.uplink = {
          interface = "eth1";
          probe = {
            targets.v4 = [ "192.168.1.1" ];
            # A long interval keeps Probe-driven Health changes, which
            # could rewrite the route through a Decision, out of the test.
            intervalMs = 600000;
            timeoutMs = 30000;
            hysteresis = {
              consecutiveDown = 10;
              consecutiveUp = 10;
            };
          };
        };
        groups.home = {
          members = {
            uplink.priority = 1;
          };
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
    router.wait_for_unit("systemd-networkd.service")
    router.succeed("ip link set eth1 up")

    observe.wait_default_route(
        "v4", "eth1", gateway="192.168.1.1", timeout=15
    )
    observe.wait_default_route(
        "v4", "eth1", group="home", gateway="192.168.1.1", timeout=15
    )
    before = observe.wait_selection("home", "uplink")["groups"]["home"]

    # The kernel flushes IPv4 routes through a device that goes down
    # without an RTM_DELROUTE. The daemon ignores Group-table
    # notifications, so deleting the route stands in for that flush,
    # without the carrier change a real link flap would add.
    router.succeed("ip -4 route del default table 1000")
    router.succeed('test -z "$(ip -4 route show default table 1000)"')

    # A second main-table default with the same Gateway stands in for
    # networkd's re-add and leaves the Gateway cache unchanged. The
    # kernel notifies the addition, while replacing networkd's
    # identical route is a silent no-op.
    router.succeed("ip -4 route add default via 192.168.1.1 dev eth1 metric 100")
    observe.wait_default_route(
        "v4", "eth1", group="home", gateway="192.168.1.1", timeout=15
    )

    # The rewrite is no Decision: the Selection keeps its start.
    after = observe.state()["groups"]["home"]
    assert after["decisionsTotal"] == before["decisionsTotal"], (
        f"decisionsTotal {before['decisionsTotal']} -> {after['decisionsTotal']}"
    )
    assert after["selectionSince"] == before["selectionSince"], (
        f"selectionSince {before['selectionSince']} -> {after['selectionSince']}"
    )
  '';
}
