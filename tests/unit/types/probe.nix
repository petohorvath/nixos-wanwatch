/*
  Unit tests for `lib/types/probe.nix`. Exercises each exported
  option type via `lib.evalModules`.
*/
{ pkgs, libnet, ... }:
let
  wanwatch = import ../../../lib {
    inherit (pkgs) lib;
    inherit libnet;
  };
  inherit (wanwatch) types;

  helpers = import ../helpers.nix { inherit pkgs; };
  inherit (helpers) evalType evalTypeFails;

  # Minimum valid probe input — only `targets` is required.
  minimalProbe = {
    targets.v4 = [ "1.1.1.1" ];
  };
in
{
  # ===== probeMethod =====

  testProbeMethodAcceptsIcmp = {
    expr = evalType types.probeMethod "icmp";
    expected = "icmp";
  };

  testProbeMethodRejectsTcp = {
    expr = evalTypeFails types.probeMethod "tcp";
    expected = true;
  };

  testProbeMethodRejectsHttp = {
    expr = evalTypeFails types.probeMethod "http";
    expected = true;
  };

  # ===== probeFamilyHealthPolicy =====

  testProbeFamilyPolicyAcceptsAll = {
    expr = evalType types.probeFamilyHealthPolicy "all";
    expected = "all";
  };

  testProbeFamilyPolicyAcceptsAny = {
    expr = evalType types.probeFamilyHealthPolicy "any";
    expected = "any";
  };

  testProbeFamilyPolicyRejectsMajority = {
    expr = evalTypeFails types.probeFamilyHealthPolicy "majority";
    expected = true;
  };

  # ===== probeTarget =====

  testProbeTargetAcceptsV4 = {
    expr = evalType types.probeTarget "1.1.1.1";
    expected = "1.1.1.1";
  };

  testProbeTargetAcceptsV6 = {
    expr = evalType types.probeTarget "2606:4700:4700::1111";
    expected = "2606:4700:4700::1111";
  };

  testProbeTargetRejectsNonIp = {
    expr = evalTypeFails types.probeTarget "not-an-ip";
    expected = true;
  };

  # ===== probeThresholds — defaults =====

  testProbeThresholdsAllDefaults = {
    expr = evalType types.probeThresholds { };
    expected = {
      lossPctDown = 30;
      lossPctUp = 10;
      rttMsDown = 500;
      rttMsUp = 250;
    };
  };

  testProbeThresholdsPartialOverride = {
    expr = evalType types.probeThresholds {
      lossPctDown = 50;
    };
    expected = {
      lossPctDown = 50;
      lossPctUp = 10;
      rttMsDown = 500;
      rttMsUp = 250;
    };
  };

  testProbeThresholdsRejectsBadLossPct = {
    expr = evalTypeFails types.probeThresholds {
      lossPctDown = 150;
    };
    expected = true;
  };

  testProbeThresholdsRejectsBadRtt = {
    expr = evalTypeFails types.probeThresholds {
      rttMsDown = 0;
    };
    expected = true;
  };

  # ===== probeHysteresis — defaults =====

  testProbeHysteresisAllDefaults = {
    expr = evalType types.probeHysteresis { };
    expected = {
      consecutiveDown = 3;
      consecutiveUp = 5;
    };
  };

  testProbeHysteresisRejectsZero = {
    expr = evalTypeFails types.probeHysteresis {
      consecutiveDown = 0;
    };
    expected = true;
  };

  # ===== probe — top-level submodule =====

  testProbeMinimalFillsDefaults = {
    expr = evalType types.probe minimalProbe;
    expected = {
      method = "icmp";
      targets = {
        v4 = [ "1.1.1.1" ];
        v6 = [ ];
      };
      intervalMs = 500;
      timeoutMs = 1000;
      windowSize = 10;
      thresholds = {
        lossPctDown = 30;
        lossPctUp = 10;
        rttMsDown = 500;
        rttMsUp = 250;
      };
      hysteresis = {
        consecutiveDown = 3;
        consecutiveUp = 5;
      };
      familyHealthPolicy = "all";
    };
  };

  testProbeAcceptsBothFamilies = {
    expr =
      (evalType types.probe {
        targets = {
          v4 = [ "1.1.1.1" ];
          v6 = [ "2606:4700:4700::1111" ];
        };
      }).targets;
    expected = {
      v4 = [ "1.1.1.1" ];
      v6 = [ "2606:4700:4700::1111" ];
    };
  };

  testProbeRejectsBadMethod = {
    expr = evalTypeFails types.probe (minimalProbe // { method = "tcp"; });
    expected = true;
  };

  testProbeRejectsBadTarget = {
    expr = evalTypeFails types.probe { targets.v4 = [ "not-an-ip" ]; };
    expected = true;
  };

  testProbeRejectsLegacyListTargets = {
    # The pre-per-family shape (targets as a flat list) must fail
    # type-check now — buckets are mandatory.
    expr = evalTypeFails types.probe { targets = [ "1.1.1.1" ]; };
    expected = true;
  };

  testProbeRejectsNegativeInterval = {
    expr = evalTypeFails types.probe (minimalProbe // { intervalMs = -1; });
    expected = true;
  };

  testProbePreservesFullSpec = {
    expr = evalType types.probe {
      method = "icmp";
      targets.v4 = [ "8.8.8.8" ];
      intervalMs = 250;
      timeoutMs = 500;
      windowSize = 20;
      thresholds = {
        lossPctDown = 25;
        lossPctUp = 5;
        rttMsDown = 400;
        rttMsUp = 150;
      };
      hysteresis = {
        consecutiveDown = 2;
        consecutiveUp = 4;
      };
      familyHealthPolicy = "any";
    };
    expected = {
      method = "icmp";
      targets = {
        v4 = [ "8.8.8.8" ];
        v6 = [ ];
      };
      intervalMs = 250;
      timeoutMs = 500;
      windowSize = 20;
      thresholds = {
        lossPctDown = 25;
        lossPctUp = 5;
        rttMsDown = 400;
        rttMsUp = 150;
      };
      hysteresis = {
        consecutiveDown = 2;
        consecutiveUp = 4;
      };
      familyHealthPolicy = "any";
    };
  };
}
