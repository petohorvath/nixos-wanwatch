/*
  Tests for `lib/internal/selector.nix`, exposed as
  `wanwatch.selector`. The scenarios mirror
  `daemon/internal/selector/primarybackup_test.go`; compare the two
  by hand to catch cross-language drift.
*/
{ lib, wanwatch, ... }:
let
  inherit (wanwatch) group selector;

  makeGroup =
    members:
    group.make {
      name = "home";
      inherit members;
      mark = 1000;
      table = 1000;
    };

  selectActive = members: memberHealth: (selector.compute (makeGroup members) memberHealth).active;

  primaryAndBackup = {
    primary.priority = 1;
    backup.priority = 2;
  };
in
{
  compute = {
    testSingleHealthyMember = {
      expr = selectActive { primary = { }; } { primary = true; };
      expected = "primary";
    };

    testSingleUnhealthyMember = {
      expr = selectActive { primary = { }; } { primary = false; };
      expected = null;
    };

    testPrimaryWinsWhenBothHealthy = {
      expr = selectActive primaryAndBackup {
        primary = true;
        backup = true;
      };
      expected = "primary";
    };

    testFailsOverToBackup = {
      expr = selectActive primaryAndBackup {
        primary = false;
        backup = true;
      };
      expected = "backup";
    };

    testAllUnhealthyYieldsNull = {
      expr = selectActive primaryAndBackup {
        primary = false;
        backup = false;
      };
      expected = null;
    };

    # A WAN absent from `memberHealth` counts as unhealthy, matching
    # Go's zero value for a `map[string]bool` lookup.
    testMissingHealthCountsAsUnhealthy = {
      expr = selectActive primaryAndBackup { backup = true; };
      expected = "backup";
    };

    # `primary` sorts last by name but has the lowest priority.
    testPriorityOutranksWanName = {
      expr =
        selectActive
          {
            backup.priority = 5;
            primary.priority = 1;
            middle.priority = 3;
          }
          {
            primary = true;
            middle = true;
            backup = true;
          };
      expected = "primary";
    };

    testEqualPrioritiesBrokenByWanName = {
      expr =
        selectActive
          (lib.genAttrs
            [
              "zzz"
              "aaa"
              "mmm"
            ]
            (_: {
              priority = 1;
            })
          )
          {
            aaa = true;
            mmm = true;
            zzz = true;
          };
      expected = "aaa";
    };

    testReportsGroupName = {
      expr = (selector.compute (makeGroup primaryAndBackup) { primary = true; }).group;
      expected = "home";
    };

    testComputeDeterministic = {
      expr =
        let
          memberHealth = {
            a = true;
            b = true;
            c = true;
          };
          members = builtins.mapAttrs (_: _: { }) memberHealth;
        in
        lib.unique (builtins.genList (_: selectActive members memberHealth) 50);
      expected = [ "a" ];
    };
  };

  strategies = {
    testRegistry = {
      expr = builtins.attrNames selector.strategies;
      expected = [ "primary-backup" ];
    };

    # Every Strategy `group.make` accepts needs a selector
    # implementation, and vice versa; otherwise a valid Group would
    # throw on its first `selector.compute` call.
    testStrategiesMatchGroupValidStrategies = {
      expr = builtins.attrNames selector.strategies;
      expected = lib.sort lib.lessThan group.validStrategies;
    };
  };
}
