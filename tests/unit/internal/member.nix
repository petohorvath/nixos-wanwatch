/*
  Unit tests for `lib/internal/member.nix` (exposed as
  `wanwatch.member`). Same `testFoo = { expr; expected; }` shape as
  every other unit test; aggregated by `tests/unit/default.nix`.

  Coverage discipline per PLAN.md §9.1: every public function
  exercised on positive and negative inputs; every error kind
  triggered in isolation and at least one aggregated multi-error
  case; the §5.1 API skeleton fully exercised.
*/
{ pkgs, libnet, ... }:
let
  wanwatch = import ../../../lib {
    inherit (pkgs) lib;
    inherit libnet;
  };
  inherit (wanwatch) member;

  helpers = import ../helpers.nix { inherit pkgs; };
  inherit (helpers) evalThrows errorMatches;
  tryError = helpers.tryError member;

  minimalInput = {
    wan = "primary";
  };

  fullInput = {
    wan = "backup";
    weight = 50;
    priority = 2;
  };
in
{
  # ===== Happy path — minimal input =====

  testMakeMinimalReturnsTaggedValue = {
    expr = (member.make minimalInput)._type;
    expected = "member";
  };

  testMakeMinimalUsesDefaultWeight = {
    expr = member.weight (member.make minimalInput);
    expected = 100;
  };

  testMakeMinimalUsesDefaultPriority = {
    expr = member.priority (member.make minimalInput);
    expected = 1;
  };

  testMakeMinimalPreservesWan = {
    expr = member.wan (member.make minimalInput);
    expected = "primary";
  };

  # ===== Happy path — full input =====

  testMakeFullPreservesAllFields = {
    expr = {
      wan = member.wan (member.make fullInput);
      weight = member.weight (member.make fullInput);
      priority = member.priority (member.make fullInput);
    };
    expected = {
      wan = "backup";
      weight = 50;
      priority = 2;
    };
  };

  # ===== Predicate: isMember =====

  testIsMemberOnMember = {
    expr = member.isMember (member.make minimalInput);
    expected = true;
  };

  testIsMemberOnRawAttrs = {
    expr = member.isMember { wan = "primary"; };
    expected = false;
  };

  testIsMemberOnProbe = {
    expr = member.isMember (wanwatch.probe.make { targets = [ "1.1.1.1" ]; });
    expected = false;
  };

  testIsMemberOnString = {
    expr = member.isMember "primary";
    expected = false;
  };

  # ===== Error: memberInvalidWan =====

  testRejectsMissingWan = {
    expr = errorMatches "memberInvalidWan" (tryError { });
    expected = true;
  };

  testRejectsEmptyWan = {
    expr = errorMatches "memberInvalidWan" (tryError {
      wan = "";
    });
    expected = true;
  };

  testRejectsLeadingDigitWan = {
    expr = errorMatches "memberInvalidWan" (tryError {
      wan = "1primary";
    });
    expected = true;
  };

  testRejectsSpaceInWan = {
    expr = errorMatches "memberInvalidWan" (tryError {
      wan = "two words";
    });
    expected = true;
  };

  testAcceptsHyphenatedWan = {
    expr = (member.tryMake { wan = "home-uplink"; }).success;
    expected = true;
  };

  # ===== Error: memberInvalidWeight =====

  testRejectsZeroWeight = {
    expr = errorMatches "memberInvalidWeight" (tryError (minimalInput // { weight = 0; }));
    expected = true;
  };

  testRejectsNegativeWeight = {
    expr = errorMatches "memberInvalidWeight" (tryError (minimalInput // { weight = -1; }));
    expected = true;
  };

  testRejectsStringWeight = {
    expr = errorMatches "memberInvalidWeight" (tryError (minimalInput // { weight = "high"; }));
    expected = true;
  };

  # ===== Error: memberInvalidPriority =====

  testRejectsZeroPriority = {
    expr = errorMatches "memberInvalidPriority" (tryError (minimalInput // { priority = 0; }));
    expected = true;
  };

  testRejectsNegativePriority = {
    expr = errorMatches "memberInvalidPriority" (tryError (minimalInput // { priority = -5; }));
    expected = true;
  };

  # ===== Multi-error aggregation =====

  testMultipleErrorsAggregated = {
    expr =
      let
        err = tryError {
          wan = "1bad";
          weight = 0;
          priority = -1;
        };
        kinds = [
          "memberInvalidWan"
          "memberInvalidWeight"
          "memberInvalidPriority"
        ];
      in
      builtins.all (k: errorMatches k err) kinds;
    expected = true;
  };

  # ===== make throws =====

  testMakeThrowsOnInvalid = {
    expr = evalThrows (member.make { wan = ""; });
    expected = true;
  };

  # ===== tryMake contract =====

  testTryMakeOkOnValid = {
    expr = (member.tryMake minimalInput).success;
    expected = true;
  };

  testTryMakeErrOnInvalid = {
    expr = (member.tryMake { wan = ""; }).success;
    expected = false;
  };

  testTryMakeErrorNullOnSuccess = {
    expr = (member.tryMake minimalInput).error;
    expected = null;
  };

  testTryMakeValueNullOnFailure = {
    expr = (member.tryMake { wan = ""; }).value;
    expected = null;
  };

  # ===== Equality =====

  testEqSameInput = {
    expr = member.eq (member.make minimalInput) (member.make minimalInput);
    expected = true;
  };

  testEqEquivalentInputs = {
    expr = member.eq (member.make minimalInput) (member.make (minimalInput // member.defaults));
    expected = true;
  };

  testEqDifferentWan = {
    expr = member.eq (member.make { wan = "a"; }) (member.make { wan = "b"; });
    expected = false;
  };

  testEqDifferentPriority = {
    expr = member.eq (member.make minimalInput) (member.make (minimalInput // { priority = 2; }));
    expected = false;
  };

  # ===== Comparison =====

  testCompareEqualReturnsZero = {
    expr = member.compare (member.make minimalInput) (member.make minimalInput);
    expected = 0;
  };

  testCompareTrichotomy = {
    expr =
      let
        a = member.make { wan = "aaa"; };
        b = member.make { wan = "zzz"; };
        c = member.compare a b;
      in
      c == -1 || c == 1;
    expected = true;
  };

  testCompareAntisymmetry = {
    expr =
      let
        a = member.make { wan = "aaa"; };
        b = member.make { wan = "zzz"; };
      in
      member.compare a b == -(member.compare b a);
    expected = true;
  };

  # ===== Derived ordering =====

  testLtDerived = {
    expr =
      let
        a = member.make { wan = "aaa"; };
        b = member.make { wan = "zzz"; };
      in
      member.lt a b;
    expected = true;
  };

  testMinReturnsLesser = {
    expr =
      let
        a = member.make { wan = "aaa"; };
        b = member.make { wan = "zzz"; };
      in
      member.min a b == a;
    expected = true;
  };

  testMaxReturnsGreater = {
    expr =
      let
        a = member.make { wan = "aaa"; };
        b = member.make { wan = "zzz"; };
      in
      member.max a b == b;
    expected = true;
  };

  # ===== toJSON =====

  testToJSONReturnsString = {
    expr = builtins.isString (member.toJSON (member.make minimalInput));
    expected = true;
  };

  testToJSONIncludesTypeTag = {
    expr = pkgs.lib.hasInfix "\"_type\":\"member\"" (member.toJSON (member.make minimalInput));
    expected = true;
  };

  testToJSONIncludesWan = {
    expr = pkgs.lib.hasInfix "\"wan\":\"primary\"" (member.toJSON (member.make minimalInput));
    expected = true;
  };

  # ===== Defaults exposed =====

  testDefaultsExposed = {
    expr = member.defaults;
    expected = {
      weight = 100;
      priority = 1;
    };
  };
}
