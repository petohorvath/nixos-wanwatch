/*
  Unit-test definitions for the wanwatch pure-Nix library.

  Each `testFoo` attr is `{ expr; expected; }` and is consumed by
  `runner.runTests` via `lib.runTests`. Per-module test files
  (mirroring `lib/`'s layout) are merged in here as the library
  grows — see PLAN.md §10 build order.
*/
{ pkgs, libnet }:
let
  runner = import ./runner.nix { inherit pkgs; };
  args = { inherit pkgs libnet; };
in
runner.runTests (
  import ./internal/primitives.nix args
  // import ./internal/probe.nix args
  // import ./internal/member.nix args
  // import ./internal/wan.nix args
  // import ./composition.nix args
  // import ./skeleton.nix args
)
