/*
  Tests for `lib/types/global.nix`. `skeleton.nix` checks that the
  `global` submodule evaluates inputs to what `global.make` builds.
*/
{
  fixtures,
  helpers,
  lib,
  wanwatch,
  ...
}:
let
  inherit (fixtures) cases;
  inherit (helpers) isAcceptedBySubmodule predicateTests typeTests;
  inherit (wanwatch) types;
in
{
  globalLogLevel = typeTests types.globalLogLevel cases.logLevels;

  # The path options test the case table `global.tryMake` tests, so the
  # two layers agree.
  paths =
    lib.genAttrs
      [
        "statePath"
        "hooksDir"
        "metricsSocket"
      ]
      (
        field:
        predicateTests (
          value: isAcceptedBySubmodule "name" types.global { ${field} = value; }
        ) cases.nonEmptyStrings
      );

  global = typeTests types.global {
    invalid = [
      { logLevel = "verbose"; }
      { hookTimeoutMs = 0; }
    ];
  };
}
