/*
  Unit tests for `lib/internal/group.nix` (exposed as
  `wanwatch.group`). Same `testFoo = { expr; expected; }` shape as
  every other unit test; aggregated by `tests/unit/default.nix`.

  Coverage discipline per PLAN.md §9.1: every public function
  exercised on positive and negative inputs; every error kind
  triggered in isolation; the duplicate-member cross-check exercised
  with both single and multi-duplicate cases.
*/
{ pkgs, libnet, ... }:
let
  wanwatch = import ../../../lib {
    inherit (pkgs) lib;
    inherit libnet;
  };
  inherit (wanwatch) group;

  helpers = import ../helpers.nix { inherit pkgs; };
  inherit (helpers) evalThrows errorMatches;
  tryError = helpers.tryError group;

  minimalInput = {
    name = "home-uplink";
    members = [
      {
        wan = "primary";
        priority = 1;
      }
    ];
  };

  fullInput = {
    name = "guest-uplink";
    members = [
      {
        wan = "primary";
        weight = 100;
        priority = 1;
      }
      {
        wan = "backup";
        weight = 50;
        priority = 2;
      }
    ];
    strategy = "primary-backup";
    table = 100;
    mark = 100;
  };
in
{
  # ===== Happy path =====

  testMakeMinimalReturnsTaggedValue = {
    expr = (group.make minimalInput)._type;
    expected = "group";
  };

  testMakeMinimalUsesDefaultStrategy = {
    expr = group.strategy (group.make minimalInput);
    expected = "primary-backup";
  };

  testMakeMinimalTableDefaultsNull = {
    # Null means "auto-allocated" — `internal.tables.allocate` fills
    # it in at a later phase.
    expr = group.table (group.make minimalInput);
    expected = null;
  };

  testMakeMinimalMarkDefaultsNull = {
    expr = group.mark (group.make minimalInput);
    expected = null;
  };

  testMakeFullPreservesAllFields = {
    expr = {
      name = group.name (group.make fullInput);
      strategy = group.strategy (group.make fullInput);
      table = group.table (group.make fullInput);
      mark = group.mark (group.make fullInput);
    };
    expected = {
      name = "guest-uplink";
      strategy = "primary-backup";
      table = 100;
      mark = 100;
    };
  };

  testMembersParsedToMemberValues = {
    expr = builtins.map (m: m._type) (group.members (group.make fullInput));
    expected = [
      "member"
      "member"
    ];
  };

  testWansAccessorReturnsNameList = {
    expr = group.wans (group.make fullInput);
    expected = [
      "primary"
      "backup"
    ];
  };

  # ===== Predicate: isGroup =====

  testIsGroupOnGroup = {
    expr = group.isGroup (group.make minimalInput);
    expected = true;
  };

  testIsGroupOnMember = {
    expr = group.isGroup (wanwatch.member.make { wan = "primary"; });
    expected = false;
  };

  testIsGroupOnRawAttrs = {
    expr = group.isGroup { name = "home"; };
    expected = false;
  };

  testIsGroupOnString = {
    expr = group.isGroup "home";
    expected = false;
  };

  # ===== Error: groupInvalidName =====

  testRejectsMissingName = {
    expr = errorMatches "groupInvalidName" (tryError {
      members = [ { wan = "primary"; } ];
    });
    expected = true;
  };

  testRejectsEmptyName = {
    expr = errorMatches "groupInvalidName" (tryError (minimalInput // { name = ""; }));
    expected = true;
  };

  testRejectsLeadingDigitName = {
    expr = errorMatches "groupInvalidName" (tryError (minimalInput // { name = "1bad"; }));
    expected = true;
  };

  # ===== Error: groupNoMembers =====

  testRejectsMissingMembers = {
    expr = errorMatches "groupNoMembers" (tryError {
      name = "home";
    });
    expected = true;
  };

  testRejectsEmptyMembers = {
    expr = errorMatches "groupNoMembers" (tryError (minimalInput // { members = [ ]; }));
    expected = true;
  };

  # ===== Error: groupInvalidMember =====

  testRejectsBadMember = {
    # Member with invalid wan gets forwarded as groupInvalidMember.
    expr = errorMatches "groupInvalidMember" (
      tryError (
        minimalInput
        // {
          members = [
            {
              wan = "1bad";
            }
          ];
        }
      )
    );
    expected = true;
  };

  testRejectsMembersNotAList = {
    expr = errorMatches "groupInvalidMember" (tryError (minimalInput // { members = "primary"; }));
    expected = true;
  };

  # ===== Error: groupDuplicateMember =====

  testRejectsDuplicateMember = {
    # Same wan referenced twice — must reject.
    expr = errorMatches "groupDuplicateMember" (
      tryError (
        minimalInput
        // {
          members = [
            {
              wan = "primary";
              priority = 1;
            }
            {
              wan = "primary";
              priority = 2;
            }
          ];
        }
      )
    );
    expected = true;
  };

  testDetectsMultipleDuplicates = {
    # Two different wans each appearing twice → both reported.
    expr =
      let
        err = tryError (
          minimalInput
          // {
            members = [
              {
                wan = "primary";
                priority = 1;
              }
              {
                wan = "backup";
                priority = 2;
              }
              {
                wan = "primary";
                priority = 3;
              }
              {
                wan = "backup";
                priority = 4;
              }
            ];
          }
        );
      in
      errorMatches "groupDuplicateMember" err
      && pkgs.lib.hasInfix "primary" err
      && pkgs.lib.hasInfix "backup" err;
    expected = true;
  };

  testDuplicateCheckSkippedWhenMemberInvalid = {
    # If any member is itself invalid, the duplicate check is
    # skipped to avoid spurious follow-on errors.
    expr =
      let
        err = tryError (
          minimalInput
          // {
            members = [
              {
                wan = "primary";
                priority = 1;
              }
              {
                wan = "1bad";
                priority = 2;
              }
              {
                wan = "primary";
                priority = 3;
              }
            ];
          }
        );
      in
      errorMatches "groupInvalidMember" err && !(errorMatches "groupDuplicateMember" err);
    expected = true;
  };

  # ===== Error: groupInvalidStrategy =====

  testRejectsUnknownStrategy = {
    expr = errorMatches "groupInvalidStrategy" (
      tryError (minimalInput // { strategy = "round-robin"; })
    );
    expected = true;
  };

  testAcceptsPrimaryBackup = {
    expr = (group.tryMake (minimalInput // { strategy = "primary-backup"; })).success;
    expected = true;
  };

  # ===== Error: groupInvalidTable =====

  testRejectsZeroTable = {
    expr = errorMatches "groupInvalidTable" (tryError (minimalInput // { table = 0; }));
    expected = true;
  };

  testRejectsNegativeTable = {
    expr = errorMatches "groupInvalidTable" (tryError (minimalInput // { table = -1; }));
    expected = true;
  };

  testAcceptsNullTable = {
    expr = (group.tryMake (minimalInput // { table = null; })).success;
    expected = true;
  };

  # ===== Error: groupInvalidMark =====

  testRejectsZeroMark = {
    expr = errorMatches "groupInvalidMark" (tryError (minimalInput // { mark = 0; }));
    expected = true;
  };

  testAcceptsNullMark = {
    expr = (group.tryMake (minimalInput // { mark = null; })).success;
    expected = true;
  };

  # ===== Multi-error aggregation =====

  testMultipleErrorsAggregated = {
    expr =
      let
        err = tryError {
          name = "1bad";
          members = [ { wan = "1also-bad"; } ];
          strategy = "huh";
          table = -1;
        };
        kinds = [
          "groupInvalidName"
          "groupInvalidMember"
          "groupInvalidStrategy"
          "groupInvalidTable"
        ];
      in
      builtins.all (k: errorMatches k err) kinds;
    expected = true;
  };

  # ===== make / tryMake contract =====

  testMakeThrowsOnInvalid = {
    expr = evalThrows (group.make { name = ""; });
    expected = true;
  };

  testTryMakeOkOnValid = {
    expr = (group.tryMake minimalInput).success;
    expected = true;
  };

  testTryMakeErrOnInvalid = {
    expr = (group.tryMake { name = ""; }).success;
    expected = false;
  };

  testTryMakeErrorNullOnSuccess = {
    expr = (group.tryMake minimalInput).error;
    expected = null;
  };

  testTryMakeValueNullOnFailure = {
    expr = (group.tryMake { name = ""; }).value;
    expected = null;
  };

  # ===== Equality =====

  testEqSameInput = {
    expr = group.eq (group.make minimalInput) (group.make minimalInput);
    expected = true;
  };

  testEqDifferentName = {
    expr = group.eq (group.make minimalInput) (group.make (minimalInput // { name = "other"; }));
    expected = false;
  };

  testEqDifferentMembers = {
    expr = group.eq (group.make minimalInput) (
      group.make (
        minimalInput
        // {
          members = [
            {
              wan = "backup";
              priority = 1;
            }
          ];
        }
      )
    );
    expected = false;
  };

  # ===== Comparison =====

  testCompareEqualReturnsZero = {
    expr = group.compare (group.make minimalInput) (group.make minimalInput);
    expected = 0;
  };

  testCompareTrichotomy = {
    expr =
      let
        a = group.make minimalInput;
        b = group.make (minimalInput // { name = "zzz"; });
        c = group.compare a b;
      in
      c == -1 || c == 1;
    expected = true;
  };

  testCompareAntisymmetry = {
    expr =
      let
        a = group.make minimalInput;
        b = group.make (minimalInput // { name = "zzz"; });
      in
      group.compare a b == -(group.compare b a);
    expected = true;
  };

  # ===== Derived ordering =====

  testLtDerived = {
    expr =
      let
        a = group.make minimalInput;
        b = group.make (minimalInput // { name = "zzz"; });
      in
      group.lt a b;
    expected = true;
  };

  testMinReturnsLesser = {
    expr =
      let
        a = group.make minimalInput;
        b = group.make (minimalInput // { name = "zzz"; });
      in
      group.min a b == a;
    expected = true;
  };

  testMaxReturnsGreater = {
    expr =
      let
        a = group.make minimalInput;
        b = group.make (minimalInput // { name = "zzz"; });
      in
      group.max a b == b;
    expected = true;
  };

  # ===== toJSON =====

  testToJSONReturnsString = {
    expr = builtins.isString (group.toJSON (group.make minimalInput));
    expected = true;
  };

  testToJSONIncludesTypeTag = {
    expr = pkgs.lib.hasInfix "\"_type\":\"group\"" (group.toJSON (group.make minimalInput));
    expected = true;
  };

  testToJSONEmbedsMembersAsObjects = {
    expr = pkgs.lib.hasInfix "\"members\":[{" (group.toJSON (group.make minimalInput));
    expected = true;
  };

  testToJSONEmitsNullTable = {
    expr = pkgs.lib.hasInfix "\"table\":null" (group.toJSON (group.make minimalInput));
    expected = true;
  };

  # ===== Defaults exposed =====

  testDefaultsExposed = {
    expr = group.defaults;
    expected = {
      strategy = "primary-backup";
      table = null;
      mark = null;
    };
  };
}
