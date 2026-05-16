/*
  Unit tests for `lib/types/group.nix`. Cross-field invariants
  (non-empty members, no duplicate WAN references) are tested in
  `tests/unit/internal/group.nix` against `group.make` /
  `group.tryMake`. Here we exercise only what the type system
  itself enforces.
*/
{ pkgs, libnet, ... }:
let
  wanwatch = import ../../../lib {
    inherit (pkgs) lib;
    inherit libnet;
  };
  inherit (wanwatch) types;

  helpers = import ../helpers.nix { inherit pkgs; };
  inherit (helpers) evalType evalTypeFails;

  # Minimal well-formed group input. `mark` and `table` are required
  # since the auto-allocator was removed; both typed as
  # `wanwatch.types.{fwmark,routingTableId}` (range [1000, 32767]).
  # `name` is `readOnly` and defaults to the attribute key — `evalType`
  # wraps under `options.value`, so the submodule sees `name = "value"`.
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
    # null no longer valid — was the "auto-allocate" sentinel, now
    # removed; every group must declare an integer.
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
        g = evalType types.group baseConfig;
      in
      {
        inherit (g)
          name
          strategy
          table
          mark
          ;
        memberCount = builtins.length g.members;
        firstMemberWan = (builtins.head g.members).wan;
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
    # Each member entry should get member's defaults filled in.
    expr =
      let
        g = evalType types.group baseConfig;
        m = builtins.head g.members;
      in
      {
        inherit (m) weight priority;
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
    # baseConfig minus `table` → the option becomes required-but-missing,
    # which the submodule rejects at eval time.
    expr = evalTypeFails types.group (builtins.removeAttrs baseConfig [ "table" ]);
    expected = true;
  };

  testGroupRejectsMissingMark = {
    expr = evalTypeFails types.group (builtins.removeAttrs baseConfig [ "mark" ]);
    expected = true;
  };
}
