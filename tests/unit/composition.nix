/*
  Unit tests for `lib/default.nix` and `lib/with-lib.nix`. Verifies
  the composition contract: core attrs reachable from the top level,
  the `withLib` entry point produces a `types` namespace, and core
  is preserved when `withLib` is invoked.

  Adds-to-coverage when new top-level modules land — e.g. when Pass 2
  introduces `lib/wan.nix`, a test here asserts `wanwatch.wan` is
  reachable.
*/
{ pkgs, ... }:
let
  wanwatch = import ../../lib;
  withLibbed = wanwatch.withLib pkgs.lib;
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

  testWithLibIsFunction = {
    expr = builtins.isFunction wanwatch.withLib;
    expected = true;
  };

  # ===== with-lib — injection contract =====

  testWithLibAddsTypesNamespace = {
    expr = withLibbed ? types;
    expected = true;
  };

  testWithLibPreservesInternal = {
    # The core's `internal` namespace must be intact after withLib —
    # injection is additive, not replacement.
    expr = withLibbed.internal == wanwatch.internal;
    expected = true;
  };

  testWithLibPreservesVersion = {
    expr = withLibbed.version;
    expected = "0.1.0-dev";
  };

  testWithLibTypesEmptyInPass1 = {
    # Pass 1 boundary: `types` is empty. This test gets updated when
    # `lib/types.nix` gains members in Pass 5 — its failure will be
    # the cue to extend the assertion with the new type names.
    expr = withLibbed.types;
    expected = { };
  };

  # ===== Pure-Nix core invariant =====

  testCoreEvaluatesWithoutNixpkgsLib = {
    # The core library must be importable without `nixpkgs.lib`.
    # `import ../../lib` itself does the eval; if it accidentally
    # reaches for `pkgs.lib` (or `inputs.lib`, etc.) at file scope,
    # this import alone would fail. Existence of the version attr
    # proves the eval reached the end of `lib/default.nix`.
    expr = builtins.isString (import ../../lib).version;
    expected = true;
  };
}
