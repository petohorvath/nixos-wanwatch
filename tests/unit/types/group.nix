/*
  Unit tests for `lib/types/group.nix`: only what the type system
  enforces. `tests/unit/internal/group.nix` covers cross-field
  invariants such as non-empty members and duplicate WAN references.
*/
{ helpers, wanwatch, ... }:
let
  inherit (helpers) evalType evalTypeFails;
  inherit (wanwatch) types;

  # `mark` and `table` are required. `name` is read-only and defaults
  # to the attribute key; `evalType` declares the option as `value`,
  # so the submodule sees "value".
  baseConfig = {
    members = [
      {
        wan = "primary";
        priority = 1;
      }
    ];
    mark = 1000;
    table = 1000;
  };
in
{
  # ===== leaf types =====

  testGroupNameAcceptsIdentifier = {
    expr = evalType types.groupName "home-uplink";
    expected = "home-uplink";
  };

  testGroupNameRejectsBad = {
    expr = evalTypeFails types.groupName "1bad";
    expected = true;
  };

  testGroupStrategyAcceptsPrimaryBackup = {
    expr = evalType types.groupStrategy "primary-backup";
    expected = "primary-backup";
  };

  testGroupStrategyRejectsUnknown = {
    expr = evalTypeFails types.groupStrategy "round-robin";
    expected = true;
  };

  testGroupTableAcceptsLowerBound = {
    expr = evalType types.groupTable 1000;
    expected = 1000;
  };

  testGroupTableAcceptsUpperBound = {
    expr = evalType types.groupTable 32767;
    expected = 32767;
  };

  testGroupTableRejectsNull = {
    # Every group must declare an integer table.
    expr = evalTypeFails types.groupTable null;
    expected = true;
  };

  testGroupTableRejectsZero = {
    expr = evalTypeFails types.groupTable 0;
    expected = true;
  };

  testGroupTableRejectsBelowRange = {
    expr = evalTypeFails types.groupTable 999;
    expected = true;
  };

  testGroupTableRejectsAboveRange = {
    expr = evalTypeFails types.groupTable 32768;
    expected = true;
  };

  testGroupMarkAcceptsLowerBound = {
    expr = evalType types.groupMark 1000;
    expected = 1000;
  };

  testGroupMarkAcceptsUpperBound = {
    expr = evalType types.groupMark 32767;
    expected = 32767;
  };

  testGroupMarkRejectsNull = {
    expr = evalTypeFails types.groupMark null;
    expected = true;
  };

  testGroupMarkRejectsZero = {
    expr = evalTypeFails types.groupMark 0;
    expected = true;
  };

  # ===== top-level submodule — defaults =====

  testGroupMinimalShape = {
    expr =
      let
        group = evalType types.group baseConfig;
      in
      {
        inherit (group)
          mark
          name
          strategy
          table
          ;
        memberCount = builtins.length group.members;
        firstMemberWan = (builtins.head group.members).wan;
      };
    expected = {
      name = "value"; # derived from `options.value` in `evalType`
      strategy = "primary-backup";
      table = 1000;
      mark = 1000;
      memberCount = 1;
      firstMemberWan = "primary";
    };
  };

  testGroupMembersFillMemberDefaults = {
    expr =
      let
        group = evalType types.group baseConfig;
        firstMember = builtins.head group.members;
      in
      {
        inherit (firstMember) priority weight;
      };
    expected = {
      weight = 100;
      priority = 1;
    };
  };

  testGroupPreservesFullSpec = {
    expr = evalType types.group {
      strategy = "primary-backup";
      table = 1500;
      mark = 1500;
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
    };
    expected = {
      name = "value";
      strategy = "primary-backup";
      table = 1500;
      mark = 1500;
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
    };
  };

  testGroupRejectsBadMember = {
    expr = evalTypeFails types.group {
      members = [ { wan = "1bad"; } ];
      mark = 1000;
      table = 1000;
    };
    expected = true;
  };

  testGroupRejectsBadStrategy = {
    expr = evalTypeFails types.group (baseConfig // { strategy = "magic"; });
    expected = true;
  };

  testGroupRejectsZeroTable = {
    expr = evalTypeFails types.group (baseConfig // { table = 0; });
    expected = true;
  };

  testGroupRejectsZeroMark = {
    expr = evalTypeFails types.group (baseConfig // { mark = 0; });
    expected = true;
  };

  testGroupRejectsMissingTable = {
    # `table` has no default, so leaving it out fails evaluation.
    expr = evalTypeFails types.group (removeAttrs baseConfig [ "table" ]);
    expected = true;
  };

  testGroupRejectsMissingMark = {
    expr = evalTypeFails types.group (removeAttrs baseConfig [ "mark" ]);
    expected = true;
  };
}
