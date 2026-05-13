/*
  Unit tests for `lib/internal/primitives.nix` (exposed as
  `wanwatch.internal.primitives`). Same `testFoo = { expr;
  expected; }` shape as every other unit test; aggregated by
  `tests/unit/default.nix`.

  Coverage discipline per PLAN.md §9.1: every public function
  exercised on both positive and negative inputs, including each
  `throws` branch via `builtins.tryEval` (`.success == false`).
*/
{ pkgs, ... }:
let
  primitives = import ../../../lib/internal/primitives.nix { inherit (pkgs) lib; };
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
  #
  # Uses `lib.nameValuePair` to build error records — same shape as
  # nixpkgs convention.

  testFormatErrorsSingleEntry = {
    expr = primitives.formatErrors "probe.make" [
      (pkgs.lib.nameValuePair "probeNoTargets" "no targets")
    ];
    expected = "probe.make: [probeNoTargets] no targets";
  };

  testFormatErrorsMultipleEntries = {
    expr = primitives.formatErrors "wan.make" [
      (pkgs.lib.nameValuePair "wanInvalidName" "name is empty")
      (pkgs.lib.nameValuePair "wanNoGateways" "no gateway set")
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
    expr = primitives.partitionTry (s: primitives.tryErr "bad:${s}") [
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
        parser = x: if x > 0 then primitives.tryOk x else primitives.tryErr "non-positive";
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
    # The typical usage: ++ a series of `check` calls into a flat
    # list of errors, with passing checks contributing nothing.
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

  # ===== parseOptional =====

  testParseOptionalNullInput = {
    expr =
      let
        parser = _: throw "must not be called";
      in
      primitives.parseOptional parser null;
    expected = {
      success = true;
      value = null;
      error = null;
    };
  };

  testParseOptionalDelegatesNonNull = {
    expr =
      let
        parser = s: primitives.tryOk "parsed:${s}";
      in
      primitives.parseOptional parser "input";
    expected = {
      success = true;
      value = "parsed:input";
      error = null;
    };
  };

  testParseOptionalPropagatesError = {
    expr =
      let
        parser = _: primitives.tryErr "bad";
      in
      primitives.parseOptional parser "input";
    expected = {
      success = false;
      value = null;
      error = "bad";
    };
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
