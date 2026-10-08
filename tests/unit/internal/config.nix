/*
  Tests for `lib/internal/config.nix`, the daemon-config renderer:
  global defaults and overrides, cross-Group mark and table checks, the
  rendered shape, and the JSON string form.
*/
{
  fixtures,
  lib,
  wanwatch,
  ...
}:
let
  inherit (wanwatch)
    config
    group
    wan
    ;

  # One-member Groups keyed by name, each with its `[ mark table ]`.
  makeGroups = lib.mapAttrs (
    name: markAndTable:
    group.make (
      fixtures.inputs.group.minimal
      // {
        inherit name;
        mark = builtins.elemAt markAndTable 0;
        table = builtins.elemAt markAndTable 1;
      }
    )
  );

  distinctGroups = makeGroups {
    home = [
      1000
      1000
    ];
    work = [
      1001
      1001
    ];
  };

  wans = {
    primary = wan.make fixtures.inputs.wan.minimal;
    vpn = wan.make fixtures.inputs.wan.full;
  };

  renderInput = {
    global.logLevel = "warn";
    inherit wans;
    groups = distinctGroups;
  };

  # The deprecated alias must behave like the new name.
  uniquenessChecks = {
    inherit (config) assertUniqueMarksAndTables resolveAllocations;
  };

  makeUniquenessTests = _: assertUnique: {
    testEmptyInput = {
      expr = assertUnique { };
      expected = { };
    };

    # Marks and tables are separate number spaces, so each Group may
    # use one number for both.
    testReturnsGroupsUnchanged = {
      expr = assertUnique distinctGroups;
      expected = distinctGroups;
    };

    testRejectsSharedMark = {
      expr = assertUnique (makeGroups {
        a = [
          1500
          1500
        ];
        b = [
          1500
          1600
        ];
      });
      expectedError = {
        type = "ThrownError";
        msg = "mark 1500 is shared by groups \\['a', 'b']";
      };
    };

    testRejectsSharedTable = {
      expr = assertUnique (makeGroups {
        a = [
          1500
          1500
        ];
        b = [
          1600
          1500
        ];
      });
      expectedError = {
        type = "ThrownError";
        msg = "table 1500 is shared by groups \\['a', 'b']";
      };
    };

    testNamesEveryGroupSharingAMark = {
      expr = assertUnique (makeGroups {
        a = [
          1500
          1500
        ];
        b = [
          1500
          1600
        ];
        c = [
          1500
          1700
        ];
      });
      expectedError = {
        type = "ThrownError";
        msg = "mark 1500 is shared by groups \\['a', 'b', 'c']";
      };
    };
  };
in
{
  testDefaultGlobal = {
    expr = config.defaultGlobal;
    expected = {
      statePath = "/run/wanwatch/state.json";
      hooksDir = "/etc/wanwatch/hooks";
      metricsSocket = "/run/wanwatch/metrics.sock";
      logLevel = "info";
      hookTimeoutMs = 5000;
    };
  };

  testSchemaVersion = {
    expr = config.schemaVersion;
    expected = 1;
  };

  uniqueness = lib.mapAttrs makeUniquenessTests uniquenessChecks;

  render = {
    testEmptyInput = {
      expr = config.render { };
      expected = {
        schema = config.schemaVersion;
        global = config.defaultGlobal;
        wans = { };
        groups = { };
      };
    };

    testMergesGlobalOverDefaults = {
      expr =
        (config.render {
          global = {
            logLevel = "debug";
            hookTimeoutMs = 9000;
          };
        }).global;
      expected = config.defaultGlobal // {
        logLevel = "debug";
        hookTimeoutMs = 9000;
      };
    };

    testSerializesWansAndGroups = {
      expr = removeAttrs (config.render renderInput) [ "global" ];
      expected = {
        schema = config.schemaVersion;
        wans = builtins.mapAttrs (_: wan.toJSONValue) wans;
        groups = builtins.mapAttrs (_: group.toJSONValue) distinctGroups;
      };
    };

    testRejectsSharedMark = {
      expr = config.render {
        groups = makeGroups {
          a = [
            1500
            1500
          ];
          b = [
            1500
            1600
          ];
        };
      };
      expectedError = {
        type = "ThrownError";
        msg = "mark 1500 is shared";
      };
    };
  };

  toJSON = {
    testParsesBackToRender = {
      expr = builtins.fromJSON (config.toJSON renderInput);
      expected = config.render renderInput;
    };
  };

  toJSONValue = {
    testIsRender = {
      expr = config.toJSONValue renderInput;
      expected = config.render renderInput;
    };
  };
}
