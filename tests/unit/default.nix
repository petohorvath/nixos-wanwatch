/*
  nix-unit tests for the wanwatch library, exposed as the flake's
  `tests.unit`. Each suite receives one test context and returns nested
  `testFoo = { expr; expected; }` attrsets, which nix-unit walks.
*/
{
  lib,
  libnet,
  wanwatch,
}:
let
  testContext = {
    inherit lib libnet wanwatch;
    fixtures = import ./fixtures.nix;
    helpers = import ./helpers.nix { inherit lib; };
  };
in
{
  composition = import ./composition.nix testContext;
  skeleton = import ./skeleton.nix testContext;

  internal = {
    config = import ./internal/config.nix testContext;
    group = import ./internal/group.nix testContext;
    member = import ./internal/member.nix testContext;
    primitives = import ./internal/primitives.nix testContext;
    probe = import ./internal/probe.nix testContext;
    selector = import ./internal/selector.nix testContext;
    wan = import ./internal/wan.nix testContext;
  };

  types = {
    group = import ./types/group.nix testContext;
    member = import ./types/member.nix testContext;
    primitives = import ./types/primitives.nix testContext;
    probe = import ./types/probe.nix testContext;
    wan = import ./types/wan.nix testContext;
  };
}
