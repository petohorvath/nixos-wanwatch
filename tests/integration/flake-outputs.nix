# The flake (`self`) exposes the same code that `test-context.nix`
# imports from source, so the other suites test what the flake
# publishes.
{
  fixtures,
  helpers,
  self,
  wanwatch,
  ...
}:
let
  inherit (helpers) evalNixosWith renderConfig;

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

  # `evalNixosWith` arguments that evaluate the flake's modules.
  flakeModulesArgs.wanwatchModules = self.nixosModules;
in
{
  testLibExports = {
    expr = builtins.attrNames self.lib;
    expected = builtins.attrNames wanwatch;
  };

  testLibVersion = {
    expr = self.lib.version;
    expected = wanwatch.version;
  };

  testNixosModules = {
    expr = builtins.attrNames self.nixosModules;
    expected = [
      "default"
      "telegraf"
      "wanwatch"
    ];
  };

  testDefaultModuleRendersLikeSource = {
    expr = renderWith flakeModulesArgs;
    expected = renderWith { };
  };

  testWanwatchModuleIsDefault = {
    expr = renderWith {
      wanwatchModules = self.nixosModules // {
        default = self.nixosModules.wanwatch;
      };
    };
    expected = renderWith { };
  };

  testTelegrafModuleConfiguresLikeSource = {
    expr = scrapeInputWith flakeModulesArgs;
    expected = scrapeInputWith { };
  };
}
