/*
  metrics — Telegraf round trip. With the wanwatch and Telegraf
  modules enabled, Telegraf writes its scrapes to a file that must
  contain wanwatch_* series, and the telegraf user must belong to the
  wanwatch group.
*/
{
  pkgs,
  nixosModule,
  telegrafModule,
}:

let
  scrapeFile = "/var/lib/telegraf/scrape.log";
in
pkgs.testers.runNixOSTest {
  name = "wanwatch-metrics";

  nodes.router =
    { lib, ... }:
    {
      imports = [
        nixosModule
        telegrafModule
      ];

      boot.kernelModules = [ "dummy" ];

      systemd = {
        network.netdevs."10-wan0".netdevConfig = {
          Kind = "dummy";
          Name = "wan0";
        };
        network.networks."20-wan0" = {
          matchConfig.Name = "wan0";
          networkConfig.LinkLocalAddressing = "no";
          linkConfig.RequiredForOnline = "no";
          address = [ "192.0.2.10/24" ];
        };
        # Telegraf writes the scrape file under its StateDirectory.
        services.telegraf.serviceConfig.StateDirectory = "telegraf";
      };
      networking = {
        useNetworkd = true;
        useDHCP = false;
        firewall.enable = lib.mkForce false;
      };

      environment.systemPackages = [ pkgs.jq ];

      services.wanwatch = {
        enable = true;
        wans.primary = {
          interface = "wan0";
          pointToPoint = true;
          probe = {
            targets.v4 = [ "192.0.2.1" ];
            intervalMs = 600000;
            timeoutMs = 30000;
            hysteresis = {
              consecutiveDown = 10;
              consecutiveUp = 10;
            };
          };
        };
        groups.home-uplink = {
          members = [
            {
              wan = "primary";
              priority = 1;
            }
          ];
          mark = 1000;
          table = 1000;
        };
        telegraf = {
          enable = true;
          # Avoid waiting ten seconds for the first scrape.
          interval = "2s";
        };
      };

      services.telegraf = {
        enable = true;
        # The default Telegraf config has no outputs.
        extraConfig = {
          outputs.file = [
            {
              files = [ scrapeFile ];
              data_format = "prometheus";
            }
          ];
          # Flush at the scrape cadence to bound the test window.
          agent.flush_interval = "2s";
        };
      };
    };

  testScript = ''
    router.wait_for_unit("wanwatch.service")
    router.wait_for_unit("telegraf.service")
    router.succeed("ip link set wan0 up")

    # Wait for bootstrap so the per-WAN series have values; *Vec
    # metrics are omitted until observed.
    router.wait_for_file("/run/wanwatch/state.json")

    # Wait up to 30 s for Telegraf to scrape and flush once. The
    # driver's type check rejects rebinding the loop's int `_` to
    # execute()'s str output, so name both results.
    for _ in range(60):
        status, output = router.execute("test -s ${scrapeFile}")
        if status == 0:
            break
        router.execute("sleep 0.5")
    else:
        router.fail("test -s ${scrapeFile}")

    body = router.succeed("cat ${scrapeFile}")
    assert "wanwatch_build_info" in body, (
        f"telegraf scrape missing wanwatch_build_info:\n{body}"
    )
    assert "wanwatch_wan_carrier" in body, (
        f"telegraf scrape missing wanwatch_wan_carrier:\n{body}"
    )

    # The telegraf user reads the 0660 socket through the group.
    groups = router.succeed("groups telegraf")
    assert "wanwatch" in groups, f"telegraf not in wanwatch group: {groups}"
  '';
}
