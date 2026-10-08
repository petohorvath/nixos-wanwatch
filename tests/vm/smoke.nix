/*
  smoke — boot a single-node router and verify the daemon lifecycle:
  the unit reaches active, State and the metrics socket appear under
  /run/wanwatch/, and the fwmark rules land in both family RIBs. No
  probe target is reachable; the failover-* scenarios cover failover.
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

      # Dummy interfaces stand in for real WANs so probe sockets can
      # bind via SO_BINDTODEVICE and rtnetlink reports a real Name.
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
      networking = {
        useNetworkd = true;
        useDHCP = false;
        # A stateful firewall would muddy the fwmark route lookup.
        firewall.enable = lib.mkForce false;
      };

      environment.systemPackages = [ pkgs.jq ];

      services.wanwatch = {
        enable = true;
        wans = {
          primary = {
            interface = "wan0";
            pointToPoint = true;
            probe.targets.v4 = [ "192.0.2.1" ];
          };
          backup = {
            interface = "wan1";
            pointToPoint = true;
            probe.targets.v4 = [ "100.64.0.1" ];
          };
        };
        groups.home-uplink = {
          members = [
            {
              wan = "primary";
              priority = 1;
            }
            {
              wan = "backup";
              priority = 2;
            }
          ];
          mark = 1000;
          table = 1000;
        };
      };
    };

  testScript = (builtins.readFile ./observation.py) + ''
    observe = Observation(router, curl="${pkgs.curl}/bin/curl")

    router.wait_for_unit("wanwatch.service")

    # 1. Daemon is alive and the unit reached active.
    router.succeed("systemctl is-active wanwatch.service")

    # 2. Bootstrap publishes state.json before any probe Sample.
    observe.state()

    # 3. Metrics socket present and group-readable.
    router.wait_for_file("/run/wanwatch/metrics.sock")
    mode = router.succeed("stat -c %a /run/wanwatch/metrics.sock").strip()
    assert mode == "660", f"metrics socket mode = {mode!r}, want '660'"

    # 4. Bootstrap sets wanwatch_build_info on the metrics endpoint.
    body = observe.scrape()
    assert "wanwatch_build_info" in body, (
        f"scrape body missing wanwatch_build_info:\n{body}"
    )

    # 5. Bootstrap installs the Group's fwmark rules in both families
    #    (docs/nftzones-integration.md) before sd_notify READY. Poll anyway, so a future
    #    reordering of bootstrap writes fails with a clear timeout.
    mark = router.succeed(
        "jq -r '.groups.\"home-uplink\".mark' /etc/wanwatch/config.json"
    ).strip()
    router.wait_until_succeeds(
        f"ip rule show fwmark 0x{int(mark):x} | grep -q .", timeout=10
    )
    router.wait_until_succeeds(
        f"ip -6 rule show fwmark 0x{int(mark):x} | grep -q .", timeout=10
    )
  '';
}
