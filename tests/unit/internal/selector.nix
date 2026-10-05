/*
  Unit tests for `lib/internal/selector.nix`, exposed as
  `wanwatch.selector`. The scenarios mirror
  `daemon/internal/selector/primarybackup_test.go`; compare the two
  by hand to catch cross-language drift.
*/
{
  pkgs,
  wanwatch,
  ...
}:
let
  inherit (pkgs) lib;
  inherit (wanwatch) group selector;

  # Members default to their 1-based list position as priority, so
  # list order is priority order. Mark and table are arbitrary.
  makeGroup =
    memberSpecs:
    group.make {
      name = "home";
      members = lib.imap1 (
        position: memberSpec:
        {
          priority = position;
          weight = 100;
        }
        // memberSpec
      ) memberSpecs;
      mark = 1000;
      table = 1000;
    };
in
{
  # ===== compute — empty members =====

  testEmptyMembersAllUnhealthy = {
    expr = (selector.compute (makeGroup [ { wan = "only"; } ]) { only = false; }).active;
    expected = null;
  };

  # ===== compute — single healthy =====

  testSingleHealthyMember = {
    expr = (selector.compute (makeGroup [ { wan = "primary"; } ]) { primary = true; }).active;
    expected = "primary";
  };

  # ===== compute — fail-over =====

  testFailoverToBackup = {
    expr =
      let
        homeGroup = makeGroup [
          { wan = "primary"; }
          { wan = "backup"; }
        ];
      in
      (selector.compute homeGroup {
        primary = false;
        backup = true;
      }).active;
    expected = "backup";
  };

  # ===== compute — primary preferred when both healthy =====

  testPrimaryWinsWhenBothHealthy = {
    expr =
      let
        homeGroup = makeGroup [
          { wan = "primary"; }
          { wan = "backup"; }
        ];
      in
      (selector.compute homeGroup {
        primary = true;
        backup = true;
      }).active;
    expected = "primary";
  };

  # ===== compute — all unhealthy =====

  testAllUnhealthyYieldsNull = {
    expr =
      let
        homeGroup = makeGroup [
          { wan = "a"; }
          { wan = "b"; }
        ];
      in
      (selector.compute homeGroup {
        a = false;
        b = false;
      }).active;
    expected = null;
  };

  # ===== compute — priority order respected regardless of list order =====

  testPriorityRespectedOutOfListOrder = {
    expr =
      let
        # `makeGroup` derives priority from list order, so set
        # explicit priorities here.
        homeGroup = group.make {
          name = "home";
          members = [
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
          ];
          mark = 1000;
          table = 1000;
        };
      in
      (selector.compute homeGroup {
        primary = true;
        middle = true;
        backup = true;
      }).active;
    expected = "primary";
  };

  # ===== compute — tie broken by wan name =====

  testEqualPrioritiesBrokenByWanName = {
    expr =
      let
        homeGroup = group.make {
          name = "home";
          members = [
            {
              wan = "zzz";
              priority = 1;
            }
            {
              wan = "aaa";
              priority = 1;
            }
            {
              wan = "mmm";
              priority = 1;
            }
          ];
          mark = 1000;
          table = 1000;
        };
      in
      (selector.compute homeGroup {
        aaa = true;
        mmm = true;
        zzz = true;
      }).active;
    expected = "aaa";
  };

  # ===== compute — missing health entry defaults to unhealthy =====

  testMissingHealthEntryUnhealthy = {
    # A WAN absent from `memberHealth` counts as unhealthy, matching
    # Go's zero value for a `map[string]bool` lookup.
    expr =
      let
        homeGroup = makeGroup [
          { wan = "primary"; }
          { wan = "backup"; }
        ];
      in
      (selector.compute homeGroup { backup = true; }).active;
    expected = "backup";
  };

  # ===== compute — weight is ignored =====

  testWeightIgnored = {
    # Despite `backup`'s far larger weight, primary-backup picks the
    # lower-priority member.
    expr =
      let
        homeGroup = group.make {
          name = "home";
          members = [
            {
              wan = "primary";
              priority = 1;
              weight = 1;
            }
            {
              wan = "backup";
              priority = 2;
              weight = 1000;
            }
          ];
          mark = 1000;
          table = 1000;
        };
      in
      (selector.compute homeGroup {
        primary = true;
        backup = true;
      }).active;
    expected = "primary";
  };

  # ===== compute — group name passed through =====

  testGroupNamePassedThrough = {
    expr =
      let
        homeGroup = group.make {
          name = "home-uplink";
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
      (selector.compute homeGroup { primary = true; }).group;
    expected = "home-uplink";
  };

  # ===== strategies registry =====

  testStrategiesRegistryHasPrimaryBackup = {
    expr = selector.strategies ? "primary-backup";
    expected = true;
  };

  testStrategiesRegistrySingleEntryInV1 = {
    expr = builtins.attrNames selector.strategies;
    expected = [ "primary-backup" ];
  };

  testStrategiesMatchGroupValidStrategies = {
    # Every strategy `group.make` accepts must have a selector
    # implementation, and vice versa; otherwise a valid group would
    # throw on its first `selector.compute` call.
    expr =
      let
        sortStrings = lib.sort lib.lessThan;
      in
      sortStrings (builtins.attrNames selector.strategies) == sortStrings group.validStrategies;
    expected = true;
  };

  # ===== compute — determinism =====

  testComputeDeterministic = {
    # Same inputs yield the same output across many calls.
    expr =
      let
        homeGroup = makeGroup [
          { wan = "a"; }
          { wan = "b"; }
          { wan = "c"; }
        ];
        memberHealth = {
          a = true;
          b = true;
          c = true;
        };
        results = builtins.genList (_: (selector.compute homeGroup memberHealth).active) 50;
      in
      builtins.all (active: active == builtins.head results) results;
    expected = true;
  };
}
