/*
  Unit tests for `lib/internal/group.nix` (exposed as
  `wanwatch.group`). Same `testFoo = { expr; expected; }` shape as
  every other unit test; aggregated by `tests/unit/default.nix`.

  Coverage discipline per PLAN.md §9.1: every public function
  exercised on positive and negative inputs; every error kind
  triggered in isolation; the duplicate-member cross-check exercised
  with both single and multi-duplicate cases. `table` and `mark` are
  required integers in [1000, 32767]; both the boundary cases and
  the "missing" case have their own tests.
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
    mark = 1000;
    table = 1000;
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
    table = 1001;
    mark = 1001;
  };
in
{
  # ===== Happy path =====

  testMakeMinimalReturnsValue = {
    expr = builtins.isAttrs (group.make minimalInput);
    expected = true;
  };

  testMakeMinimalUsesDefaultStrategy = {
    expr = (group.make minimalInput).strategy;
    expected = "primary-backup";
  };

  testMakeMinimalPreservesMarkTable = {
    expr = {
      inherit (group.make minimalInput) mark table;
    };
    expected = {
      mark = 1000;
      table = 1000;
    };
  };

  testMakeFullPreservesAllFields = {
    expr = {
      inherit (group.make fullInput)
        name
        strategy
        table
        mark
        ;
    };
    expected = {
      name = "guest-uplink";
      strategy = "primary-backup";
      table = 1001;
      mark = 1001;
    };
  };

  testMembersParsedToMemberValues = {
    expr = builtins.all builtins.isAttrs (group.make fullInput).members;
    expected = true;
  };

  testWansAccessorReturnsNameList = {
    expr = group.wans (group.make fullInput);
    expected = [
      "primary"
      "backup"
    ];
  };

  # ===== Error: groupInvalidName =====

  testRejectsMissingName = {
    expr = errorMatches "groupInvalidName" (tryError {
      members = [ { wan = "primary"; } ];
      mark = 1000;
      table = 1000;
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
      mark = 1000;
      table = 1000;
    });
    expected = true;
  };

  testRejectsEmptyMembers = {
    expr = errorMatches "groupNoMembers" (tryError (minimalInput // { members = [ ]; }));
    expected = true;
  };

  # ===== Error: groupInvalidMember =====

  testRejectsBadMember = {
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

  testRejectsMissingTable = {
    # No `table` at all → null → out of range → groupInvalidTable.
    expr = errorMatches "groupInvalidTable" (tryError (builtins.removeAttrs minimalInput [ "table" ]));
    expected = true;
  };

  testRejectsZeroTable = {
    expr = errorMatches "groupInvalidTable" (tryError (minimalInput // { table = 0; }));
    expected = true;
  };

  testRejectsNegativeTable = {
    expr = errorMatches "groupInvalidTable" (tryError (minimalInput // { table = -1; }));
    expected = true;
  };

  testRejectsTooLowTable = {
    # 999 is below the [1000, 32767] floor — buries small-integer
    # scripts but still rejects 999.
    expr = errorMatches "groupInvalidTable" (tryError (minimalInput // { table = 999; }));
    expected = true;
  };

  testRejectsTooHighTable = {
    expr = errorMatches "groupInvalidTable" (tryError (minimalInput // { table = 32768; }));
    expected = true;
  };

  testRejectsKernelReservedTable = {
    # 254 = main. The 1000 floor catches this; pin it explicitly so
    # a future range change doesn't silently re-admit it.
    expr = errorMatches "groupInvalidTable" (tryError (minimalInput // { table = 254; }));
    expected = true;
  };

  testAcceptsTableLowerBound = {
    expr = (group.tryMake (minimalInput // { table = 1000; })).success;
    expected = true;
  };

  testAcceptsTableUpperBound = {
    expr = (group.tryMake (minimalInput // { table = 32767; })).success;
    expected = true;
  };

  # ===== Error: groupInvalidMark =====

  testRejectsMissingMark = {
    expr = errorMatches "groupInvalidMark" (tryError (builtins.removeAttrs minimalInput [ "mark" ]));
    expected = true;
  };

  testRejectsZeroMark = {
    expr = errorMatches "groupInvalidMark" (tryError (minimalInput // { mark = 0; }));
    expected = true;
  };

  testRejectsTooLowMark = {
    expr = errorMatches "groupInvalidMark" (tryError (minimalInput // { mark = 999; }));
    expected = true;
  };

  testRejectsTooHighMark = {
    expr = errorMatches "groupInvalidMark" (tryError (minimalInput // { mark = 32768; }));
    expected = true;
  };

  testAcceptsMarkLowerBound = {
    expr = (group.tryMake (minimalInput // { mark = 1000; })).success;
    expected = true;
  };

  testAcceptsMarkUpperBound = {
    expr = (group.tryMake (minimalInput // { mark = 32767; })).success;
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
          mark = -1;
        };
        kinds = [
          "groupInvalidName"
          "groupInvalidMember"
          "groupInvalidStrategy"
          "groupInvalidTable"
          "groupInvalidMark"
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

  # ===== toJSONValue =====

  testToJSONValueEmbedsMembersAsAttrsets = {
    expr = builtins.isAttrs (builtins.head (group.toJSONValue (group.make minimalInput)).members);
    expected = true;
  };

  testToJSONValueEmitsUserTable = {
    expr = (group.toJSONValue (group.make minimalInput)).table;
    expected = 1000;
  };

  testToJSONValueEmitsUserMark = {
    expr = (group.toJSONValue (group.make minimalInput)).mark;
    expected = 1000;
  };

  # ===== Defaults exposed =====

  testDefaultsExposed = {
    # Only `strategy` has a default now — table and mark are
    # user-required, so removing them from `defaults` enforces "no
    # null sentinel" by construction.
    expr = group.defaults;
    expected = {
      strategy = "primary-backup";
    };
  };

  # ===== Round-trip =====

  testRoundTrip = {
    # PLAN §9.1 (5): re-emitting the JSON shape after a second
    # `make` must be byte-identical to the first. Nested members
    # round-trip via member.toJSONValue / member.make, so this
    # covers the group ∘ member composition.
    expr =
      let
        js1 = group.toJSONValue (group.make minimalInput);
        js2 = group.toJSONValue (group.make js1);
      in
      js1 == js2;
    expected = true;
  };
}
