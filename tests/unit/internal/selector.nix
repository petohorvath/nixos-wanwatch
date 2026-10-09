/*
  Tests for `lib/internal/selector.nix`, exposed as
  `wanwatch.selector`. The scenarios mirror
  `daemon/internal/selector/primarybackup_test.go`; compare the two
  by hand to catch cross-language drift.
*/
{ lib, wanwatch, ... }:
let
  inherit (wanwatch) group selector;

  # Members take their 1-based list position as priority unless they
  # set one, so list order is priority order by default.
  makeGroup =
    members:
    group.make {
      name = "home";
      members = lib.imap1 (position: member: { priority = position; } // member) members;
      mark = 1000;
      table = 1000;
    };

  selectActive = members: memberHealth: (selector.compute (makeGroup members) memberHealth).active;

  primaryAndBackup = [
    { wan = "primary"; }
    { wan = "backup"; }
  ];
in
{
  compute = {
    testSingleHealthyMember = {
      expr = selectActive [ { wan = "primary"; } ] { primary = true; };
      expected = "primary";
    };

    testSingleUnhealthyMember = {
      expr = selectActive [ { wan = "primary"; } ] { primary = false; };
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

    testPriorityOutranksListOrder = {
      expr =
        selectActive
          [
            {
              wan = "backup";
              priority = 5;
            }
            {
              wan = "primary";
              priority = 1;
            }
            {
              wan = "middle";
              priority = 3;
            }
          ]
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
          (map
            (wan: {
              inherit wan;
              priority = 1;
            })
            [
              "zzz"
              "aaa"
              "mmm"
            ]
          )
          {
            aaa = true;
            mmm = true;
            zzz = true;
          };
      expected = "aaa";
    };

    # Despite `backup`'s far larger weight, primary-backup picks the
    # Member with the lowest priority.
    testIgnoresWeight = {
      expr =
        selectActive
          [
            {
              wan = "primary";
              weight = 1;
            }
            {
              wan = "backup";
              weight = 1000;
            }
          ]
          {
            primary = true;
            backup = true;
          };
      expected = "primary";
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
          members = map (wan: { inherit wan; }) (builtins.attrNames memberHealth);
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
