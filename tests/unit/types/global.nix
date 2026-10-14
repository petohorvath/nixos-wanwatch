/*
  Tests for `lib/types/global.nix`. `skeleton.nix` checks that the
  `global` submodule evaluates inputs to what `global.make` builds.
*/
{
  fixtures,
  helpers,
  wanwatch,
  ...
}:
let
  inherit (fixtures) cases;
  inherit (helpers) typeTests;
  inherit (wanwatch) types;
in
{
  globalLogLevel = typeTests types.globalLogLevel cases.logLevels;

  global = typeTests types.global {
    invalid = [
      { logLevel = "verbose"; }
      { hookTimeoutMs = 0; }
      { statePath = "/var/lib/wanwatch/state.json"; }
    ];
  };
}
