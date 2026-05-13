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

  testMakeMinimalReturnsValue = {
    expr = builtins.isAttrs (member.make minimalInput);
    expected = true;
  };

  testMakeMinimalUsesDefaultWeight = {
    expr = (member.make minimalInput).weight;
    expected = 100;
  };

  testMakeMinimalUsesDefaultPriority = {
    expr = (member.make minimalInput).priority;
    expected = 1;
  };

  testMakeMinimalPreservesWan = {
    expr = (member.make minimalInput).wan;
    expected = "primary";
  };

  # ===== Happy path — full input =====

  testMakeFullPreservesAllFields = {
    expr = {
      inherit (member.make fullInput) wan weight priority;
    };
    expected = {
      wan = "backup";
      weight = 50;
      priority = 2;
    };
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

  # ===== toJSONValue =====

  testToJSONValueIncludesWan = {
    expr = (member.toJSONValue (member.make minimalInput)).wan;
    expected = "primary";
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
