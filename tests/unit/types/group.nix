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

  # Minimal valid group input. `name` is `readOnly` and defaults
  # to the attribute key — `evalType` wraps under
  # `options.value`, so the submodule sees `name = "value"`.
  baseConfig = {
    members = [
      {
        wan = "primary";
        priority = 1;
      }
    ];
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

  testGroupTableAcceptsNull = {
    expr = evalType types.groupTable null;
    expected = null;
  };

  testGroupTableAcceptsPositive = {
    expr = evalType types.groupTable 100;
    expected = 100;
  };

  testGroupTableRejectsZero = {
    expr = evalTypeFails types.groupTable 0;
    expected = true;
  };

  testGroupMarkAcceptsNull = {
    expr = evalType types.groupMark null;
    expected = null;
  };

  testGroupMarkAcceptsPositive = {
    expr = evalType types.groupMark 100;
    expected = 100;
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
      table = null;
      mark = null;
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
      table = 100;
      mark = 100;
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
      table = 100;
      mark = 100;
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
}
