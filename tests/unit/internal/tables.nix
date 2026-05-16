/*
  Unit tests for `lib/internal/tables.nix` (exposed as
  `wanwatch.tables`). Like `marks.nix`, this module is a thin
  wrapper — the underlying algorithm is tested in
  `tests/unit/internal/allocator.nix`. These tests assert the
  wiring (right base / size / forbidden set) plus the contract.
*/
{ pkgs, libnet, ... }:
let
  wanwatch = import ../../../lib {
    inherit (pkgs) lib;
    inherit libnet;
  };
  inherit (wanwatch) tables;
in
{
  testAllocateReturnsFunction = {
    expr = builtins.isFunction tables.allocate;
    expected = true;
  };

  testAllocateEmptySet = {
    expr = tables.allocate [ ];
    expected = { };
  };

  testAllocateInTableRange = {
    # All allocated ids must fall in [100, 32766].
    expr =
      let
        r = tables.allocate [
          "home"
          "guest"
          "iot"
        ];
        ids = builtins.attrValues r;
      in
      builtins.all (id: id >= 100 && id <= 32766) ids;
    expected = true;
  };

  testAllocateNeverAssignsReserved = {
    # 253/254/255 are kernel-reserved (default/main/local) and
    # must never be allocated. Use a name set large enough to push
    # the probe through the neighborhood — the contract is that
    # no matter what hashes to where, those three ids stay free.
    expr =
      let
        # 64 names — comfortably below the 32664 capacity, but
        # enough to exercise many probe paths.
        names = builtins.genList (i: "group${toString i}") 64;
        r = tables.allocate names;
        ids = builtins.attrValues r;
      in
      !(builtins.any (id: id == 253 || id == 254 || id == 255) ids);
    expected = true;
  };

  testAllocateOrderInvariant = {
    # The allocator sorts its input internally, so list-order
    # permutations must produce identical outputs.
    expr =
      let
        a = tables.allocate [
          "home"
          "guest"
          "iot"
        ];
        b = tables.allocate [
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
    # every existing assignment unchanged — pins the "displacement
    # is local" half of PLAN §12 OQ #2.
    expr =
      let
        base = tables.allocate [
          "alpha"
          "beta"
          "gamma"
        ];
        after = tables.allocate [
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
        r = tables.allocate names;
        ids = builtins.attrValues r;
      in
      builtins.length ids == builtins.length (pkgs.lib.unique ids);
    expected = true;
  };
}
