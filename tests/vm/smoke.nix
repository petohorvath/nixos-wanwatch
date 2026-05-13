/*
  smoke — boot a single-node VM with services.wanwatch.enable = true
  and verify the daemon comes up, the systemd unit reaches active,
  state.json + metrics socket appear under /run/wanwatch/, and the
  fwmark policy-routing rules land in both family RIBs.

  The test deliberately stays in "no probe target reachable" mode
  — the assertions focus on lifecycle + apply, not on actual
  failover (covered by failover-v4.nix / failover-v6.nix when those
  scenarios land).
*/
{
  pkgs,
  nixosModule,
}:

pkgs.testers.runNixOSTest {
  name = "wanwatch-smoke";

  nodes.router =
    { lib, ... }:
    {
      imports = [ nixosModule ];

      # The kernel boots without any real WAN — dummy interfaces
      # stand in so the daemon can bind probe sockets via
      # SO_BINDTODEVICE and rtnetlink reports a real Name.
      boot.kernelModules = [ "dummy" ];
      systemd.network.netdevs = {
        "10-wan0" = {
          netdevConfig = {
            Kind = "dummy";
            Name = "wan0";
          };
        };
        "10-wan1" = {
          netdevConfig = {
            Kind = "dummy";
            Name = "wan1";
          };
        };
      };
      systemd.network.networks = {
        "20-wan0" = {
          matchConfig.Name = "wan0";
          networkConfig.LinkLocalAddressing = "no";
          linkConfig.RequiredForOnline = "no";
          address = [ "192.0.2.10/24" ];
        };
        "20-wan1" = {
          matchConfig.Name = "wan1";
          networkConfig.LinkLocalAddressing = "no";
          linkConfig.RequiredForOnline = "no";
          address = [ "100.64.0.10/24" ];
        };
      };
      networking.useNetworkd = true;
      networking.useDHCP = false;

      environment.systemPackages = [ pkgs.jq ];

      services.wanwatch = {
        enable = true;
        wans = {
          primary = {
            interface = "wan0";
            pointToPoint = true;
            probe.targets = [ "192.0.2.1" ];
          };
          backup = {
            interface = "wan1";
            pointToPoint = true;
            probe.targets = [ "100.64.0.1" ];
          };
        };
        groups.home-uplink.members = [
          {
            wan = "primary";
            priority = 1;
          }
          {
            wan = "backup";
            priority = 2;
          }
        ];
      };

      # Disable the default firewall — the test asserts that the
      # daemon's fwmark rules land in the kernel, and a stateful
      # firewall would muddy the route lookup.
      networking.firewall.enable = lib.mkForce false;
    };

  testScript = ''
    router.wait_for_unit("wanwatch.service")

    # 1. Daemon is alive and the unit reached active.
    router.succeed("systemctl is-active wanwatch.service")

    # 2. Initial state.json is published from bootstrap — exists
    #    even before any probe sample.
    router.wait_for_file("/run/wanwatch/state.json")
    schema = router.succeed("jq -r .schema /run/wanwatch/state.json").strip()
    assert schema == "1", f"state.json schema = {schema!r}, want '1'"

    # 3. Metrics socket present and group-readable.
    router.wait_for_file("/run/wanwatch/metrics.sock")
    mode = router.succeed("stat -c %a /run/wanwatch/metrics.sock").strip()
    assert mode == "660", f"metrics socket mode = {mode!r}, want '660'"

    # 4. Scrape /metrics over the unix socket and assert
    #    wanwatch_build_info is present (set during bootstrap with
    #    a constant value of 1).
    body = router.succeed(
        "${pkgs.curl}/bin/curl -s --unix-socket /run/wanwatch/metrics.sock "
        "http://wanwatch/metrics"
    )
    assert "wanwatch_build_info" in body, (
        f"scrape body missing wanwatch_build_info:\n{body}"
    )

    # 5. The daemon's bootstrap step installed fwmark policy rules
    #    for the configured group, in BOTH families. PLAN §6.1.
    mark = router.succeed(
        "jq -r '.groups.\"home-uplink\".mark' /etc/wanwatch/config.json"
    ).strip()
    router.succeed(f"ip rule show fwmark 0x{int(mark):x} | grep -q .")
    router.succeed(f"ip -6 rule show fwmark 0x{int(mark):x} | grep -q .")
  '';
}
