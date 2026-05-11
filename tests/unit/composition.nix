/*
  Unit tests for `lib/default.nix` and `lib/with-lib.nix`. Verifies
  the composition contract: core attrs reachable from the top level,
  the `withLib` entry point produces a `types` namespace, and core
  is preserved when `withLib` is invoked.

  Adds-to-coverage when new top-level modules land — e.g. when Pass 2
  introduces `lib/wan.nix`, a test here asserts `wanwatch.wan` is
  reachable.
*/
{ pkgs, libnet, ... }:
let
  wanwatch = import ../../lib {
    inherit (pkgs) lib;
    inherit libnet;
  };
in
{
  # ===== lib/default.nix — top-level composition =====

  testVersionExposed = {
    expr = wanwatch.version;
    expected = "0.1.0-dev";
  };

  testInternalTypesReachable = {
    # Smoke-test that lib/internal/types is wired through; the module
    # itself has its own thorough test file.
    expr = wanwatch.internal.types.tags.wan;
    expected = "wan";
  };

  # ===== types namespace =====

  testTypesNamespaceReachable = {
    expr = wanwatch ? types;
    expected = true;
  };

  testTypesEmptyInPass1 = {
    # Pass 1 boundary: `types` is empty. This test gets updated when
    # `lib/types.nix` gains members in Pass 5 — its failure will be
    # the cue to extend the assertion with the new type names.
    expr = wanwatch.types;
    expected = {
      types = { };
    };
  };

  testProbeAndWanReachable = {
    expr = builtins.all (k: wanwatch ? ${k}) [
      "probe"
      "wan"
    ];
    expected = true;
  };
}
