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

  testAllocateIsDeterministic = {
    expr =
      let
        names = [
          "home"
          "guest"
        ];
      in
      marks.allocate names == marks.allocate names;
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
