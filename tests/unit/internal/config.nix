/*
  Unit tests for `lib/internal/config.nix`, the daemon-config JSON
  renderer: global defaults and overrides, cross-group duplicate
  mark/table detection in `assertUniqueMarksAndTables`, the rendered shape,
  and the `toJSON` string form.
*/
{
  helpers,
  pkgs,
  wanwatch,
  ...
}:
let
  inherit (pkgs) lib;
  inherit (helpers) evalThrows;
  inherit (wanwatch)
    config
    group
    wan
    ;

  # A one-member group; `overrides` sets its mark and table.
  makeGroup =
    name: overrides:
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
      // overrides
    );

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

  # ===== assertUniqueMarksAndTables — pass-through =====

  testUniqueMarksAndTablesEmptyInput = {
    expr = config.assertUniqueMarksAndTables { };
    expected = { };
  };

  testUniqueMarksAndTablesReturnsGroupsUnchanged = {
    # The check validates without transforming: distinct
    # marks and tables echo the input back untouched.
    expr =
      let
        input = {
          home = homeGroup;
          work = workGroup;
        };
      in
      config.assertUniqueMarksAndTables input == input;
    expected = true;
  };

  testUniqueMarksAndTablesPreservesExplicitValues = {
    expr =
      let
        resolved = config.assertUniqueMarksAndTables {
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

  testUniqueMarksAndTablesAllowsMarkEqualToTable = {
    # Marks and tables are independent number spaces, so duplicates
    # are checked within each field, not across them.
    expr =
      let
        sameNumbers = makeGroup "sameNumbers" {
          mark = 1500;
          table = 1500;
        };
      in
      (config.assertUniqueMarksAndTables { inherit sameNumbers; }).sameNumbers.mark == 1500;
    expected = true;
  };

  # ===== assertUniqueMarksAndTables — duplicate detection =====

  testUniqueMarksAndTablesThrowsOnDuplicateMark = {
    expr =
      evalThrows
        (config.assertUniqueMarksAndTables {
          a = makeGroup "a" {
            mark = 1500;
            table = 1500;
          };
          b = makeGroup "b" {
            mark = 1500; # collides with a
            table = 1600;
          };
        }).a.mark;
    expected = true;
  };

  testUniqueMarksAndTablesThrowsOnDuplicateTable = {
    expr =
      evalThrows
        (config.assertUniqueMarksAndTables {
          a = makeGroup "a" {
            mark = 1500;
            table = 1500;
          };
          b = makeGroup "b" {
            mark = 1600;
            table = 1500; # collides with a
          };
        }).a.table;
    expected = true;
  };

  testUniqueMarksAndTablesThreeWayDuplicateMark = {
    expr =
      evalThrows
        (config.assertUniqueMarksAndTables {
          a = makeGroup "a" {
            mark = 1500;
            table = 1500;
          };
          b = makeGroup "b" {
            mark = 1500;
            table = 1600;
          };
          c = makeGroup "c" {
            mark = 1500;
            table = 1700;
          };
        }).a.mark;
    expected = true;
  };

  # ===== resolveAllocations — deprecated alias =====

  testResolveAllocationsAliasReturnsGroupsUnchanged = {
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

  testResolveAllocationsAliasThrowsOnDuplicateMark = {
    expr =
      evalThrows
        (config.resolveAllocations {
          a = makeGroup "a" {
            mark = 1500;
            table = 1500;
          };
          b = makeGroup "b" {
            mark = 1500;
            table = 1600;
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
    expr = lib.hasInfix "\"schema\":1" (config.toJSON { });
    expected = true;
  };

  testToJSONIncludesGlobal = {
    expr = lib.hasInfix "\"global\":{" (config.toJSON { });
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
