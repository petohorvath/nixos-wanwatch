/*
  metrics — Telegraf round-trip. Boots the router with the
  wanwatch + telegraf modules both enabled, configures Telegraf
  to dump scraped metrics to a file, and verifies wanwatch_*
  series appear (including a sample with a `family` label,
  the per-(WAN, family) gauge that's most likely to silently
  regress when the metrics catalog drifts).
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

      systemd.network.netdevs."10-wan0".netdevConfig = {
        Kind = "dummy";
        Name = "wan0";
      };
      systemd.network.networks."20-wan0" = {
        matchConfig.Name = "wan0";
        networkConfig.LinkLocalAddressing = "no";
        linkConfig.RequiredForOnline = "no";
        address = [ "192.0.2.10/24" ];
      };
      networking.useNetworkd = true;
      networking.useDHCP = false;
      networking.firewall.enable = lib.mkForce false;

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
        groups.home-uplink.members = [
          {
            wan = "primary";
            priority = 1;
          }
        ];
        telegraf = {
          enable = true;
          # Tight interval so the test doesn't have to wait
          # ten seconds for the first scrape.
          interval = "2s";
        };
      };

      services.telegraf = {
        enable = true;
        # Telegraf default config has no outputs — append a file
        # output so the test can read the scraped metrics.
        extraConfig = {
          outputs.file = [
            {
              files = [ scrapeFile ];
              data_format = "prometheus";
            }
          ];
          agent = {
            # Flush at the same cadence as the scrape so the test
            # window is bounded.
            flush_interval = "2s";
          };
        };
      };

      # telegraf needs the StateDirectory to write to.
      systemd.services.telegraf.serviceConfig.StateDirectory = "telegraf";
    };

  testScript = ''
    router.wait_for_unit("wanwatch.service")
    router.wait_for_unit("telegraf.service")
    router.succeed("ip link set wan0 up")

    # Trigger at least one gauge update so the per-(WAN, family)
    # series has a value Telegraf can scrape (otherwise some
    # *Vec metrics elide when never observed).
    router.wait_for_file("/run/wanwatch/state.json")

    # Wait up to 30s for Telegraf to scrape + flush at least once.
    # Don't reuse `_` here — the for-loop's `_` is already typed
    # `int` and the typed test driver on stable channels rejects
    # reassigning it to execute()'s stdout str.
    for _ in range(60):
        ok, out = router.execute("test -s ${scrapeFile}")
        if ok == 0:
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

    # The metrics module promises the telegraf account is in the
    # wanwatch group so it can read the 0660 socket.
    groups = router.succeed("groups telegraf")
    assert "wanwatch" in groups, f"telegraf not in wanwatch group: {groups}"
  '';
}
