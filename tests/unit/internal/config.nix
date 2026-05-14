/*
  Unit tests for `lib/internal/config.nix` — the daemon-config
  JSON renderer.

  Exercises:
    - defaultGlobal exposed values
    - global merging (defaults + user overrides)
    - resolveAllocations: auto-fills null mark/table; preserves
      user-set values
    - render shape: schema, global, wans, groups
    - toJSON returns a string with the expected structure
*/
{ pkgs, libnet, ... }:
let
  wanwatch = import ../../../lib {
    inherit (pkgs) lib;
    inherit libnet;
  };
  inherit (wanwatch)
    config
    wan
    group
    ;

  helpers = import ../helpers.nix { inherit pkgs; };
  inherit (helpers) evalThrows;

  # Build a group with optional mark/table overrides. Single
  # auto-allocated member so callers can focus on the field under
  # test.
  mkGroup =
    name: extras:
    group.make (
      {
        inherit name;
        members = [
          {
            wan = "primary";
            priority = 1;
          }
        ];
      }
      // extras
    );

  # Sample inputs reused across tests.
  primaryWan = wan.make {
    name = "primary";
    interface = "eth0";
    probe.targets = [ "1.1.1.1" ];
  };

  backupWan = wan.make {
    name = "backup";
    interface = "wwan0";
    probe.targets = [ "8.8.8.8" ];
  };

  homeGroup = group.make {
    name = "home";
    members = [
      {
        wan = "primary";
        priority = 1;
      }
    ];
  };

  workGroup = group.make {
    name = "work";
    members = [
      {
        wan = "backup";
        priority = 1;
      }
    ];
  };

  # Explicit user-set mark/table for testing override preservation.
  pinnedGroup = group.make {
    name = "pinned";
    members = [
      {
        wan = "primary";
        priority = 1;
      }
    ];
    mark = 999;
    table = 1234;
  };
