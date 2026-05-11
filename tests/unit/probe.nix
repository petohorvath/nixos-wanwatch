/*
  Unit tests for `lib/probe.nix` (exposed as `wanwatch.probe`).

  Coverage discipline per PLAN.md §9.1: every public function
  exercised on positive and negative inputs; every error kind
  triggered in isolation and at least one aggregated multi-error
  case; the §5.1 API skeleton (`make` / `tryMake` / `isProbe` /
  `eq` / `compare` / derived ordering / `toJSON`) all exercised.
*/
{ pkgs, libnet, ... }:
let
  wanwatch = import ../../lib {
    inherit (pkgs) lib;
    inherit libnet;
  };
  inherit (wanwatch) probe;

  helpers = import ./helpers.nix { inherit pkgs; };
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

  testMakeMinimalReturnsTaggedValue = {
    expr = (probe.make minimalInput)._type;
    expected = "probe";
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

  # ===== Predicate: isProbe =====

  testIsProbeOnProbe = {
    expr = probe.isProbe (probe.make minimalInput);
    expected = true;
  };

  testIsProbeOnRawAttrs = {
    expr = probe.isProbe { foo = "bar"; };
    expected = false;
  };

  testIsProbeOnWan = {
    expr = probe.isProbe { _type = "wan"; };
    expected = false;
  };

  testIsProbeOnString = {
    expr = probe.isProbe "icmp";
    expected = false;
  };

  # ===== Equality =====

  testEqSameInput = {
    expr = probe.eq (probe.make minimalInput) (probe.make minimalInput);
    expected = true;
  };

  testEqEquivalentInputs = {
    # Specifying all-defaults explicitly must equal the default-implied value.
    expr = probe.eq (probe.make minimalInput) (probe.make (minimalInput // probe.defaults));
    expected = true;
  };

  testEqDifferentTargets = {
    expr = probe.eq (probe.make { targets = [ "1.1.1.1" ]; }) (probe.make { targets = [ "8.8.8.8" ]; });
    expected = false;
  };

  testEqDifferentInterval = {
    expr = probe.eq (probe.make minimalInput) (probe.make (minimalInput // { intervalMs = 250; }));
    expected = false;
  };

  # ===== Comparison =====

  testCompareEqualReturnsZero = {
    expr = probe.compare (probe.make minimalInput) (probe.make minimalInput);
    expected = 0;
  };

  testCompareIsDeterministic = {
    # Same inputs → same compare result. Tests that toJSON is canonical.
    expr =
      let
        a = probe.make minimalInput;
        b = probe.make minimalInput;
      in
      [
        (probe.compare a b)
        (probe.compare a b)
        (probe.compare a b)
      ];
    expected = [
      0
      0
      0
    ];
  };

  testCompareTotalOrderTrichotomy = {
    # For distinct values, exactly one of {-1, 1} holds; never 0.
    expr =
      let
        a = probe.make {
          targets = [ "1.1.1.1" ];
        };
        b = probe.make {
          targets = [ "8.8.8.8" ];
        };
        c = probe.compare a b;
      in
      c == -1 || c == 1;
    expected = true;
  };

  testCompareAntisymmetry = {
    expr =
      let
        a = probe.make {
          targets = [ "1.1.1.1" ];
        };
        b = probe.make {
          targets = [ "8.8.8.8" ];
        };
      in
      probe.compare a b == -(probe.compare b a);
    expected = true;
  };

  # ===== Derived ordering =====

  testLtDerived = {
    expr =
      let
        a = probe.make {
          targets = [ "1.1.1.1" ];
        };
        b = probe.make {
          targets = [ "8.8.8.8" ];
        };
      in
      probe.lt a b == (probe.compare a b == -1);
    expected = true;
  };

  testMinReturnsLesser = {
    expr =
      let
        a = probe.make {
          targets = [ "1.1.1.1" ];
        };
        b = probe.make {
          targets = [ "8.8.8.8" ];
        };
      in
      probe.min a b == (if probe.lt a b then a else b);
    expected = true;
  };

  testMaxReturnsGreater = {
    expr =
      let
        a = probe.make {
          targets = [ "1.1.1.1" ];
        };
        b = probe.make {
          targets = [ "8.8.8.8" ];
        };
      in
      probe.max a b == (if probe.lt a b then b else a);
    expected = true;
  };

  # ===== toJSON =====

  testToJSONReturnsString = {
    expr = builtins.isString (probe.toJSON (probe.make minimalInput));
    expected = true;
  };

  testToJSONIncludesTypeTag = {
    expr = pkgs.lib.hasInfix "\"_type\":\"probe\"" (probe.toJSON (probe.make minimalInput));
    expected = true;
  };

  testToJSONStringifiesTargets = {
    # Targets in JSON output are strings, not nested libnet structures.
    expr = pkgs.lib.hasInfix "\"targets\":[\"1.1.1.1\"]" (
      probe.toJSON (probe.make { targets = [ "1.1.1.1" ]; })
    );
    expected = true;
  };

  testToJSONDeterministic = {
    expr =
      let
        a = probe.toJSON (probe.make minimalInput);
        b = probe.toJSON (probe.make minimalInput);
      in
      a == b;
    expected = true;
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
    expr = (probe.tryMake minimalInput).value._type;
    expected = "probe";
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
