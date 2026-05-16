/*
  Unit tests for `lib/internal/marks.nix` (exposed as
  `wanwatch.marks`). The marks module is a thin wrapper over
  `internal/allocator.nix`; these tests assert the wiring
  (right base / size, no forbidden values) rather than the
  underlying algorithm — that's covered by `allocator.nix`
  tests.
*/
{ pkgs, libnet, ... }:
let
  wanwatch = import ../../../lib {
    inherit (pkgs) lib;
    inherit libnet;
  };
  inherit (wanwatch) marks;
in
{
  testAllocateReturnsFunction = {
    expr = builtins.isFunction marks.allocate;
    expected = true;
  };

  testAllocateEmptySet = {
    expr = marks.allocate [ ];
    expected = { };
  };

  testAllocateInMarkRange = {
    # All allocated marks must fall in [0x64, 0x7FFF] = [100, 32767].
    expr =
      let
        r = marks.allocate [
          "home"
          "guest"
          "iot"
        ];
        ids = builtins.attrValues r;
      in
      builtins.all (m: m >= 100 && m <= 32767) ids;
    expected = true;
  };

  testAllocateOrderInvariant = {
    # The allocator sorts its input internally, so list-order
    # permutations must produce identical outputs. Replaces the
    # tautological `allocate names == allocate names` self-equality
    # check this slot used to hold.
    expr =
      let
        a = marks.allocate [
          "home"
          "guest"
          "iot"
        ];
        b = marks.allocate [
          "iot"
          "home"
          "guest"
        ];
      in
      a == b;
    expected = true;
  };

  testAllocateAppendPreservesExisting = {
    # Adding a name that sorts after every existing one must leave
    # every existing assignment unchanged — the algorithm processes
    # names in sorted order, so a late append cannot displace
    # earlier entries even on hash collision. Pins the
    # "displacement is local" half of PLAN §12 OQ #2.
    expr =
      let
        base = marks.allocate [
          "alpha"
          "beta"
          "gamma"
        ];
        after = marks.allocate [
          "alpha"
          "beta"
          "gamma"
          "zulu"
        ];
      in
      base.alpha == after.alpha && base.beta == after.beta && base.gamma == after.gamma;
    expected = true;
  };

  testAllocateAssignsUnique = {
    expr =
      let
        names = [
          "home"
          "guest"
          "iot"
          "lab"
          "cctv"
        ];
        r = marks.allocate names;
        ids = builtins.attrValues r;
      in
      builtins.length ids == builtins.length (pkgs.lib.unique ids);
    expected = true;
  };

  testAllocateAssignsToEveryGroup = {
    expr =
      let
        names = [
          "home"
          "guest"
          "iot"
        ];
        r = marks.allocate names;
      in
      builtins.all (n: r ? ${n}) names;
    expected = true;
  };
}
