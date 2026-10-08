# Tests for `lib/internal/primitives.nix`, exposed as
# `wanwatch.internal.primitives`.
{
  fixtures,
  helpers,
  lib,
  wanwatch,
  ...
}:
let
  inherit (fixtures) cases;
  inherit (helpers) predicateTests;
  inherit (wanwatch.internal) primitives;

  # Accepts positive numbers, so `partitionTry` sees both outcomes.
  parsePositive =
    number:
    if number > 0 then primitives.tryOk number else primitives.tryErr "non-positive ${toString number}";
in
{
  tryOk = {
    testValue = {
      expr = primitives.tryOk 42;
      expected = {
        success = true;
        value = 42;
        error = null;
      };
    };

    testNullValue = {
      expr = primitives.tryOk null;
      expected = {
        success = true;
        value = null;
        error = null;
      };
    };
  };

  tryErr = {
    testError = {
      expr = primitives.tryErr "bad input";
      expected = {
        success = false;
        value = null;
        error = "bad input";
      };
    };

    testEmptyError = {
      expr = primitives.tryErr "";
      expected = {
        success = false;
        value = null;
        error = "";
      };
    };
  };

  formatErrors = {
    testSingleError = {
      expr = primitives.formatErrors "probe.make" [
        (lib.nameValuePair "probeNoTargets" "no targets")
      ];
      expected = "probe.make: [probeNoTargets] no targets";
    };

    testJoinsErrorsInOrder = {
      expr = primitives.formatErrors "wan.make" [
        (lib.nameValuePair "wanInvalidName" "name is empty")
        (lib.nameValuePair "wanInvalidInterface" "interface is empty")
      ];
      expected = "wan.make: [wanInvalidName] name is empty; [wanInvalidInterface] interface is empty";
    };

    testNoErrors = {
      expr = primitives.formatErrors "ctx" [ ];
      expected = "ctx: ";
    };
  };

  check = {
    testPassReturnsNothing = {
      expr = primitives.check "kind" true "msg";
      expected = [ ];
    };

    testFailReturnsRecord = {
      expr = primitives.check "kind" false "msg";
      expected = [ (lib.nameValuePair "kind" "msg") ];
    };

    # Validators join `check` calls with `++`; passing checks add
    # nothing to the flat error list.
    testChainKeepsFailures = {
      expr =
        primitives.check "k1" true "m1"
        ++ primitives.check "k2" false "m2"
        ++ primitives.check "k3" true "m3"
        ++ primitives.check "k4" false "m4";
      expected = [
        (lib.nameValuePair "k2" "m2")
        (lib.nameValuePair "k4" "m4")
      ];
    };
  };

  partitionTry = {
    testEmpty = {
      expr = primitives.partitionTry parsePositive [ ];
      expected = {
        parsed = [ ];
        errors = [ ];
      };
    };

    testAllParsed = {
      expr = primitives.partitionTry parsePositive [
        1
        2
      ];
      expected = {
        parsed = [
          1
          2
        ];
        errors = [ ];
      };
    };

    testAllFailed = {
      expr = primitives.partitionTry parsePositive [
        0
        (-1)
      ];
      expected = {
        parsed = [ ];
        errors = [
          "non-positive 0"
          "non-positive -1"
        ];
      };
    };

    testMixedKeepsInputOrder = {
      expr = primitives.partitionTry parsePositive [
        1
        (-1)
        2
        0
        3
      ];
      expected = {
        parsed = [
          1
          2
          3
        ];
        errors = [
          "non-positive -1"
          "non-positive 0"
        ];
      };
    };
  };

  isValidName = predicateTests primitives.isValidName cases.identifiers;

  isPositiveInt = predicateTests primitives.isPositiveInt cases.positiveInts;
}
