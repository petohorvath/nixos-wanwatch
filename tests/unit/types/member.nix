# Unit tests for `lib/types/member.nix`.
{ pkgs, libnet, ... }:
let
  wanwatch = import ../../../lib {
    inherit (pkgs) lib;
    inherit libnet;
  };
  inherit (wanwatch) types;

  helpers = import ../helpers.nix { inherit pkgs; };
  inherit (helpers) evalType evalTypeFails;
in
{
  # ===== leaf types =====

  testMemberWanAcceptsIdentifier = {
    expr = evalType types.memberWan "primary";
    expected = "primary";
  };

  testMemberWanRejectsLeadingDigit = {
    expr = evalTypeFails types.memberWan "1bad";
    expected = true;
  };

  testMemberWeightAcceptsPositive = {
    expr = evalType types.memberWeight 50;
    expected = 50;
  };

  testMemberWeightRejectsZero = {
    expr = evalTypeFails types.memberWeight 0;
    expected = true;
  };

  testMemberPriorityAcceptsPositive = {
    expr = evalType types.memberPriority 2;
    expected = 2;
  };

  # ===== top-level submodule — defaults =====

  testMemberMinimalFillsDefaults = {
    expr = evalType types.member { wan = "primary"; };
    expected = {
      wan = "primary";
      weight = 100;
      priority = 1;
    };
  };

  testMemberPreservesFullSpec = {
    expr = evalType types.member {
      wan = "backup";
      weight = 50;
      priority = 2;
    };
    expected = {
      wan = "backup";
      weight = 50;
      priority = 2;
    };
  };

  testMemberRejectsBadWan = {
    expr = evalTypeFails types.member { wan = "1bad"; };
    expected = true;
  };

  testMemberRejectsZeroWeight = {
    expr = evalTypeFails types.member {
      wan = "primary";
      weight = 0;
    };
    expected = true;
  };

  testMemberRejectsZeroPriority = {
    expr = evalTypeFails types.member {
      wan = "primary";
      priority = 0;
    };
    expected = true;
  };
}
