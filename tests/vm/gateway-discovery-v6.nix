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
        # state, racing the router's wait_until_succeeds below.
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

  testScript = ''
    import json


    def diagnostics(router):
        """Dump state.json + every routing table + the daemon's recent
        journal at failure time so the CI log captures enough to
        distinguish 'daemon never received RTM_NEWROUTE' from 'daemon
        received it but decoded Gw=nil' from 'state.json wasn't
        rewritten'."""
        try:
            state = router.succeed("cat /run/wanwatch/state.json")
        except Exception as e:
            state = f"<failed to read state.json: {e}>"
        try:
            routes = router.succeed("ip -6 route show table all")
        except Exception as e:
            routes = f"<failed: {e}>"
        try:
            journal = router.succeed(
                "journalctl -u wanwatch.service --no-pager -n 50 -o cat"
            )
        except Exception as e:
            journal = f"<failed: {e}>"
        return (
            "===== state.json =====\n" + state
            + "\n===== ip -6 route show table all =====\n" + routes
            + "\n===== last 50 wanwatch.service log lines =====\n" + journal
        )


    def wait_for(predicate, timeout=15, what="condition"):
        for _ in range(timeout * 4):
            try:
                if predicate():
                    return
            except Exception:
                pass
            router.execute("sleep 0.25")
        raise AssertionError(
            f"{what} never became true within {timeout}s\n" + diagnostics(router)
        )


    start_all()
    isp.wait_for_unit("multi-user.target")
    router.wait_for_unit("wanwatch.service")
    router.wait_for_unit("systemd-networkd.service")

    # Disable DAD on both sides post-boot — networkd's per-link
    # default is `IPv6AcceptRA=true` which leaves accept_dad=1
    # regardless of the per-network override. Setting the sysctl
    # imperatively guarantees the address is usable on the next
    # check without a one-second tentative window racing the
    # wait_until_succeeds.
    isp.succeed("sysctl -w net.ipv6.conf.eth1.accept_dad=0")
    router.succeed("sysctl -w net.ipv6.conf.eth1.accept_dad=0")

    router.succeed("ip link set eth1 up")
    isp.succeed("ip link set eth1 up")

    # 1. The kernel really did install the default route the
    #    daemon needs to discover. Poll: systemd-networkd may
    #    still be applying the Gateway= directive when
    #    wait_for_unit returns.
    router.wait_until_succeeds(
        "ip -6 route show default | grep -q 'via fd00:1::1'", timeout=15
    )

    # 2. The daemon publishes state.json with schema=1 + the
    #    discovered gateway in wans.uplink.gateways.v6.
    def has_gateway():
        state = json.loads(router.succeed("cat /run/wanwatch/state.json"))
        if state["schema"] != 1:
            return False
        return state["wans"]["uplink"]["gateways"]["v6"] == "fd00:1::1"


    wait_for(has_gateway, what="state.json v6 gateway")

    # 3. The daemon wrote a `via fd00:1::1` default route into
    #    the group's table — proves the non-PtP v6 apply path
    #    works end-to-end (RouteEvent → cache → applyRoutes).
    def has_group_default():
        table = router.succeed(
            "jq -r '.groups.home.table' /etc/wanwatch/config.json"
        ).strip()
        out = router.succeed(f"ip -6 route show table {table}")
        return "fd00:1::1" in out and "eth1" in out


    wait_for(has_group_default, what="group-table v6 default route")
  '';
}
