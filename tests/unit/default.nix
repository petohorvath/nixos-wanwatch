/*
  Unit-test definitions for the wanwatch pure-Nix library.

  Each `testFoo` attr is `{ expr; expected; }` and is consumed by
  `runner.runTests` via `lib.runTests`. Per-module test files
  (mirroring `lib/`'s layout) are merged in here as the library
  grows — see PLAN.md §10 build order.

  Pass 1 boundary: harness only. The `testHarnessSelfCheck` placeholder
  exists so the runner has something to evaluate while `lib/` is being
  built up; it is removed in the same commit that lands the first
  real test file.
*/
{ pkgs }:
let
  runner = import ./runner.nix { inherit pkgs; };
in
runner.runTests {
  testHarnessSelfCheck = {
    expr = builtins.add 2 2;
    expected = 4;
  };
}
