/*
  Tests for `lib/internal/group.nix`, exposed as `wanwatch.group`.
  `skeleton.nix` covers the `make` / `tryMake` / `toJSONValue`
  contract; this suite covers defaults, members, and every error kind.
*/
{
  fixtures,
  helpers,
  lib,
  wanwatch,
  ...
}:
let
  inherit (fixtures) cases;
  inherit (fixtures.inputs.group) full minimal;
  inherit (helpers) getErrorKinds;
  inherit (wanwatch) group;

  groupFieldTests = helpers.fieldTests {
    inherit (group) tryMake;
    input = minimal;
  };

  withMembers = members: minimal // { inherit members; };
in
{
  defaults = {
    # `mark` and `table` are required, so only `strategy` has one.
    testValues = {
      expr = group.defaults;
      expected = {
        strategy = "primary-backup";
      };
    };

    testFillMinimalInput = {
      expr = (group.make minimal).strategy;
      expected = group.defaults.strategy;
    };
  };

  testParsesMembers = {
    expr = (group.make full).members;
    expected = {
      primary = {
        wan = "primary";
        priority = 1;
      };
      backup = {
        wan = "backup";
        priority = 2;
      };
    };
  };

  testMemberWanTakesAttributeKey = {
    expr =
      (group.make (withMembers {
        backup.priority = 2;
      })).members;
    expected.backup = {
      wan = "backup";
      priority = 2;
    };
  };

  testWansSortedByName = {
    expr = group.wans (group.make full);
    expected = [
      "backup"
      "primary"
    ];
  };

  fields = {
    name = groupFieldTests "name" "groupInvalidName" cases.identifiers;
    strategy = groupFieldTests "strategy" "groupInvalidStrategy" cases.strategies;
    table = groupFieldTests "table" "groupInvalidTable" cases.markTableIds;
    mark = groupFieldTests "mark" "groupInvalidMark" cases.markTableIds;
  };

  rejections = helpers.rejectionTests group.tryMake {
    groupInvalidName.missingName = removeAttrs minimal [ "name" ];
    groupNoMembers = {
      missingMembers = removeAttrs minimal [ "members" ];
      emptyMembers = withMembers { };
    };
    groupInvalidMember = {
      membersNotAnAttrset = withMembers "primary";
      membersAsList = withMembers [ { wan = "primary"; } ];
      wanDisagreesWithKey = withMembers { primary.wan = "backup"; };
    };
    groupInvalidTable.missingTable = removeAttrs minimal [ "table" ];
    groupInvalidMark.missingMark = removeAttrs minimal [ "mark" ];
  };

  # WANs, Groups, and Members report invalid identifiers alike.
  testInvalidNameMessage = {
    expr = (group.tryMake (minimal // { name = "1bad"; })).error;
    expected = "group.make: [groupInvalidName] name must be a valid wanwatch identifier (matching [a-zA-Z][a-zA-Z0-9-]*); got \"1bad\"";
  };

  # The Member's own error kinds follow the wrapping kind.
  testForwardsMemberErrors = {
    expr = getErrorKinds (
      group.tryMake (withMembers {
        "1bad" = { };
      })
    );
    expected = [
      "groupInvalidMember"
      "memberInvalidWan"
    ];
  };

  # Before the Member rejected a non-attrset input itself, `5`
  # reported `memberInvalidWan`.
  testForwardsNonAttrsetMember = {
    expr = getErrorKinds (
      group.tryMake (withMembers {
        primary = 5;
      })
    );
    expected = [
      "groupInvalidMember"
      "memberNotAnAttrset"
    ];
  };

  testReportsEachInvalidMember = {
    expr =
      let
        result = group.tryMake (withMembers {
          "1bad" = { };
          primary.priority = 0;
        });
      in
      {
        kinds = getErrorKinds result;
        namesBoth = lib.all (key: lib.hasInfix "members.${key}:" result.error) [
          "1bad"
          "primary"
        ];
      };
    expected = {
      kinds = [
        "groupInvalidMember"
        "memberInvalidWan"
        "groupInvalidMember"
        "memberInvalidPriority"
      ];
      namesBoth = true;
    };
  };

  # A Member that fails to parse reports only its own errors, not a
  # disagreement between its `wan` and its key.
  testSkipsKeyCheckForInvalidMembers = {
    expr = getErrorKinds (
      group.tryMake (withMembers {
        primary.wan = "1bad";
      })
    );
    expected = [
      "groupInvalidMember"
      "memberInvalidWan"
    ];
  };

  testReportsEveryViolation = {
    expr = getErrorKinds (
      group.tryMake {
        name = "1bad";
        members = "primary";
        strategy = "huh";
        table = -1;
        mark = -1;
      }
    );
    expected = [
      "groupInvalidName"
      "groupInvalidMember"
      "groupInvalidStrategy"
      "groupInvalidTable"
      "groupInvalidMark"
    ];
  };
}
