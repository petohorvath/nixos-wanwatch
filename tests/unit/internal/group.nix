/*
  Unit tests for `lib/internal/group.nix`, exposed as `wanwatch.group`.
  Per AGENTS.md, each public function is exercised on positive and
  negative inputs and each error kind is triggered alone. The
  duplicate-member check covers single and multiple duplicates;
  `table` and `mark` cover their [1000, 32767] bounds and absence.
*/
{
  helpers,
  pkgs,
  wanwatch,
  ...
}:
let
  inherit (pkgs) lib;
  inherit (helpers) errorMatches evalThrows;
  inherit (wanwatch) group;

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

  testGroupMakeMinimalReturnsValue = {
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

  testGroupMakeFullPreservesAllFields = {
    expr = {
      inherit (group.make fullInput)
        mark
        name
        strategy
        table
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

  testGroupRejectsMissingName = {
    expr = errorMatches "groupInvalidName" (tryError {
      members = [ { wan = "primary"; } ];
      mark = 1000;
      table = 1000;
    });
    expected = true;
  };

  testGroupRejectsEmptyName = {
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
        error = tryError (
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
      errorMatches "groupDuplicateMember" error
      && lib.hasInfix "primary" error
      && lib.hasInfix "backup" error;
    expected = true;
  };

  testDuplicateCheckSkippedWhenMemberInvalid = {
    expr =
      let
        error = tryError (
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
      errorMatches "groupInvalidMember" error && !(errorMatches "groupDuplicateMember" error);
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
    # A missing table defaults to null, which fails the range check.
    expr = errorMatches "groupInvalidTable" (tryError (removeAttrs minimalInput [ "table" ]));
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
    # 999 sits just below the [1000, 32767] floor.
    expr = errorMatches "groupInvalidTable" (tryError (minimalInput // { table = 999; }));
    expected = true;
  };

  testRejectsTooHighTable = {
    expr = errorMatches "groupInvalidTable" (tryError (minimalInput // { table = 32768; }));
    expected = true;
  };

  testRejectsKernelReservedTable = {
    # 254 is the main table. The 1000 floor rejects it; pin it so a
    # future range change cannot silently re-admit it.
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
    expr = errorMatches "groupInvalidMark" (tryError (removeAttrs minimalInput [ "mark" ]));
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

  testGroupMultipleErrorsAggregated = {
    expr =
      let
        error = tryError {
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
      builtins.all (kind: errorMatches kind error) kinds;
    expected = true;
  };

  # ===== make / tryMake contract =====

  testGroupMakeThrowsOnInvalid = {
    expr = evalThrows (group.make { name = ""; });
    expected = true;
  };

  testGroupTryMakeOkOnValid = {
    expr = (group.tryMake minimalInput).success;
    expected = true;
  };

  testGroupTryMakeErrOnInvalid = {
    expr = (group.tryMake { name = ""; }).success;
    expected = false;
  };

  testGroupTryMakeErrorNullOnSuccess = {
    expr = (group.tryMake minimalInput).error;
    expected = null;
  };

  testGroupTryMakeValueNullOnFailure = {
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

  testGroupDefaultsExposed = {
    # Only `strategy` has a default; table and mark are required.
    expr = group.defaults;
    expected = {
      strategy = "primary-backup";
    };
  };

  # ===== Round-trip =====

  testGroupRoundTrip = {
    # AGENTS.md (5): re-emitting the JSON shape after a second
    # `make` must be byte-identical to the first, nested members
    # included.
    expr =
      let
        firstJSON = group.toJSONValue (group.make minimalInput);
        secondJSON = group.toJSONValue (group.make firstJSON);
      in
      firstJSON == secondJSON;
    expected = true;
  };
}
