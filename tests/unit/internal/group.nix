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
  inherit (wanwatch) group member;

  groupFieldTests = helpers.fieldTests {
    inherit (group) tryMake;
    input = minimal;
  };

  withMembers = wans: minimal // { members = map (wan: { inherit wan; }) wans; };
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
    expected = map member.make full.members;
  };

  testWansInMemberOrder = {
    expr = group.wans (group.make full);
    expected = [
      "primary"
      "backup"
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
      emptyMembers = withMembers [ ];
    };
    groupInvalidMember.membersNotAList = minimal // {
      members = "primary";
    };
    groupDuplicateMember.repeatedWan = withMembers [
      "primary"
      "primary"
    ];
    groupInvalidTable.missingTable = removeAttrs minimal [ "table" ];
    groupInvalidMark.missingMark = removeAttrs minimal [ "mark" ];
  };

  # The Member's own error kinds follow the wrapping kind.
  testForwardsMemberErrors = {
    expr = getErrorKinds (group.tryMake (withMembers [ "1bad" ]));
    expected = [
      "groupInvalidMember"
      "memberInvalidWan"
    ];
  };

  testReportsEachDuplicateWan = {
    expr =
      let
        result = group.tryMake (withMembers [
          "primary"
          "backup"
          "primary"
          "backup"
        ]);
      in
      {
        kinds = getErrorKinds result;
        namesBoth = lib.all (wan: lib.hasInfix "'${wan}'" result.error) [
          "primary"
          "backup"
        ];
      };
    expected = {
      kinds = [
        "groupDuplicateMember"
        "groupDuplicateMember"
      ];
      namesBoth = true;
    };
  };

  # An invalid member has no reliable WAN name, so duplicates go
  # unchecked until every member parses.
  testSkipsDuplicateCheckForInvalidMembers = {
    expr = getErrorKinds (
      group.tryMake (withMembers [
        "primary"
        "1bad"
        "primary"
      ])
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
