/*
  Tests for `lib/internal/global.nix`, exposed as `wanwatch.global`.
  `skeleton.nix` covers the `make` / `tryMake` / `toJSONValue`
  contract; this suite covers defaults and every error kind.
*/
{
  fixtures,
  helpers,
  wanwatch,
  ...
}:
let
  inherit (fixtures) cases;
  inherit (fixtures.inputs.global) minimal;
  inherit (helpers) getErrorKinds;
  inherit (wanwatch) global;

  globalFieldTests = helpers.fieldTests {
    inherit (global) tryMake;
    input = minimal;
  };
in
{
  defaults = {
    testValues = {
      expr = global.defaults;
      expected = {
        logLevel = "info";
        hookTimeoutMs = 5000;
      };
    };

    testFillMinimalInput = {
      expr = global.make minimal;
      expected = global.defaults;
    };

    testMergeUnderPartialInput = {
      expr = global.make { logLevel = "debug"; };
      expected = global.defaults // {
        logLevel = "debug";
      };
    };
  };

  fields = {
    logLevel = globalFieldTests "logLevel" "globalInvalidLogLevel" cases.logLevels;
    hookTimeoutMs = globalFieldTests "hookTimeoutMs" "globalNonPositiveHookTimeout" cases.positiveInts;
  };

  testReportsEveryViolation = {
    expr = getErrorKinds (
      global.tryMake {
        logLevel = "verbose";
        hookTimeoutMs = 0;
        loglevel = "debug";
      }
    );
    expected = [
      "globalUnknownField"
      "globalInvalidLogLevel"
      "globalNonPositiveHookTimeout"
    ];
  };

  # `config.paths` fixes the paths (ADR-0014), so they are not
  # settings.
  testRejectsPathField = {
    expr = getErrorKinds (global.tryMake { statePath = "/var/lib/wanwatch/state.json"; });
    expected = [ "globalUnknownField" ];
  };
}
