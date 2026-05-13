/*
  Unit tests for `lib/internal/probe.nix` (exposed as `wanwatch.probe`).

  Coverage discipline per PLAN.md §9.1: every public function
  exercised on positive and negative inputs; every error kind
  triggered in isolation and at least one aggregated multi-error
  case; the §5.1 API skeleton (`make` / `tryMake` / `toJSONValue`)
  exercised.
*/
{ pkgs, libnet, ... }:
let
  wanwatch = import ../../../lib {
    inherit (pkgs) lib;
    inherit libnet;
  };
  inherit (wanwatch) probe;

  helpers = import ../helpers.nix { inherit pkgs; };
  inherit (helpers) evalThrows errorMatches;
  tryError = helpers.tryError probe;

  minimalInput = {
    targets = [ "1.1.1.1" ];
  };

  fullInput = {
    method = "icmp";
    targets = [
      "1.1.1.1"
      "2606:4700:4700::1111"
    ];
    intervalMs = 250;
    timeoutMs = 200;
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
in
{
  # ===== Happy path — minimal input =====

  testMakeMinimalReturnsValue = {
    expr = builtins.isAttrs (probe.make minimalInput);
    expected = true;
  };

  testMakeMinimalUsesDefaultMethod = {
    expr = probe.method (probe.make minimalInput);
    expected = "icmp";
  };

  testMakeMinimalUsesDefaultInterval = {
    expr = probe.intervalMs (probe.make minimalInput);
    expected = 500;
  };

  testMakeMinimalUsesDefaultTimeout = {
    expr = probe.timeoutMs (probe.make minimalInput);
    expected = 1000;
  };

  testMakeMinimalUsesDefaultWindowSize = {
    expr = probe.windowSize (probe.make minimalInput);
    expected = 10;
  };

  testMakeMinimalUsesDefaultThresholds = {
    expr = probe.thresholds (probe.make minimalInput);
    expected = {
      lossPctDown = 30;
      lossPctUp = 10;
      rttMsDown = 500;
      rttMsUp = 250;
    };
  };

  testMakeMinimalUsesDefaultHysteresis = {
    expr = probe.hysteresis (probe.make minimalInput);
    expected = {
      consecutiveDown = 3;
      consecutiveUp = 5;
    };
  };

  testMakeMinimalUsesDefaultFamilyPolicy = {
    expr = probe.familyHealthPolicy (probe.make minimalInput);
    expected = "all";
  };

  # ===== Happy path — full input =====

  testMakeFullPreservesMethod = {
    expr = probe.method (probe.make fullInput);
    expected = "icmp";
  };

  testMakeFullPreservesIntervalMs = {
    expr = probe.intervalMs (probe.make fullInput);
    expected = 250;
  };

  testMakeFullPreservesThresholds = {
    expr = probe.thresholds (probe.make fullInput);
    expected = {
      lossPctDown = 25;
      lossPctUp = 5;
      rttMsDown = 400;
      rttMsUp = 150;
    };
  };

  testMakeFullPreservesHysteresis = {
    expr = probe.hysteresis (probe.make fullInput);
    expected = {
      consecutiveDown = 2;
      consecutiveUp = 4;
    };
  };

  testMakeFullPreservesFamilyPolicy = {
    expr = probe.familyHealthPolicy (probe.make fullInput);
    expected = "any";
  };

  # ===== Target parsing =====

  testTargetsParsedToLibnetValues = {
    # Each target is stored as a libnet ip value carrying _type.
    expr = builtins.map (t: t._type) (probe.targets (probe.make fullInput));
    expected = [
      "ipv4"
      "ipv6"
    ];
  };

  testV4OnlyTargets = {
    expr = probe.families (
      probe.make {
        targets = [
          "1.1.1.1"
          "8.8.8.8"
        ];
      }
    );
    expected = {
      v4 = true;
      v6 = false;
    };
  };

  testV6OnlyTargets = {
    expr = probe.families (
      probe.make {
        targets = [
          "2606:4700:4700::1111"
          "2001:4860:4860::8888"
        ];
      }
    );
    expected = {
      v4 = false;
      v6 = true;
    };
  };

  testMixedFamilyTargets = {
    expr = probe.families (
      probe.make {
        targets = [
          "1.1.1.1"
          "2606:4700:4700::1111"
        ];
      }
    );
    expected = {
      v4 = true;
      v6 = true;
    };
  };

  # ===== Partial thresholds overlay =====

  testPartialThresholdsMergeWithDefaults = {
    # Specifying only lossPctDown should leave the other three at their defaults.
    expr = probe.thresholds (
      probe.make {
        targets = [ "1.1.1.1" ];
        thresholds = {
          lossPctDown = 50;
        };
      }
    );
    expected = {
      lossPctDown = 50;
      lossPctUp = 10;
      rttMsDown = 500;
      rttMsUp = 250;
    };
  };

  testPartialHysteresisMergeWithDefaults = {
    expr = probe.hysteresis (
      probe.make {
        targets = [ "1.1.1.1" ];
        hysteresis = {
          consecutiveUp = 8;
        };
      }
    );
    expected = {
      consecutiveDown = 3;
      consecutiveUp = 8;
    };
  };

  # ===== toJSONValue =====

  testToJSONValueStringifiesTargets = {
    # Targets render as strings, not nested libnet structures.
    expr = (probe.toJSONValue (probe.make { targets = [ "1.1.1.1" ]; })).targets;
    expected = [ "1.1.1.1" ];
  };

  # ===== Error: probeNoTargets =====

  testRejectsEmptyTargetsList = {
    expr = errorMatches "probeNoTargets" (tryError {
      targets = [ ];
    });
    expected = true;
  };

  testMakeThrowsOnEmptyTargets = {
    expr = evalThrows (probe.make { targets = [ ]; });
    expected = true;
  };

  # ===== Error: probeInvalidTarget =====

  testRejectsInvalidTarget = {
    expr = errorMatches "probeInvalidTarget" (tryError {
      targets = [ "not-an-ip" ];
    });
    expected = true;
  };

  testRejectsPartiallyInvalidTargets = {
    expr = errorMatches "probeInvalidTarget" (tryError {
      targets = [
        "1.1.1.1"
        "not-an-ip"
      ];
    });
    expected = true;
  };

  # ===== Error: probeInvalidMethod =====

  testRejectsInvalidMethod = {
    expr = errorMatches "probeInvalidMethod" (tryError (minimalInput // { method = "tcp"; }));
    expected = true;
  };

  testRejectsHttpMethod = {
    expr = errorMatches "probeInvalidMethod" (tryError (minimalInput // { method = "http"; }));
    expected = true;
  };

  # ===== Error: probeNonPositiveInterval =====

  testRejectsZeroInterval = {
    expr = errorMatches "probeNonPositiveInterval" (tryError (minimalInput // { intervalMs = 0; }));
    expected = true;
  };

  testRejectsNegativeInterval = {
    expr = errorMatches "probeNonPositiveInterval" (tryError (minimalInput // { intervalMs = -1; }));
    expected = true;
  };

  # ===== Error: probeNonPositiveTimeout =====

  testRejectsZeroTimeout = {
    expr = errorMatches "probeNonPositiveTimeout" (tryError (minimalInput // { timeoutMs = 0; }));
    expected = true;
  };

  # ===== Error: probeNonPositiveWindow =====

  testRejectsZeroWindowSize = {
    expr = errorMatches "probeNonPositiveWindow" (tryError (minimalInput // { windowSize = 0; }));
    expected = true;
  };

  # ===== Timeout / interval are independent (multiple probes in flight allowed) =====

  testAcceptsTimeoutExceedingInterval = {
    # dpinger-style: probes can overlap. send every 500ms but wait up
    # to 1000ms before declaring a probe lost.
    expr =
      (probe.tryMake (
        minimalInput
        // {
          intervalMs = 500;
          timeoutMs = 1000;
        }
      )).success;
    expected = true;
  };

  testAcceptsTimeoutEqualToInterval = {
    expr =
      (probe.tryMake (
        minimalInput
        // {
          intervalMs = 500;
          timeoutMs = 500;
        }
      )).success;
    expected = true;
  };

  # ===== Error: probeLossPctOutOfRange =====

  testRejectsNegativeLossPctDown = {
    expr = errorMatches "probeLossPctOutOfRange" (
      tryError (
        minimalInput
        // {
          thresholds = {
            lossPctDown = -1;
            lossPctUp = 0;
            rttMsDown = 500;
            rttMsUp = 250;
          };
        }
      )
    );
    expected = true;
  };

  testRejectsLossPctOver100 = {
    expr = errorMatches "probeLossPctOutOfRange" (
      tryError (
        minimalInput
        // {
          thresholds = {
            lossPctDown = 101;
            lossPctUp = 50;
            rttMsDown = 500;
            rttMsUp = 250;
          };
        }
      )
    );
    expected = true;
  };

  # ===== Error: probeLossThresholdsInverted =====

  testRejectsLossThresholdsInverted = {
    expr = errorMatches "probeLossThresholdsInverted" (
      tryError (
        minimalInput
        // {
          thresholds = {
            lossPctDown = 10;
            lossPctUp = 30;
            rttMsDown = 500;
            rttMsUp = 250;
          };
        }
      )
    );
    expected = true;
  };

  testRejectsLossThresholdsEqual = {
    expr = errorMatches "probeLossThresholdsInverted" (
      tryError (
        minimalInput
        // {
          thresholds = {
            lossPctDown = 20;
            lossPctUp = 20;
            rttMsDown = 500;
            rttMsUp = 250;
          };
        }
      )
    );
    expected = true;
  };

  # ===== Error: probeNonPositiveRTT =====

  testRejectsZeroRttDown = {
    expr = errorMatches "probeNonPositiveRTT" (
      tryError (
        minimalInput
        // {
          thresholds = {
            lossPctDown = 30;
            lossPctUp = 10;
            rttMsDown = 0;
            rttMsUp = 250;
          };
        }
      )
    );
    expected = true;
  };

  # ===== Error: probeRTTThresholdsInverted =====

  testRejectsRttThresholdsInverted = {
    expr = errorMatches "probeRTTThresholdsInverted" (
      tryError (
        minimalInput
        // {
          thresholds = {
            lossPctDown = 30;
            lossPctUp = 10;
            rttMsDown = 100;
            rttMsUp = 200;
          };
        }
      )
    );
    expected = true;
  };

  # ===== Error: probeNonPositiveHysteresis =====

  testRejectsZeroConsecutiveDown = {
    expr = errorMatches "probeNonPositiveHysteresis" (
      tryError (
        minimalInput
        // {
          hysteresis = {
            consecutiveDown = 0;
            consecutiveUp = 5;
          };
        }
      )
    );
    expected = true;
  };

  testRejectsZeroConsecutiveUp = {
    expr = errorMatches "probeNonPositiveHysteresis" (
      tryError (
        minimalInput
        // {
          hysteresis = {
            consecutiveDown = 3;
            consecutiveUp = 0;
          };
        }
      )
    );
    expected = true;
  };

  # ===== Error: probeInvalidFamilyPolicy =====

  testRejectsInvalidFamilyPolicy = {
    expr = errorMatches "probeInvalidFamilyPolicy" (
      tryError (minimalInput // { familyHealthPolicy = "majority"; })
    );
    expected = true;
  };

  testAcceptsAnyAsFamilyPolicy = {
    expr = (probe.tryMake (minimalInput // { familyHealthPolicy = "any"; })).success;
    expected = true;
  };

  # ===== Aggregated error reporting =====

  testMultipleErrorsAggregated = {
    # Submit a config with several distinct violations; every one
    # should appear in the error message. nftzones-style aggregation.
    expr =
      let
        err = tryError {
          targets = [ ];
          method = "tcp";
          intervalMs = 0;
        };
        kinds = [
          "probeNoTargets"
          "probeInvalidMethod"
          "probeNonPositiveInterval"
        ];
      in
      builtins.all (k: errorMatches k err) kinds;
    expected = true;
  };

  # ===== tryMake contract =====

  testTryMakeOkOnValid = {
    expr = (probe.tryMake minimalInput).success;
    expected = true;
  };

  testTryMakeReturnsValue = {
    expr = builtins.isAttrs (probe.tryMake minimalInput).value;
    expected = true;
  };

  testTryMakeErrOnInvalid = {
    expr = (probe.tryMake { targets = [ ]; }).success;
    expected = false;
  };

  testTryMakeErrorNullOnSuccess = {
    expr = (probe.tryMake minimalInput).error;
    expected = null;
  };

  testTryMakeValueNullOnFailure = {
    expr = (probe.tryMake { targets = [ ]; }).value;
    expected = null;
  };

  # ===== Defaults exposed =====

  testDefaultsExposed = {
    # Tests / module-types may reference probe.defaults directly.
    expr = probe.defaults.intervalMs;
    expected = 500;
  };
}
