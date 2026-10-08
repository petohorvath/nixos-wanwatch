/*
  Tests for `lib/internal/probe.nix`, exposed as `wanwatch.probe`.
  `skeleton.nix` covers the `make` / `tryMake` / `toJSONValue`
  contract; this suite covers defaults, target parsing, and every
  error kind.
*/
{
  fixtures,
  helpers,
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
    ];
  };

  fields = {
    method = probeFieldTests "method" "probeInvalidMethod" cases.methods;
    intervalMs = probeFieldTests "intervalMs" "probeNonPositiveInterval" cases.positiveInts;
    timeoutMs = probeFieldTests "timeoutMs" "probeNonPositiveTimeout" cases.positiveInts;
    windowSize = probeFieldTests "windowSize" "probeNonPositiveWindow" cases.positiveInts;
    consecutiveDown =
      probeFieldTests "hysteresis.consecutiveDown" "probeNonPositiveHysteresis"
        cases.positiveInts;
    consecutiveUp =
      probeFieldTests "hysteresis.consecutiveUp" "probeNonPositiveHysteresis"
        cases.positiveInts;
    familyHealthPolicy =
      probeFieldTests "familyHealthPolicy" "probeInvalidFamilyPolicy"
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

    probeNonPositiveRTT = {
      zeroDown = withThresholds { rttMsDown = 0; };
      zeroUp = withThresholds { rttMsUp = 0; };
    };

    probeRTTThresholdsInverted = {
      upAboveDown = withThresholds {
        rttMsDown = 100;
        rttMsUp = 200;
      };
      upEqualsDown = withThresholds {
        rttMsDown = 250;
        rttMsUp = 250;
      };
    };
  };

  testReportsEveryViolation = {
    expr = getErrorKinds (
      probe.tryMake {
        targets = { };
        method = "tcp";
        intervalMs = 0;
      }
    );
    expected = [
      "probeInvalidMethod"
      "probeNoTargets"
      "probeNonPositiveInterval"
    ];
  };
}
