/*
  gateway-discovery-v6 — IPv6 counterpart of gateway-discovery.

  Same two-node topology (isp + router); systemd-networkd installs
  a v6 default route into the router's main RIB via a
  `networkConfig.Gateway = "fd00:1::1"` directive. The daemon's
  rtnl.RouteSubscriber observes the RTM_NEWROUTE and threads the
  next-hop into the gatewayCache; the apply pass writes a
  `via fd00:1::1 dev eth1` default route into the group's per-
  family routing table.

  Exists because the v4 gateway-discovery scenario was the only
  end-to-end coverage of this loop. The daemon code is family-
  parameterised (rtnl.RouteFamily + apply.WriteDefault both take
  `family ∈ {v4, v6}`), but a regression in the v6 socket bind
  inside RTNLGRP_IPV6_ROUTE subscription, the v6 route attribute
  decode, or the v6 branch of the per-family apply loop would
  ship silent — no scenario hit that code path on a real packet
  stack until now.

  v6-specific setup. The nixosTest framework only auto-IPs v4 on
  VLAN-joined nodes, so both ends declare their `fd00:1::/64`
  addresses + Gateway via systemd.network instead of relying on
  the framework's defaults. DAD is disabled (accept_dad=0) on
  both nodes so the address becomes usable immediately rather
  than waiting out the default ~1 s probe window.
*/
{
  pkgs,
  nixosModule,
}:

pkgs.testers.runNixOSTest {
  name = "wanwatch-gateway-discovery-v6";

  # nodes.isp:    fd00:1::1 — acts as the next-hop the router learns
  # nodes.router: fd00:1::2 — uses Gateway=fd00:1::1
  nodes.isp =
    { lib, ... }:
    {
      virtualisation.vlans = [ 1 ];
      networking.firewall.enable = lib.mkForce false;
      networking.useNetworkd = true;
      systemd.network.networks."01-eth1" = lib.mkForce {
        matchConfig.Name = "eth1";
        address = [ "fd00:1::1/64" ];
        # Disable DAD on the netdev — the framework's default
        # accept_dad=1 means the address spends ~1 s in tentative
        # state, racing the router's route observation below.
        networkConfig.IPv6AcceptRA = false;
        linkConfig.RequiredForOnline = "no";
      };
    };

  nodes.router =
    { lib, ... }:
    {
      imports = [ nixosModule ];
      virtualisation.vlans = [ 1 ];
      networking.firewall.enable = lib.mkForce false;

      # Override the default networkd config the test framework
      # supplies so we can declare a real v6 Gateway= the kernel
      # will install in the main v6 RIB. The daemon's
      # RouteSubscriber picks up the resulting RTM_NEWROUTE.
      networking.useNetworkd = true;
      systemd.network.networks."01-eth1" = lib.mkForce {
        matchConfig.Name = "eth1";
        address = [ "fd00:1::2/64" ];
        networkConfig.Gateway = "fd00:1::1";
        networkConfig.IPv6AcceptRA = false;
        linkConfig.RequiredForOnline = "no";
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
            targets.v6 = [ "fd00:1::1" ];
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

    # Disable DAD on both sides post-boot — networkd's per-link
    # default is `IPv6AcceptRA=true` which leaves accept_dad=1
    # regardless of the per-network override. Setting the sysctl
    # imperatively guarantees the address is usable on the next
    # check without a one-second tentative window racing the
    # route observation.
    isp.succeed("sysctl -w net.ipv6.conf.eth1.accept_dad=0")
    router.succeed("sysctl -w net.ipv6.conf.eth1.accept_dad=0")

    router.succeed("ip link set eth1 up")
    isp.succeed("ip link set eth1 up")

    # networkd must first install the main-table default to discover.
    observe.wait_default_route(
        "v6", "eth1", gateway="fd00:1::1", timeout=15
    )

    # State must publish the discovered next-hop under schema 1.
    observe.wait_gateway("uplink", "v6", "fd00:1::1")

    # Independently verify the non-PtP Apply path in the Group's kernel table.
    observe.wait_default_route(
        "v6", "eth1", group="home", gateway="fd00:1::1", timeout=15
    )
  '';
}
