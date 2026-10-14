/*
  Tests for `lib/internal/config.nix`, the daemon-config renderer:
  global-settings validation, cross-Group mark and table checks, the
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
    global
    group
    wan
    ;

  # Groups keyed by name: the minimal Group, one Member on `primary`,
  # with the given fields, such as mark and table, overridden.
  makeGroups = lib.mapAttrs (
    name: overrides: group.make (fixtures.inputs.group.minimal // overrides // { inherit name; })
  );

  distinctGroups = makeGroups {
    home = {
      mark = 1000;
      table = 1000;
    };
    work = {
      mark = 1001;
      table = 1001;
    };
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

  assertUnique = config.assertUniqueMarksAndTables;
in
{
  testSchemaVersion = {
    expr = config.schemaVersion;
    expected = 1;
  };

  testExports = {
    expr = builtins.attrNames config;
    expected = [
      "assertUniqueMarksAndTables"
      "paths"
      "render"
      "schemaVersion"
      "toJSON"
    ];
  };

  # The module's sandbox lets the daemon write only under /run/wanwatch
  # (ADR-0014).
  testPaths = {
    expr = config.paths;
    expected = {
      statePath = "/run/wanwatch/state.json";
      hooksDir = "/etc/wanwatch/hooks";
      metricsSocket = "/run/wanwatch/metrics.sock";
    };
  };

  assertUniqueMarksAndTables = {
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
        a = {
          mark = 1500;
          table = 1500;
        };
        b = {
          mark = 1500;
          table = 1600;
        };
      });
      expectedError = {
        type = "ThrownError";
        msg = "mark 1500 is shared by groups \\['a', 'b']";
      };
    };

    testRejectsSharedTable = {
      expr = assertUnique (makeGroups {
        a = {
          mark = 1500;
          table = 1500;
        };
        b = {
          mark = 1600;
          table = 1500;
        };
      });
      expectedError = {
        type = "ThrownError";
        msg = "table 1500 is shared by groups \\['a', 'b']";
      };
    };

    testNamesEveryGroupSharingAMark = {
      expr = assertUnique (makeGroups {
        a = {
          mark = 1500;
          table = 1500;
        };
        b = {
          mark = 1500;
          table = 1600;
        };
        c = {
          mark = 1500;
          table = 1700;
        };
      });
      expectedError = {
        type = "ThrownError";
        msg = "mark 1500 is shared by groups \\['a', 'b', 'c']";
      };
    };
  };

  render = {
    testEmptyInput = {
      expr = config.render { };
      expected = {
        schema = config.schemaVersion;
        global = global.defaults // config.paths;
        wans = { };
        groups = { };
      };
    };

    testFillsGlobalDefaults = {
      expr = (config.render { global.logLevel = "debug"; }).global;
      expected = global.defaults // config.paths // { logLevel = "debug"; };
    };

    # A global value is also valid input.
    testSerializesGlobalValue = {
      expr = (config.render { global = global.make fixtures.inputs.global.full; }).global;
      expected = fixtures.inputs.global.full // config.paths;
    };

    testRejectsInvalidGlobal = {
      expr = config.render { global.logLevel = "verbose"; };
      expectedError = {
        type = "ThrownError";
        msg = "global.make: \\[globalInvalidLogLevel]";
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
        inherit wans;
        groups = makeGroups {
          a = {
            mark = 1500;
            table = 1500;
          };
          b = {
            mark = 1500;
            table = 1600;
          };
        };
      };
      expectedError = {
        type = "ThrownError";
        msg = "mark 1500 is shared";
      };
    };

    # The daemon would reject the file and fail its unit.
    testRejectsUndeclaredMemberWan = {
      expr = config.render {
        inherit wans;
        groups.home-uplink = group.make (
          fixtures.inputs.group.minimal
          // {
            members = {
              primary = { };
              backup = { };
            };
          }
        );
      };
      expectedError = {
        type = "ThrownError";
        msg = "group 'home-uplink' references wan 'backup'";
      };
    };

    testNamesEveryUndeclaredMemberWan = {
      expr = config.render {
        groups = makeGroups {
          a = {
            mark = 1500;
            table = 1500;
            members = {
              primary = { };
              backup = { };
            };
          };
          b = {
            mark = 1600;
            table = 1600;
          };
        };
      };
      expectedError = {
        type = "ThrownError";
        msg = "group 'a' references wan 'backup'; group 'a' references wan 'primary'; group 'b' references wan 'primary'";
      };
    };
  };

  toJSON = {
    testParsesBackToRender = {
      expr = builtins.fromJSON (config.toJSON renderInput);
      expected = config.render renderInput;
    };
  };
}
