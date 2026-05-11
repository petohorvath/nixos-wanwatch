/*
  Unit tests for `lib/internal/allocator.nix` (exposed as
  `wanwatch.internal.allocator`).

  The allocator's output for any individual name depends on the
  full input set (hash collisions probe forward); these tests check
  the contract properties — determinism, uniqueness, range-membership,
  forbidden-set avoidance, order-invariance — rather than asserting
  specific allocations.
*/
{ pkgs, libnet, ... }:
let
  wanwatch = import ../../../lib {
    inherit (pkgs) lib;
    inherit libnet;
  };
  inherit (wanwatch.internal) allocator;
  inherit (import ../helpers.nix { inherit pkgs; }) evalThrows;

  # A small allocator (size 4) for collision-test scenarios.
  tiny = allocator.mkAllocator {
    base = 100;
    size = 4;
  };

  # A typical allocator with a forbidden set.
  withForbidden = allocator.mkAllocator {
    base = 100;
    size = 10;
    forbidden = [
      102
      105
    ];
  };

  # Marks-shaped allocator (matches the constants `internal.marks`
  # will use).
  marksLike = allocator.mkAllocator {
    base = 100;
    size = 32668;
  };
in
{
  # ===== hashToInt =====

  testHashToIntReturnsInt = {
    expr = builtins.isInt (allocator.hashToInt "primary");
    expected = true;
  };

  testHashToIntIs16Bit = {
    # First 4 hex chars of sha256 fit in [0, 65536).
    expr =
      let
        h = allocator.hashToInt "primary";
      in
      h >= 0 && h < 65536;
    expected = true;
  };

  testHashToIntDeterministic = {
    expr = allocator.hashToInt "primary" == allocator.hashToInt "primary";
    expected = true;
  };

  testHashToIntDifferentForDifferentNames = {
    # Vanishingly unlikely for two 16-bit prefixes to collide on
    # these particular strings — if this ever does, pick new test
    # inputs rather than weaken the assertion.
    expr = allocator.hashToInt "primary" != allocator.hashToInt "backup";
    expected = true;
  };

  # ===== validIds =====

  testValidIdsRangeOnly = {
    expr = allocator.validIds {
      base = 100;
      size = 5;
    };
    expected = [
      100
      101
      102
      103
      104
    ];
  };

  testValidIdsExcludesForbidden = {
    expr = allocator.validIds {
      base = 100;
      size = 5;
      forbidden = [
        101
        103
      ];
    };
    expected = [
      100
      102
      104
    ];
  };

  testValidIdsIgnoresForbiddenOutsideRange = {
    # Forbidden values not in [base, base+size) are simply ignored.
    expr = allocator.validIds {
      base = 100;
      size = 3;
      forbidden = [
        50
        500
      ];
    };
    expected = [
      100
      101
      102
    ];
  };

  # ===== mkAllocator — basic allocation =====

  testAllocateEmptySet = {
    expr = marksLike [ ];
    expected = { };
  };

  testAllocateSingleName = {
    expr =
      let
        r = marksLike [ "primary" ];
      in
      r ? primary && r.primary >= 100 && r.primary < 32768;
    expected = true;
  };

  testAllocateAssignsToEveryName = {
    expr =
      let
        names = [
          "primary"
          "backup"
          "guest"
          "iot"
        ];
        r = marksLike names;
      in
      builtins.all (n: r ? ${n}) names;
    expected = true;
  };

  # ===== Determinism =====

  testAllocateIsDeterministic = {
    # Same inputs → exact same output, repeatedly.
    expr =
      let
        names = [
          "alpha"
          "bravo"
          "charlie"
          "delta"
        ];
        a = marksLike names;
        b = marksLike names;
        c = marksLike names;
      in
      a == b && b == c;
    expected = true;
  };

  testAllocateIsOrderInvariant = {
    # Input list order doesn't matter — names get sorted internally.
    expr =
      let
        a = marksLike [
          "alpha"
          "bravo"
          "charlie"
        ];
        b = marksLike [
          "charlie"
          "alpha"
          "bravo"
        ];
        c = marksLike [
          "bravo"
          "charlie"
          "alpha"
        ];
      in
      a == b && b == c;
    expected = true;
  };

  # ===== Uniqueness =====

  testAllocateAssignsUniqueIds = {
    expr =
      let
        names = [
          "a"
          "b"
          "c"
          "d"
          "e"
          "f"
          "g"
          "h"
          "i"
          "j"
        ];
        r = marksLike names;
        ids = builtins.attrValues r;
        uniqueCount = builtins.length (pkgs.lib.unique ids);
      in
      uniqueCount == builtins.length names;
    expected = true;
  };

  # ===== Range membership =====

  testAllocateStaysInRange = {
    expr =
      let
        names = [
          "a"
          "b"
          "c"
          "d"
          "e"
        ];
        r = marksLike names;
      in
      builtins.all (id: id >= 100 && id < 32768) (builtins.attrValues r);
    expected = true;
  };

  # ===== Forbidden avoidance =====

  testAllocateRespectsForbidden = {
    # Allocate many names; none should land on a forbidden id.
    expr =
      let
        # 8 names into a size-10-minus-2-forbidden = 8-slot allocator.
        names = [
          "a"
          "b"
          "c"
          "d"
          "e"
          "f"
          "g"
          "h"
        ];
        r = withForbidden names;
        ids = builtins.attrValues r;
      in
      !(builtins.any (id: id == 102 || id == 105) ids);
    expected = true;
  };

  # ===== Probe — collision handling =====

  testAllocateHandlesFullSaturation = {
    # tiny has 4 slots. Allocate 4 names; every slot must be used
    # exactly once — even if all four hash to the same start.
    expr =
      let
        names = [
          "alpha"
          "bravo"
          "charlie"
          "delta"
        ];
        r = tiny names;
        ids = builtins.attrValues r;
        sortedIds = pkgs.lib.sort pkgs.lib.lessThan ids;
      in
      sortedIds == [
        100
        101
        102
        103
      ];
    expected = true;
  };

  # ===== Overflow =====

  testAllocateThrowsOnOverflow = {
    # tiny has 4 slots; 5 names is unrecoverable.
    expr = evalThrows (tiny [
      "a"
      "b"
      "c"
      "d"
      "e"
    ]);
    expected = true;
  };
}
