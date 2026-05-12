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
    expected = "0.1.0";
  };

  testInternalNamespacesReachable = {
    # Smoke-test that every operational module is wired through;
    # each one has its own thorough test file.
    expr = builtins.all (k: wanwatch.internal ? ${k}) [
      "primitives"
      "probe"
      "wan"
    ];
    expected = true;
  };

  # ===== types namespace =====

  testTypesNamespaceReachable = {
    expr = wanwatch ? types;
    expected = true;
  };

  testTypesNamespaceHasMembers = {
    # `types` is the flattened merge of per-concept type files.
    # This test asserts that the primitives slot is populated and
    # reachable — per-type tests in `tests/unit/types/*.nix` cover
    # the contents.
    expr = builtins.all (k: wanwatch.types ? ${k}) [
      "identifier"
      "positiveInt"
      "pctInt"
    ];
    expected = true;
  };

  testProbeAndWanReachable = {
    expr = builtins.all (k: wanwatch ? ${k}) [
      "probe"
      "wan"
    ];
    expected = true;
  };
}
