/*
  gateway-discovery — networkd installs a v4 default route via the
  isp node. The daemon must discover that gateway from the main RIB,
  publish it in State, and write a `via` default route into the
  Group's table. It must then follow the default the kernel uses:
  the lowest metric of two defaults on the WAN's interface, the
  next-best default once that one is deleted, and the nexthop on the
  WAN's interface of a multipath default.
*/
{ inputs, pkgs }:

pkgs.testers.runNixOSTest {
  name = "wanwatch-gateway-discovery";

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
          # The default pointToPoint = false discovers the gateway.
          probe = {
            targets.v4 = [ "192.168.1.1" ];
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

    # networkd must first install the main-table default to discover.
    observe.wait_default_route(
        "v4", "eth1", gateway="192.168.1.1", timeout=15
    )

    # State must publish the discovered next-hop under schema 1.
    observe.wait_gateway("uplink", "v4", "192.168.1.1")

    # Verify the gateway Apply path in the Group's kernel table.
    observe.wait_default_route(
        "v4", "eth1", group="home", gateway="192.168.1.1", timeout=15
    )

    # Two defaults on eth1: the kernel uses the lower metric, and so
    # must discovery. The gateways need not answer; only the routes count.
    router.succeed(
        "ip route del default via 192.168.1.1 dev eth1",
        "ip route add default via 192.168.1.4 dev eth1 metric 200",
        "ip route add default via 192.168.1.3 dev eth1 metric 100",
    )
    observe.wait_gateway("uplink", "v4", "192.168.1.3")
    observe.wait_default_route(
        "v4", "eth1", group="home", gateway="192.168.1.3", timeout=15
    )

    # Deleting the preferred default moves the Gateway to the next-best
    # default rather than clearing it.
    router.succeed("ip route del default via 192.168.1.3 dev eth1 metric 100")
    observe.wait_gateway("uplink", "v4", "192.168.1.4")
    observe.wait_default_route(
        "v4", "eth1", group="home", gateway="192.168.1.4", timeout=15
    )

    # A multipath default has no gateway of its own. The WAN takes the
    # nexthop on its interface, listed after a nexthop on another link.
    router.succeed(
        "ip link add dummy0 type dummy",
        "ip address add 10.0.0.1/24 dev dummy0",
        "ip link set dummy0 up",
        "ip route del default via 192.168.1.4 dev eth1 metric 200",
        "ip route add default metric 100"
        + " nexthop via 10.0.0.2 dev dummy0"
        + " nexthop via 192.168.1.5 dev eth1",
    )
    observe.wait_gateway("uplink", "v4", "192.168.1.5")
    observe.wait_default_route(
        "v4", "eth1", group="home", gateway="192.168.1.5", timeout=15
    )
  '';
}
