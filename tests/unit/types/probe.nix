/*
  Tests for `lib/types/probe.nix`. `skeleton.nix` checks that the
  `probe` submodule evaluates inputs to what `probe.make` builds.
*/
{
  fixtures,
  helpers,
  wanwatch,
  ...
}:
let
  inherit (fixtures) cases;
  inherit (fixtures.inputs.probe) minimal;
  inherit (helpers) evalType typeTests;
  inherit (wanwatch) probe types;
in
{
  probeMethod = typeTests types.probeMethod cases.methods;
  probeFamilyHealthPolicy = typeTests types.probeFamilyHealthPolicy cases.familyHealthPolicies;

  probeTarget = typeTests types.probeTarget {
    valid = [
      "1.1.1.1"
      "2606:4700:4700::1111"
    ];
    invalid = [
      "not-an-ip"
      "1.1.1.1/32"
    ];
  };

  probeThresholds = {
    testFillsDefaults = {
      expr = evalType types.probeThresholds { lossPctDown = 50; };
      expected = probe.defaults.thresholds // {
        lossPctDown = 50;
      };
    };
  }
  // typeTests types.probeThresholds {
    invalid = [
      { lossPctDown = 150; }
      { rttMsDown = 0; }
    ];
  };

  probeHysteresis = {
    testFillsDefaults = {
      expr = evalType types.probeHysteresis { consecutiveUp = 8; };
      expected = probe.defaults.hysteresis // {
        consecutiveUp = 8;
      };
    };
  }
  // typeTests types.probeHysteresis {
    invalid = [
      { consecutiveDown = 0; }
    ];
  };

  probe = typeTests types.probe {
    invalid = [
      (minimal // { method = "tcp"; })
      (minimal // { intervalMs = -1; })
      { targets.v4 = [ "not-an-ip" ]; }
      # The former flat-list shape; per-family buckets are mandatory.
      { targets = [ "1.1.1.1" ]; }
    ];
  };
}
