/*
  Unit tests for `lib/internal/config.nix` — the daemon-config
  JSON renderer.

  Exercises:
    - defaultGlobal exposed values
    - global merging (defaults + user overrides)
    - resolveAllocations: returns input groups unchanged on success;
      throws on duplicate mark or table across groups
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

  # Build a group with the given name + explicit mark/table. The
  # auto-allocator was removed in this release — every group must
  # declare both, validated against [1000, 32767] upstream.
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
        mark = 1000;
        table = 1000;
      }
      // extras
    );

  # Sample inputs reused across tests.
  primaryWan = wan.make {
    name = "primary";
    interface = "eth0";
    probe.targets.v4 = [ "1.1.1.1" ];
  };

  backupWan = wan.make {
    name = "backup";
    interface = "wwan0";
    probe.targets.v4 = [ "8.8.8.8" ];
  };

  homeGroup = group.make {
    name = "home";
    members = [
      {
        wan = "primary";
        priority = 1;
      }
    ];
    mark = 1000;
    table = 1000;
  };

  workGroup = group.make {
    name = "work";
    members = [
      {
        wan = "backup";
        priority = 1;
      }
    ];
    mark = 1001;
    table = 1001;
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

  # ===== resolveAllocations — pass-through =====

  testResolveAllocationsEmptyInput = {
    # No groups → nothing to validate → trivial pass.
    expr = config.resolveAllocations { };
    expected = { };
  };

  testResolveAllocationsReturnsGroupsUnchanged = {
    # Distinct marks and tables → no duplicates → input echoed back
    # untouched. Confirms the post-allocator-removal behaviour:
    # resolveAllocations is now a validator, not a transformer.
    expr =
      let
        input = {
          home = homeGroup;
          work = workGroup;
        };
      in
      config.resolveAllocations input == input;
    expected = true;
  };

  testResolveAllocationsPreservesExplicitValues = {
    expr =
      let
        resolved = config.resolveAllocations {
          home = homeGroup;
          work = workGroup;
        };
      in
      {
        homeMark = resolved.home.mark;
        homeTable = resolved.home.table;
        workMark = resolved.work.mark;
        workTable = resolved.work.table;
      };
    expected = {
      homeMark = 1000;
      homeTable = 1000;
      workMark = 1001;
      workTable = 1001;
    };
  };

  testResolveAllocationsAllowsMarkEqualToTable = {
    # mark and table are independent integer spaces — a group with
    # `mark = 1000; table = 1000;` is fine. The duplicate check is
    # within-field, not across-field.
    expr =
      let
        g = mkGroup "g" {
          mark = 1500;
          table = 1500;
        };
      in
      (config.resolveAllocations { inherit g; }).g.mark == 1500;
    expected = true;
  };

  # ===== resolveAllocations — duplicate detection =====

  testResolveAllocationsThrowsOnDuplicateMark = {
    expr =
      evalThrows
        (config.resolveAllocations {
          a = mkGroup "a" {
            mark = 1500;
            table = 1500;
          };
          b = mkGroup "b" {
            mark = 1500; # collides with a
            table = 1600;
          };
        }).a.mark;
    expected = true;
  };

  testResolveAllocationsThrowsOnDuplicateTable = {
    expr =
      evalThrows
        (config.resolveAllocations {
          a = mkGroup "a" {
            mark = 1500;
            table = 1500;
          };
          b = mkGroup "b" {
            mark = 1600;
            table = 1500; # collides with a
          };
        }).a.table;
    expected = true;
  };

  testResolveAllocationsThreeWayDuplicateMark = {
    # Three groups all sharing the same mark — still throws.
    expr =
      evalThrows
        (config.resolveAllocations {
          a = mkGroup "a" {
            mark = 1500;
            table = 1500;
          };
          b = mkGroup "b" {
            mark = 1500;
            table = 1600;
          };
          c = mkGroup "c" {
            mark = 1500;
            table = 1700;
          };
        }).a.mark;
    expected = true;
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

  testRenderGroupsCarryUserMarkAndTable = {
    # Confirms the user's mark/table flow through render unchanged.
    expr =
      let
        rendered = config.render {
          groups = {
            home = homeGroup;
          };
        };
      in
      {
        inherit (rendered.groups.home) mark table;
      };
    expected = {
      mark = 1000;
      table = 1000;
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
