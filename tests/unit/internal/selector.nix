/*
  Unit tests for `lib/internal/selector.nix` (exposed as
  `wanwatch.selector`). Mirrors the scenarios in
  `daemon/internal/selector/primarybackup_test.go` —
  cross-language drift is caught by manual diff between this file
  and the Go test cases.
*/
{ pkgs, libnet, ... }:
let
  wanwatch = import ../../../lib {
    inherit (pkgs) lib;
    inherit libnet;
  };
  inherit (wanwatch) selector group;

  helpers = import ../helpers.nix { inherit pkgs; };
  inherit (helpers) evalThrows;

  # Build a one-line group input quickly. Members default to
  # priority = (index + 1) so the order of the list is also the
  # default priority order.
  mkGroup =
    memberSpecs:
    group.make {
      name = "home";
      members = pkgs.lib.imap1 (
        i: m:
        {
          priority = i;
          weight = 100;
        }
        // m
      ) memberSpecs;
    };
in
{
  # ===== compute — empty members =====

  testEmptyMembersAllUnhealthy = {
    expr = (selector.compute (mkGroup [ { wan = "only"; } ]) { only = false; }).active;
    expected = null;
  };

  # ===== compute — single healthy =====

  testSingleHealthyMember = {
    expr = (selector.compute (mkGroup [ { wan = "primary"; } ]) { primary = true; }).active;
    expected = "primary";
  };

  # ===== compute — fail-over =====

  testFailoverToBackup = {
    expr =
      let
        g = mkGroup [
          { wan = "primary"; }
          { wan = "backup"; }
        ];
      in
      (selector.compute g {
        primary = false;
        backup = true;
      }).active;
    expected = "backup";
  };

  # ===== compute — primary preferred when both healthy =====

  testPrimaryWinsWhenBothHealthy = {
    expr =
      let
        g = mkGroup [
          { wan = "primary"; }
          { wan = "backup"; }
        ];
      in
      (selector.compute g {
        primary = true;
        backup = true;
      }).active;
    expected = "primary";
  };

  # ===== compute — all unhealthy =====

  testAllUnhealthyYieldsNull = {
    expr =
      let
        g = mkGroup [
          { wan = "a"; }
          { wan = "b"; }
        ];
      in
      (selector.compute g {
        a = false;
        b = false;
      }).active;
    expected = null;
  };

  # ===== compute — priority order respected regardless of list order =====

  testPriorityRespectedOutOfListOrder = {
    expr =
      let
        # Out-of-priority-order list — `mkGroup` would assign
        # `priority = i+1`, so use explicit priorities.
        g = group.make {
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
        };
      in
      (selector.compute g {
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
        g = group.make {
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
        };
      in
      (selector.compute g {
        aaa = true;
        mmm = true;
        zzz = true;
      }).active;
    expected = "aaa";
  };

  # ===== compute — missing health entry defaults to unhealthy =====

  testMissingHealthEntryUnhealthy = {
    # `memberHealth` doesn't include `primary` — strategy treats
    # it as `false`. Matches the Go default-zero behavior on a
    # `map[string]bool` lookup.
    expr =
      let
        g = mkGroup [
          { wan = "primary"; }
          { wan = "backup"; }
        ];
      in
      (selector.compute g { backup = true; }).active;
    expected = "backup";
  };

  # ===== compute — weight is ignored =====

  testWeightIgnored = {
    # Even though `backup` has 10× the weight of `primary`,
    # primary-backup picks the lower-priority member.
    expr =
      let
        g = group.make {
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
        };
      in
      (selector.compute g {
        primary = true;
        backup = true;
      }).active;
    expected = "primary";
  };

  # ===== compute — group name passed through =====

  testGroupNamePassedThrough = {
    expr =
      let
        g = group.make {
          name = "home-uplink";
          members = [
            {
              wan = "primary";
              priority = 1;
            }
          ];
        };
      in
      (selector.compute g { primary = true; }).group;
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
    # Drift catcher: every strategy `group.make` accepts must be
    # implemented by `selector.compute`, and vice versa. Adding a
    # strategy to one side and forgetting the other would let groups
    # be constructed and then throw at first selector call —
    # surface that mismatch at eval time instead.
    expr =
      let
        sortStr = pkgs.lib.sort (a: b: a < b);
      in
      sortStr (builtins.attrNames selector.strategies) == sortStr group.validStrategies;
    expected = true;
  };

  # ===== compute — determinism =====

  testComputeDeterministic = {
    # Same inputs → same outputs across many calls.
    expr =
      let
        g = mkGroup [
          { wan = "a"; }
          { wan = "b"; }
          { wan = "c"; }
        ];
        h = {
          a = true;
          b = true;
          c = true;
        };
        results = builtins.genList (_: (selector.compute g h).active) 50;
      in
      builtins.all (r: r == builtins.head results) results;
    expected = true;
  };
}
