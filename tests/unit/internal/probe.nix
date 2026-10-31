/*
  Tests for `lib/internal/probe.nix`, exposed as `wanwatch.probe`.
  `skeleton.nix` covers the `make` / `tryMake` / `toJSONValue`
  contract; this suite covers defaults, target parsing, and every
  error kind.
*/
{
  fixtures,
  helpers,
  lib,
  libnet,
  wanwatch,
  ...
}:
let
  inherit (fixtures) cases;
  inherit (fixtures.inputs.probe) minimal;
  inherit (helpers) getErrorKinds;
  inherit (wanwatch) probe;

  probeFieldTests = helpers.fieldTests {
    inherit (probe) tryMake;
    input = minimal;
  };

  withThresholds = thresholds: minimal // { inherit thresholds; };

  inherit (fixtures.invalidTargets.probeTargetIpv4Mapped) mappedInV6;

  # `fixtures.invalidTargets` as `rejectionTests` cases.
  invalidTargetInputs = lib.mapAttrs (
    _: lib.mapAttrs (_: targets: { inherit targets; })
  ) fixtures.invalidTargets;
in
{
  defaults = {
    testValues = {
      expr = probe.defaults;
      expected = {
        method = "icmp";
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

    testFillMinimalInput = {
      expr = probe.toJSONValue (probe.make minimal);
      expected = probe.defaults // {
        targets = {
          v4 = [ "1.1.1.1" ];
          v6 = [ ];
        };
      };
    };

    testMergeUnderPartialThresholds = {
      expr =
        (probe.make (withThresholds {
          lossPctDown = 50;
        })).thresholds;
      expected = probe.defaults.thresholds // {
        lossPctDown = 50;
      };
    };

    testMergeUnderPartialHysteresis = {
      expr = (probe.make (minimal // { hysteresis.consecutiveUp = 8; })).hysteresis;
      expected = probe.defaults.hysteresis // {
        consecutiveUp = 8;
      };
    };
  };

  targets = {
    testParsedToLibnetValues = {
      expr =
        let
          inherit (probe.make fixtures.inputs.probe.full) targets;
        in
        {
          v4 = builtins.all libnet.ip.isIpv4 targets.v4;
          v6 = builtins.all libnet.ip.isIpv6 targets.v6;
        };
      expected = {
        v4 = true;
        v6 = true;
      };
    };
  };

  families = {
    testV4Only = {
      expr = probe.families (probe.make minimal);
      expected = {
        v4 = true;
        v6 = false;
      };
    };

    testV6Only = {
      expr = probe.families (probe.make { targets.v6 = [ "2606:4700:4700::1111" ]; });
      expected = {
        v4 = false;
        v6 = true;
      };
    };

    testDualStack = {
      expr = probe.families (probe.make fixtures.inputs.probe.full);
      expected = {
        v4 = true;
        v6 = true;
      };
    };
  };

  accepts = helpers.predicateTests (input: (probe.tryMake input).success) {
    valid = [
      # Samples may overlap, dpinger-style, so `timeoutMs` may reach or
      # exceed `intervalMs`.
      (minimal // { timeoutMs = probe.defaults.intervalMs; })
      (minimal // { timeoutMs = 2 * probe.defaults.intervalMs; })
      # Loss thresholds span the whole percentage range.
      (withThresholds {
        lossPctDown = 100;
        lossPctUp = 0;
      })
      # One bucket may hold several distinct Targets.
      {
        targets = {
          v4 = [
            "1.1.1.1"
            "8.8.8.8"
            "9.9.9.9"
          ];
          v6 = [
            "2606:4700:4700::1111"
            "2001:4860:4860::8888"
          ];
        };
      }
    ];
  };

  fields = {
    method = probeFieldTests "method" "probeInvalidMethod" cases.methods;
    intervalMs = probeFieldTests "intervalMs" "probeNonPositiveInterval" cases.positiveInts;
    timeoutMs = probeFieldTests "timeoutMs" "probeNonPositiveTimeout" cases.positiveInts;
    windowSize = probeFieldTests "windowSize" "probeNonPositiveWindowSize" cases.positiveInts;
    consecutiveDown =
      probeFieldTests "hysteresis.consecutiveDown" "probeNonPositiveHysteresis"
        cases.positiveInts;
    consecutiveUp =
      probeFieldTests "hysteresis.consecutiveUp" "probeNonPositiveHysteresis"
        cases.positiveInts;
    familyHealthPolicy =
      probeFieldTests "familyHealthPolicy" "probeInvalidFamilyHealthPolicy"
        cases.familyHealthPolicies;
  };

  rejections = helpers.rejectionTests probe.tryMake {
    probeNoTargets = {
      omittedBuckets.targets = { };
      emptyBuckets.targets = {
        v4 = [ ];
        v6 = [ ];
      };
    };

    probeInvalidTarget = {
      notAnIp.targets.v4 = [ "not-an-ip" ];
      oneOfSeveral.targets.v4 = [
        "1.1.1.1"
        "not-an-ip"
      ];
      # Malformed shapes surface as error kinds rather than evaluation
      # failures.
      targetsNotAnAttrset.targets = "1.1.1.1";
      bucketNotAList.targets.v4 = "1.1.1.1";
    };

    probeTargetFamilyMismatch = {
      v6InV4Bucket.targets.v4 = [ "2001:db8::1" ];
      v4InV6Bucket.targets.v6 = [ "192.0.2.1" ];
    };

    probeInvalidThresholds = {
      thresholdsNotAnAttrset = minimal // {
        thresholds = 5;
      };
    };

    probeInvalidHysteresis = {
      hysteresisNotAnAttrset = minimal // {
        hysteresis = [ 1 ];
      };
    };

    probeLossPctOutOfRange = {
      negativeDown = withThresholds {
        lossPctDown = -1;
        lossPctUp = 0;
      };
      downAbove100 = withThresholds { lossPctDown = 101; };
      upNotAnInt = withThresholds { lossPctUp = "5"; };
    };

    probeLossThresholdsInverted = {
      upAboveDown = withThresholds {
        lossPctDown = 10;
        lossPctUp = 30;
      };
      upEqualsDown = withThresholds {
        lossPctDown = 20;
        lossPctUp = 20;
      };
    };

    probeNonPositiveRtt = {
      zeroDown = withThresholds { rttMsDown = 0; };
      zeroUp = withThresholds { rttMsUp = 0; };
    };

    probeRttThresholdsInverted = {
      upAboveDown = withThresholds {
        rttMsDown = 100;
        rttMsUp = 200;
      };
      upEqualsDown = withThresholds {
        rttMsDown = 250;
        rttMsUp = 250;
      };
    };

    # `skeleton.nix` covers a misspelled top-level field.
    probeUnknownField = {
      misspelledThreshold = withThresholds { lossDown = 50; };
      misspelledHysteresisCounter = minimal // {
        hysteresis.consecutive = 2;
      };
      unknownTargetFamily.targets = {
        v4 = [ "1.1.1.1" ];
        v5 = [ ];
      };
    };
  };

  # Shared with `tests/integration/rejections.nix`.
  targetRejections = helpers.rejectionTests probe.tryMake invalidTargetInputs;

  testMakeThrowsOnIpv4MappedTarget = {
    expr = probe.make { targets = mappedInV6; };
    expectedError = {
      type = "ThrownError";
      msg = "\\[probeTargetIpv4Mapped]";
    };
  };

  testMakeThrowsOnDuplicateTarget = {
    expr = probe.make { targets.v4 = fixtures.invalidTargets.probeDuplicateTarget.duplicateInV4.v4; };
    expectedError = {
      type = "ThrownError";
      msg = "\\[probeDuplicateTarget]";
    };
  };

  # Each duplicated address is reported once, in canonical form.
  testDuplicateTargetMessage = {
    expr =
      (probe.tryMake {
        targets.v6 = [
          "2606:4700:4700:0:0:0:0:1111"
          "2606:4700:4700::1111"
          "2606:4700:4700::1111"
        ];
      }).error;
    expected = "probe.make: [probeDuplicateTarget] 2606:4700:4700::1111 appears more than once in targets.v6";
  };

  # The same address in both buckets is a Family mismatch in one of
  # them, not a duplicate.
  testDuplicateIsPerBucket = {
    expr = getErrorKinds (
      probe.tryMake {
        targets = {
          v4 = [ "1.1.1.1" ];
          v6 = [ "1.1.1.1" ];
        };
      }
    );
    expected = [ "probeTargetFamilyMismatch" ];
  };

  testIpv4MappedTargetMessage = {
    expr = (probe.tryMake { targets = mappedInV6; }).error;
    expected = "probe.make: [probeTargetIpv4Mapped] ${builtins.head mappedInV6.v6} in targets.v6 is an IPv4-mapped address; list the IPv4 address in targets.v4";
  };

  testUnknownFieldMessage = {
    expr =
      (probe.tryMake (withThresholds {
        lossDown = 50;
      })).error;
    expected = "probe.make: [probeUnknownField] thresholds has unknown fields [\"lossDown\"]; known fields are [\"lossPctDown\",\"lossPctUp\",\"rttMsDown\",\"rttMsUp\"]";
  };

  testReportsEveryViolation = {
    expr = getErrorKinds (
      probe.tryMake {
        targets = { };
        method = "tcp";
        intervalMs = 0;
        interval = 1000;
      }
    );
    expected = [
      "probeUnknownField"
      "probeInvalidMethod"
      "probeNoTargets"
      "probeNonPositiveInterval"
    ];
  };
}
