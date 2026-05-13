/*
  gateway-discovery — boot a two-node topology (isp + router),
  configure the router's WAN interface with a kernel-installed
  default route via systemd-networkd, and verify:

    1. The daemon discovers the gateway from the kernel's main RIB
       (RouteSubscriber → GatewayCache) without operator config.
    2. state.json surfaces the discovered next-hop in
       `wans.<name>.gateways.{v4,v6}`.
    3. The daemon writes a `via <gw>` default route into the
       group's routing table — the non-PtP apply path that this
       commit series introduced.

  This test pins both the discovery loop and the state.json
  gateway field.
*/
{
  pkgs,
  nixosModule,
}:

pkgs.testers.runNixOSTest {
  name = "wanwatch-gateway-discovery";

  # nodes.isp:    192.168.1.1 — acts as the next-hop the router learns
  # nodes.router: 192.168.1.2 — uses Gateway=192.168.1.1
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

      # Override the default networkd config the test framework
      # supplies so we can declare a real Gateway= the kernel will
      # install in the main RIB. The daemon's RouteSubscriber
      # picks up the resulting RTM_NEWROUTE.
      networking.useNetworkd = true;
      systemd.network.networks."01-eth1" = lib.mkForce {
        matchConfig.Name = "eth1";
        networkConfig.Gateway = "192.168.1.1";
        # Pin the address so the kernel-assigned LAN side and the
        # nixos-test framework's assignment don't fight.
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
          # pointToPoint = false (default) → daemon discovers
          # gateway via netlink. This is the path under test.
          probe = {
            targets = [ "192.168.1.1" ];
            intervalMs = 600000;
            timeoutMs = 30000;
            hysteresis = {
              consecutiveDown = 10;
              consecutiveUp = 10;
            };
          };
        };
        groups.home.members = [
          {
            wan = "uplink";
            priority = 1;
          }
        ];
      };
    };

  testScript = ''
    import json


    def wait_for(predicate, timeout=15, what="condition"):
        for _ in range(timeout * 4):
            try:
                if predicate():
                    return
            except Exception:
                pass
            router.execute("sleep 0.25")
        raise AssertionError(f"{what} never became true within {timeout}s")


    start_all()
    isp.wait_for_unit("multi-user.target")
    router.wait_for_unit("wanwatch.service")
    router.wait_for_unit("systemd-networkd.service")
    router.succeed("ip link set eth1 up")

    # 1. The kernel really did install the default route the
    #    daemon needs to discover.
    main_route = router.succeed("ip -4 route show default")
    assert "192.168.1.1" in main_route, (
        f"kernel main-RIB default not installed:\n{main_route}"
    )

    # 2. The daemon publishes state.json with schema=1 + the
    #    discovered gateway in wans.uplink.gateways.v4.
    def has_gateway():
        state = json.loads(router.succeed("cat /run/wanwatch/state.json"))
        if state["schema"] != 1:
            return False
        return state["wans"]["uplink"]["gateways"]["v4"] == "192.168.1.1"


    wait_for(has_gateway, what="state.json gateway")

    # 3. The daemon wrote a `via 192.168.1.1` default route into
    #    the group's table — proves the non-PtP apply path works
    #    end-to-end (RouteEvent → cache → applyRoutes).
    def has_group_default():
        table = router.succeed(
            "jq -r '.groups.home.table' /etc/wanwatch/config.json"
        ).strip()
        out = router.succeed(f"ip -4 route show table {table}")
        return "192.168.1.1" in out and "eth1" in out


    wait_for(has_group_default, what="group-table default route")
  '';
}
