# The Telegraf module beside the NixOS module in a NixOS system.
{
  fixtures,
  helpers,
  lib,
  ...
}:
let
  inherit (helpers) failedWanwatchAssertions;

  # The Telegraf integration enabled, with the given settings changed;
  # `primaryAndBackup` enables wanwatch, so disabling it needs mkForce.
  evalTelegraf =
    overrides:
    helpers.evalNixosWith { telegraf = true; } [
      fixtures.nixos.primaryAndBackup
      (lib.recursiveUpdate {
        services.wanwatch.telegraf.enable = true;
        services.telegraf.enable = true;
      } overrides)
    ];

  telegrafConfig = evalTelegraf { };
in
{
  assertions = {
    testRequiresWanwatch = {
      expr = failedWanwatchAssertions (evalTelegraf {
        services.wanwatch.enable = lib.mkForce false;
      });
      expected = [
        ''
          services.wanwatch.telegraf.enable requires
          services.wanwatch.enable — the prometheus input has no
          socket to scrape otherwise.
        ''
      ];
    };

    testRequiresTelegraf = {
      expr = failedWanwatchAssertions (evalTelegraf {
        services.telegraf.enable = false;
      });
      expected = [
        ''
          services.wanwatch.telegraf.enable requires
          services.telegraf.enable — the input would land in a
          config no service consumes.
        ''
      ];
    };

    testBothEnabledPass = {
      expr = failedWanwatchAssertions telegrafConfig;
      expected = [ ];
    };
  };

  testScrapesMetricsSocket = {
    expr = telegrafConfig.services.telegraf.extraConfig.inputs.prometheus;
    expected = [
      {
        urls = [ "unix:///run/wanwatch/metrics.sock" ];
        namepass = [ "wanwatch_*" ];
        interval = "10s";
      }
    ];
  };

  # The metrics socket has mode 0660.
  testJoinsWanwatchGroup = {
    expr = builtins.elem "wanwatch" telegrafConfig.users.users.telegraf.extraGroups;
    expected = true;
  };
}
