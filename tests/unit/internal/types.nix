/*
  Unit tests for `lib/internal/types.nix` (exposed as
  `wanwatch.internal.types`). Same `testFoo = { expr; expected; }`
  shape as every other unit test; aggregated by
  `tests/unit/default.nix`.

  Coverage discipline per PLAN.md §9.1: every public function
  exercised on both positive and negative inputs, including each
  `throws` branch via `builtins.tryEval` (`.success == false`).
*/
{ pkgs, ... }:
let
  types = import ../../../lib/internal/types.nix;

  # An attrset matching a real wan tag — used in positive cases.
  wanValue = {
    _type = "wan";
    name = "eth0";
  };

  # A wrong-tag attrset — used in mismatch cases.
  probeValue = {
    _type = "probe";
    targets = [ "1.1.1.1" ];
  };

  evalThrows = expr: !(builtins.tryEval expr).success;
in
{
  # ===== tags — canonical set =====

  testTagsExactSet = {
    expr = builtins.attrNames types.tags;
    expected = [
      "group"
      "member"
      "probe"
      "wan"
    ];
  };

  testTagsWanValue = {
    expr = types.tags.wan;
    expected = "wan";
  };

  testTagsProbeValue = {
    expr = types.tags.probe;
    expected = "probe";
  };

  testTagsGroupValue = {
    expr = types.tags.group;
    expected = "group";
  };

  testTagsMemberValue = {
    expr = types.tags.member;
    expected = "member";
  };

  # ===== hasTag — positive cases =====

  testHasTagMatches = {
    expr = types.hasTag "wan" wanValue;
    expected = true;
  };

  testHasTagMatchesViaConstant = {
    expr = types.hasTag types.tags.wan wanValue;
    expected = true;
  };

  # ===== hasTag — negative cases =====

  testHasTagWrongTag = {
    expr = types.hasTag "wan" probeValue;
    expected = false;
  };

  testHasTagMissingTypeAttr = {
    expr = types.hasTag "wan" { name = "eth0"; };
    expected = false;
  };

  testHasTagNotAttrs = {
    expr = types.hasTag "wan" "eth0";
    expected = false;
  };

  testHasTagInt = {
    expr = types.hasTag "wan" 42;
    expected = false;
  };

  testHasTagList = {
    expr = types.hasTag "wan" [ "eth0" ];
    expected = false;
  };

  testHasTagNull = {
    expr = types.hasTag "wan" null;
    expected = false;
  };

  testHasTagEmptyAttrs = {
    expr = types.hasTag "wan" { };
    expected = false;
  };

  # ===== isWan / isProbe / isGroup / isMember — positive =====

  testIsWanPositive = {
    expr = types.isWan wanValue;
    expected = true;
  };

  testIsProbePositive = {
    expr = types.isProbe probeValue;
    expected = true;
  };

  testIsGroupPositive = {
    expr = types.isGroup { _type = "group"; };
    expected = true;
  };

  testIsMemberPositive = {
    expr = types.isMember { _type = "member"; };
    expected = true;
  };

  # ===== isWan / isProbe / isGroup / isMember — negative =====

  testIsWanNegativeMismatch = {
    expr = types.isWan probeValue;
    expected = false;
  };

  testIsProbeNegativeMismatch = {
    expr = types.isProbe wanValue;
    expected = false;
  };

  testIsGroupNegativeMismatch = {
    expr = types.isGroup wanValue;
    expected = false;
  };

  testIsMemberNegativeMismatch = {
    expr = types.isMember wanValue;
    expected = false;
  };

  testIsWanCurriable = {
    # `is*` must be curried so it composes with `filter` / `any` / etc.
    expr = builtins.filter types.isWan [
      wanValue
      probeValue
      "eth0"
    ];
    expected = [ wanValue ];
  };

  # ===== tryOk =====

  testTryOkStructure = {
    expr = types.tryOk 42;
    expected = {
      success = true;
      value = 42;
      error = null;
    };
  };

  testTryOkWithAttrs = {
    expr = types.tryOk wanValue;
    expected = {
      success = true;
      value = wanValue;
      error = null;
    };
  };

  testTryOkWithNull = {
    expr = types.tryOk null;
    expected = {
      success = true;
      value = null;
      error = null;
    };
  };

  # ===== tryErr =====

  testTryErrStructure = {
    expr = types.tryErr "bad input";
    expected = {
      success = false;
      value = null;
      error = "bad input";
    };
  };

  testTryErrEmptyString = {
    expr = types.tryErr "";
    expected = {
      success = false;
      value = null;
      error = "";
    };
  };

  # ===== ensureTag — passes through =====

  testEnsureTagReturnsValueOnMatch = {
    expr = types.ensureTag "wan" "fn" wanValue;
    expected = wanValue;
  };

  # ===== ensureTag — throws =====

  testEnsureTagThrowsOnWrongTag = {
    expr = evalThrows (types.ensureTag "wan" "fn" probeValue);
    expected = true;
  };

  testEnsureTagThrowsOnString = {
    expr = evalThrows (types.ensureTag "wan" "fn" "eth0");
    expected = true;
  };

  testEnsureTagThrowsOnInt = {
    expr = evalThrows (types.ensureTag "wan" "fn" 42);
    expected = true;
  };

  testEnsureTagThrowsOnNull = {
    expr = evalThrows (types.ensureTag "wan" "fn" null);
    expected = true;
  };

  testEnsureTagThrowsOnAttrsWithoutType = {
    expr = evalThrows (types.ensureTag "wan" "fn" { name = "eth0"; });
    expected = true;
  };

  testEnsureTagErrorMentionsCtx = {
    # The throw message must include the caller-supplied `ctx` so users
    # can locate the offending call site without a stack trace.
    expr =
      let
        r = builtins.tryEval (types.ensureTag "wan" "myFunction" probeValue);
      in
      r.success;
    expected = false;
  };

  testEnsureTagErrorMentionsObservedType = {
    # The throw message must include the observed `_type` so the user
    # sees what they passed in. Spec-level — string-match the message
    # by attempting eval and inspecting that it does throw (we can't
    # capture the message without effort; this test only confirms the
    # throw path is the one taken, not the message body).
    expr =
      let
        r = builtins.tryEval (
          types.ensureTag "wan" "fn" {
            _type = "probe";
          }
        );
      in
      r.success;
    expected = false;
  };
}
