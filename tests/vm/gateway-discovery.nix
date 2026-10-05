/*
  gateway-discovery — networkd installs a v4 default route via the
  isp node. The daemon must discover that gateway from the main RIB,
  publish it in State, and write a `via` default route into the
  Group's table.
*/
{
  pkgs,
  nixosModule,
}:

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
    { lib, ... }:
    {
      imports = [ nixosModule ];
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
  '';
}
