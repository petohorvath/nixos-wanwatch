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
        statePath = "/run/wanwatch/state.json";
        hooksDir = "/etc/wanwatch/hooks";
        metricsSocket = "/run/wanwatch/metrics.sock";
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
    statePath = globalFieldTests "statePath" "globalInvalidStatePath" cases.nonEmptyStrings;
    hooksDir = globalFieldTests "hooksDir" "globalInvalidHooksDir" cases.nonEmptyStrings;
    metricsSocket = globalFieldTests "metricsSocket" "globalInvalidMetricsSocket" cases.nonEmptyStrings;
    logLevel = globalFieldTests "logLevel" "globalInvalidLogLevel" cases.logLevels;
    hookTimeoutMs = globalFieldTests "hookTimeoutMs" "globalNonPositiveHookTimeout" cases.positiveInts;
  };

  testReportsEveryViolation = {
    expr = getErrorKinds (
      global.tryMake {
        statePath = "";
        hooksDir = "";
        metricsSocket = "";
        logLevel = "verbose";
        hookTimeoutMs = 0;
        loglevel = "debug";
      }
    );
    expected = [
      "globalUnknownField"
      "globalInvalidStatePath"
      "globalInvalidHooksDir"
      "globalInvalidMetricsSocket"
      "globalInvalidLogLevel"
      "globalNonPositiveHookTimeout"
    ];
  };
}
