/*
  Unit tests for `lib/internal/primitives.nix` (exposed as
  `wanwatch.internal.primitives`). Same `testFoo = { expr;
  expected; }` shape as every other unit test; aggregated by
  `tests/unit/default.nix`.

  Coverage discipline per PLAN.md §9.1: every public function
  exercised on both positive and negative inputs, including each
  `throws` branch via `builtins.tryEval` (`.success == false`).
  Type-specific predicates (`isWan`, `isProbe`, …) live with their
  owning type modules and are tested in `tests/unit/internal/<type>.nix`.
*/
{ pkgs, ... }:
let
  primitives = import ../../../lib/internal/primitives.nix { inherit (pkgs) lib; };
  inherit (import ../helpers.nix { inherit pkgs; }) evalThrows;

  # A representative tagged value for the hasTag / ensureTag tests.
  tagged = {
    _type = "wan";
    name = "eth0";
  };

  # A different-tag value for mismatch cases.
  otherTagged = {
    _type = "probe";
    targets = [ "1.1.1.1" ];
  };
in
{
  # ===== hasTag — positive cases =====

  testHasTagMatches = {
    expr = primitives.hasTag "wan" tagged;
    expected = true;
  };

  # ===== hasTag — negative cases =====

  testHasTagWrongTag = {
    expr = primitives.hasTag "wan" otherTagged;
    expected = false;
  };

  testHasTagMissingTypeAttr = {
    expr = primitives.hasTag "wan" { name = "eth0"; };
    expected = false;
  };

  testHasTagNotAttrs = {
    expr = primitives.hasTag "wan" "eth0";
    expected = false;
  };

  testHasTagInt = {
    expr = primitives.hasTag "wan" 42;
    expected = false;
  };

  testHasTagList = {
    expr = primitives.hasTag "wan" [ "eth0" ];
    expected = false;
  };

  testHasTagNull = {
    expr = primitives.hasTag "wan" null;
    expected = false;
  };

  testHasTagEmptyAttrs = {
    expr = primitives.hasTag "wan" { };
    expected = false;
  };

  testHasTagCurriable = {
    # `hasTag tag` must be a curried function suitable for
    # `filter`/`any` and for per-type `is<Type>` binding.
    expr = builtins.filter (primitives.hasTag "wan") [
      tagged
      otherTagged
      "eth0"
    ];
    expected = [ tagged ];
  };

  # ===== tryOk =====

  testTryOkStructure = {
    expr = primitives.tryOk 42;
    expected = {
      success = true;
      value = 42;
      error = null;
    };
  };

  testTryOkWithAttrs = {
    expr = primitives.tryOk tagged;
    expected = {
      success = true;
      value = tagged;
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

  # ===== ensureTag — passes through =====

  testEnsureTagReturnsValueOnMatch = {
    expr = primitives.ensureTag "wan" "fn" tagged;
    expected = tagged;
  };

  # ===== ensureTag — throws =====

  testEnsureTagThrowsOnWrongTag = {
    expr = evalThrows (primitives.ensureTag "wan" "fn" otherTagged);
    expected = true;
  };

  testEnsureTagThrowsOnString = {
    expr = evalThrows (primitives.ensureTag "wan" "fn" "eth0");
    expected = true;
  };

  testEnsureTagThrowsOnInt = {
    expr = evalThrows (primitives.ensureTag "wan" "fn" 42);
    expected = true;
  };

  testEnsureTagThrowsOnNull = {
    expr = evalThrows (primitives.ensureTag "wan" "fn" null);
    expected = true;
  };

  testEnsureTagThrowsOnAttrsWithoutType = {
    expr = evalThrows (primitives.ensureTag "wan" "fn" { name = "eth0"; });
    expected = true;
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

  # ===== tagError =====

  testTagErrorStructure = {
    expr = primitives.tagError "someKind" "some message";
    expected = {
      name = "someKind";
      value = "some message";
    };
  };

  testTagErrorCurriable = {
    # The typical usage: `map (tagError "kind") errors` tags a list
    # of error strings with the same kind.
    expr = builtins.map (primitives.tagError "k") [
      "msg1"
      "msg2"
    ];
    expected = [
      {
        name = "k";
        value = "msg1";
      }
      {
        name = "k";
        value = "msg2";
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