in
{
  # ===== defaultGlobal =====

  testDefaultGlobalShape = {
    expr = config.defaultGlobal;
    expected = {
      statePath = "/run/wanwatch/state.json";
      hooksDir = "/etc/wanwatch/hooks";
      metricsSocket = "/run/wanwatch/metrics.sock";
      logLevel = "info";
      hookTimeoutMs = 5000;
    };
  };

  # ===== schemaVersion =====

  testSchemaVersionIsInt = {
    expr = builtins.isInt config.schemaVersion;
    expected = true;
  };

  testSchemaVersionStartsAtOne = {
    expr = config.schemaVersion;
    expected = 1;
  };

  # ===== resolveAllocations — auto-fill =====

  testResolveAllocatesAllNullMembers = {
    # Both groups have null mark + null table; both should be filled.
    expr =
      let
        resolved = config.resolveAllocations {
          home = homeGroup;
          work = workGroup;
        };
      in
      {
        homeMarkIsInt = builtins.isInt resolved.home.mark;
        workMarkIsInt = builtins.isInt resolved.work.mark;
        homeTableIsInt = builtins.isInt resolved.home.table;
        workTableIsInt = builtins.isInt resolved.work.table;
      };
    expected = {
      homeMarkIsInt = true;
      workMarkIsInt = true;
      homeTableIsInt = true;
      workTableIsInt = true;
    };
  };

  testResolveMarksDistinctForDifferentNames = {
    expr =
      let
        resolved = config.resolveAllocations {
          home = homeGroup;
          work = workGroup;
        };
      in
      resolved.home.mark != resolved.work.mark;
    expected = true;
  };

  testResolveTablesDistinctForDifferentNames = {
    expr =
      let
        resolved = config.resolveAllocations {
          home = homeGroup;
          work = workGroup;
        };
      in
      resolved.home.table != resolved.work.table;
    expected = true;
  };

  # ===== resolveAllocations — preserve explicit values =====

  testResolvePreservesExplicitMark = {
    expr = (config.resolveAllocations { pinned = pinnedGroup; }).pinned.mark;
    expected = 999;
  };

  testResolvePreservesExplicitTable = {
    expr = (config.resolveAllocations { pinned = pinnedGroup; }).pinned.table;
    expected = 1234;
  };

  # ===== resolveAllocations — collision detection =====

  testResolveThrowsOnMarkCollision = {
    # Compute the auto-allocation for a single-group set, then pin
    # that same mark on a second group — auto for the first must
    # collide with explicit on the second.
    expr =
      let
        autoOnly = config.resolveAllocations { home = mkGroup "home" { }; };
        pinnedMark = autoOnly.home.mark;
        result = config.resolveAllocations {
          home = mkGroup "home" { };
          collides = mkGroup "collides" { mark = pinnedMark; };
        };
      in
      evalThrows result.home.mark;
    expected = true;
  };

  testResolveThrowsOnTableCollision = {
    expr =
      let
        autoOnly = config.resolveAllocations { home = mkGroup "home" { }; };
        pinnedTable = autoOnly.home.table;
        result = config.resolveAllocations {
          home = mkGroup "home" { };
          collides = mkGroup "collides" { table = pinnedTable; };
        };
      in
      evalThrows result.home.table;
    expected = true;
  };

  testResolveAcceptsNonCollidingExplicits = {
    # Picking explicit values far outside the auto-allocator's
    # typical hash range — vanishingly unlikely to collide.
    expr =
      let
        resolved = config.resolveAllocations {
          home = mkGroup "home" { };
          pinned = mkGroup "pinned" {
            mark = 65000;
            table = 32000;
          };
        };
      in
      resolved.pinned.mark == 65000 && resolved.pinned.table == 32000;
    expected = true;
  };

  testResolveMixedExplicitAndAuto = {
    # `home` is auto; `pinned` is explicit. The auto allocation must
    # not collide with the pinned value and must produce a different
    # mark/table for home.
    expr =
      let
        resolved = config.resolveAllocations {
          home = homeGroup;
          pinned = pinnedGroup;
        };
      in
      {
        pinnedMark = resolved.pinned.mark;
        pinnedTable = resolved.pinned.table;
        homeMarkIsAutoAllocated = builtins.isInt resolved.home.mark && resolved.home.mark != 999;
        homeTableIsAutoAllocated = builtins.isInt resolved.home.table && resolved.home.table != 1234;
      };
    expected = {
      pinnedMark = 999;
      pinnedTable = 1234;
      homeMarkIsAutoAllocated = true;
      homeTableIsAutoAllocated = true;
    };
  };

  # ===== render — shape =====

  testRenderHasSchema = {
    expr = (config.render { }).schema;
    expected = 1;
  };

  testRenderEmptyGlobalUsesDefaults = {
    expr = (config.render { }).global;
    expected = config.defaultGlobal;
  };

  testRenderGlobalOverridesDefaults = {
    expr =
      (config.render {
        global = {
          logLevel = "debug";
          statePath = "/var/run/wanwatch/state.json";
          hookTimeoutMs = 9000;
        };
      }).global;
    expected = {
      statePath = "/var/run/wanwatch/state.json";
      hooksDir = "/etc/wanwatch/hooks";
      metricsSocket = "/run/wanwatch/metrics.sock";
      logLevel = "debug";
      hookTimeoutMs = 9000;
    };
  };

  testRenderEmbedsWans = {
    expr =
      let
        rendered = config.render {
          wans = {
            primary = primaryWan;
            backup = backupWan;
          };
        };
      in
      builtins.attrNames rendered.wans;
    expected = [
      "backup"
      "primary"
    ];
  };

  testRenderWansAreSerializedObjects = {
    # Each rendered wan must surface as an attrset (toJSONValue form),
    # not the raw tagged value.
    expr =
      let
        rendered = config.render {
          wans = {
            primary = primaryWan;
          };
        };
      in
      rendered.wans.primary.interface;
    expected = "eth0";
  };

  testRenderGroupsHaveResolvedMarkAndTable = {
    expr =
      let
        rendered = config.render {
          groups = {
            home = homeGroup;
          };
        };
      in
      {
        markIsInt = builtins.isInt rendered.groups.home.mark;
        tableIsInt = builtins.isInt rendered.groups.home.table;
      };
    expected = {
      markIsInt = true;
      tableIsInt = true;
    };
  };

  testRenderEmptyInputs = {
    expr = config.render { };
    expected = {
      schema = 1;
      global = config.defaultGlobal;
      wans = { };
      groups = { };
    };
  };

  # ===== toJSON — string output =====

  testToJSONReturnsString = {
    expr = builtins.isString (config.toJSON { });
    expected = true;
  };

  testToJSONIncludesSchema = {
    expr = pkgs.lib.hasInfix "\"schema\":1" (config.toJSON { });
    expected = true;
  };

  testToJSONIncludesGlobal = {
    expr = pkgs.lib.hasInfix "\"global\":{" (config.toJSON { });
    expected = true;
  };

  testToJSONRoundTrip = {
    # The rendered string must parse back to the same shape.
    expr =
      let
        input = {
          global = {
            logLevel = "warn";
          };
          wans = {
            primary = primaryWan;
          };
          groups = {
            home = homeGroup;
          };
        };
        rendered = config.render input;
        roundTripped = builtins.fromJSON (config.toJSON input);
      in
      roundTripped == rendered;
    expected = true;
  };

  # ===== toJSONValue alias =====

  testToJSONValueIsRender = {
    expr = config.toJSONValue { } == config.render { };
    expected = true;
  };
}
