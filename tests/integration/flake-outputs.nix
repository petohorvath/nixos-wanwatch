# The flake (`self`) exposes the same code that `test-context.nix`
# imports from source, so the other suites test what the flake
# publishes.
{
  fixtures,
  helpers,
  lib,
  self,
  wanwatch,
  ...
}:
let
  inherit (helpers) evalNixosWith renderConfig revisionFlags;

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

  # The `lib` names that ADR-0018 declares stable across 0.x minor
  # releases.
  stableLibPaths = [
    [
      "types"
      "fwmark"
    ]
    [
      "types"
      "routingTableId"
    ]
    [
      "config"
      "paths"
    ]
    [ "version" ]
  ];
in
{
  testStableLibNamesExist = {
    expr = builtins.filter (path: !lib.hasAttrByPath path self.lib) stableLibPaths;
    expected = [ ];
  };

  # ADR-0018 lets a stable type accept more values in 0.x but never
  # fewer, so both ends of the range they accept at v0.1.0 stay valid.
  testStableTypesAcceptRange = {
    expr = lib.genAttrs [ "fwmark" "routingTableId" ] (
      name:
      map self.lib.types.${name}.check [
        1000
        32767
      ]
    );
    expected = {
      fwmark = [
        true
        true
      ];
      routingTableId = [
        true
        true
      ];
    };
  };

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

  # The flake passes one revision to its `packages` output and to the
  # module's default package.
  testModulePackageRevisionMatchesPackages =
    let
      config = evalNixosWith flakeModulesArgs [ fixtures.nixos.primaryAndBackup ];
    in
    {
      expr = revisionFlags config.services.wanwatch.package;
      expected = revisionFlags self.packages.${config.nixpkgs.hostPlatform.system}.wanwatchd;
    };
}
