/*
  Unit tests for `lib/internal/primitives.nix`, exposed as
  `wanwatch.internal.primitives`. Per AGENTS.md, each public
  function is exercised on positive and negative inputs.
*/
{ pkgs, wanwatch, ... }:
let
  inherit (pkgs) lib;
  inherit (wanwatch.internal) primitives;
in
{
  # ===== tryOk =====

  testTryOkStructure = {
    expr = primitives.tryOk 42;
    expected = {
      success = true;
      value = 42;
      error = null;
    };
  };

  testTryOkWithNull = {
    expr = primitives.tryOk null;
    expected = {
      success = true;
      value = null;
      error = null;
    };
  };

  # ===== tryErr =====

  testTryErrStructure = {
    expr = primitives.tryErr "bad input";
    expected = {
      success = false;
      value = null;
      error = "bad input";
    };
  };

  testTryErrEmptyString = {
    expr = primitives.tryErr "";
    expected = {
      success = false;
      value = null;
      error = "";
    };
  };

  # ===== formatErrors =====

  testFormatErrorsSingleEntry = {
    expr = primitives.formatErrors "probe.make" [
      (lib.nameValuePair "probeNoTargets" "no targets")
    ];
    expected = "probe.make: [probeNoTargets] no targets";
  };

  testFormatErrorsMultipleEntries = {
    expr = primitives.formatErrors "wan.make" [
      (lib.nameValuePair "wanInvalidName" "name is empty")
      (lib.nameValuePair "wanNoGateways" "no gateway set")
    ];
    expected = "wan.make: [wanInvalidName] name is empty; [wanNoGateways] no gateway set";
  };

  testFormatErrorsEmpty = {
    expr = primitives.formatErrors "ctx" [ ];
    expected = "ctx: ";
  };

  # ===== check =====

  testCheckPassReturnsEmpty = {
    expr = primitives.check "kind" true "msg";
    expected = [ ];
  };

  testCheckFailReturnsRecord = {
    expr = primitives.check "kind" false "msg";
    expected = [
      {
        name = "kind";
        value = "msg";
      }
    ];
  };

  # ===== partitionTry =====

  testPartitionTryAllOk = {
    expr = primitives.partitionTry primitives.tryOk [
      1
      2
      3
    ];
    expected = {
      parsed = [
        1
        2
        3
      ];
      errors = [ ];
    };
  };

  testPartitionTryAllErr = {
    expr = primitives.partitionTry (item: primitives.tryErr "bad:${item}") [
      "a"
      "b"
    ];
    expected = {
      parsed = [ ];
      errors = [
        "bad:a"
        "bad:b"
      ];
    };
  };

  testPartitionTryMixed = {
    expr =
      let
        parser = number: if number > 0 then primitives.tryOk number else primitives.tryErr "non-positive";
      in
      primitives.partitionTry parser [
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
        "non-positive"
        "non-positive"
      ];
    };
  };

  testPartitionTryEmpty = {
    expr = primitives.partitionTry primitives.tryOk [ ];
    expected = {
      parsed = [ ];
      errors = [ ];
    };
  };

  testCheckChainable = {
    # Validators join `check` calls with `++`; passing checks
    # contribute nothing to the flat error list.
    expr =
      primitives.check "k1" true "m1"
      ++ primitives.check "k2" false "m2"
      ++ primitives.check "k3" true "m3"
      ++ primitives.check "k4" false "m4";
    expected = [
      {
        name = "k2";
        value = "m2";
      }
      {
        name = "k4";
        value = "m4";
      }
    ];
  };

  # ===== isValidName =====

  testIsValidNameAcceptsAlpha = {
    expr = primitives.isValidName "primary";
    expected = true;
  };

  testIsValidNameAcceptsHyphen = {
    expr = primitives.isValidName "home-uplink";
    expected = true;
  };

  testIsValidNameAcceptsAlphanumeric = {
    expr = primitives.isValidName "wan42";
    expected = true;
  };

  testIsValidNameRejectsEmpty = {
    expr = primitives.isValidName "";
    expected = false;
  };

  testIsValidNameRejectsLeadingDigit = {
    expr = primitives.isValidName "1primary";
    expected = false;
  };

  testIsValidNameRejectsSpace = {
    expr = primitives.isValidName "primary wan";
    expected = false;
  };

  testIsValidNameRejectsNonString = {
    expr = primitives.isValidName 42;
    expected = false;
  };
}
