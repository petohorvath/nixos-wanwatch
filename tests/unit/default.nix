/*
  Unit-test entry point for the wanwatch pure-Nix library.

  Builds the library and the shared helpers once, passes them to every
  suite as one test context, and runs the merged `testFoo = { expr;
  expected; }` attrsets through `runner.nix`.
*/
{ pkgs, libnet }:
let
  inherit (pkgs) lib;

  runner = import ./runner.nix { inherit pkgs; };

  testContext = {
    inherit libnet pkgs;
    helpers = import ./helpers.nix { inherit pkgs; };
    wanwatch = import ../../lib { inherit lib libnet; };
  };
in
lib.pipe
  [
    ./internal/primitives.nix
    ./internal/probe.nix
    ./internal/member.nix
    ./internal/wan.nix
    ./internal/group.nix
    ./internal/selector.nix
    ./internal/config.nix
    ./types/primitives.nix
    ./types/probe.nix
    ./types/member.nix
    ./types/wan.nix
    ./types/group.nix
    ./composition.nix
    ./skeleton.nix
  ]
  [
    (map (suitePath: import suitePath testContext))
    lib.mergeAttrsList
    runner.runTests
  ]
