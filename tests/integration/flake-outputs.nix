/*
  The flake's public outputs, evaluated through flake-parts as
  consumers see them, expose the same code that `test-context.nix`
  imports from source, so the other suites test what the flake
  publishes.
*/
{
  fixtures,
  helpers,
  wanwatch,
  ...
}:
let
  inherit (helpers) evalNixosWith flakeOutputs renderConfig;

  # The config rendered from the shared declaration, with the
  # `evalNixosWith` arguments given.
  renderWith = evalArgs: renderConfig (evalNixosWith evalArgs [ fixtures.nixos.primaryAndBackup ]);

  # The Telegraf input configured with the `evalNixosWith` arguments
  # given.
  scrapeInputWith =
    evalArgs:
    (evalNixosWith (evalArgs // { telegraf = true; }) [
      fixtures.nixos.primaryAndBackup
      {
        services.wanwatch.telegraf.enable = true;
        services.telegraf.enable = true;
      }
    ]).services.telegraf.extraConfig.inputs.prometheus;

  fromFlake.wanwatchModules = flakeOutputs.nixosModules;
in
{
  testLibExports = {
    expr = builtins.attrNames flakeOutputs.lib;
    expected = builtins.attrNames wanwatch;
  };

  testLibVersion = {
    expr = flakeOutputs.lib.version;
    expected = wanwatch.version;
  };

  testNixosModules = {
    expr = builtins.attrNames flakeOutputs.nixosModules;
    expected = [
      "default"
      "telegraf"
      "wanwatch"
    ];
  };

  testDefaultModuleRendersLikeSource = {
    expr = renderWith fromFlake;
    expected = renderWith { };
  };

  testWanwatchModuleIsDefault = {
    expr = renderWith {
      wanwatchModules = flakeOutputs.nixosModules // {
        default = flakeOutputs.nixosModules.wanwatch;
      };
    };
    expected = renderWith { };
  };

  testTelegrafModuleConfiguresLikeSource = {
    expr = scrapeInputWith fromFlake;
    expected = scrapeInputWith { };
  };
}
