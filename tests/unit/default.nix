# The unit entry point: nix-unit suites that evaluate the wanwatch
# library and its option types. `dev/default.nix` passes the flake's
# inputs.
{ libnet, nixpkgs }:
let
  testContext = import ../test-context.nix { inherit libnet nixpkgs; };
in
{
  lib = import ./lib.nix testContext;
  skeleton = import ./skeleton.nix testContext;

  internal = {
    config = import ./internal/config.nix testContext;
    global = import ./internal/global.nix testContext;
    group = import ./internal/group.nix testContext;
    member = import ./internal/member.nix testContext;
    primitives = import ./internal/primitives.nix testContext;
    probe = import ./internal/probe.nix testContext;
    selector = import ./internal/selector.nix testContext;
    wan = import ./internal/wan.nix testContext;
  };

  types = {
    global = import ./types/global.nix testContext;
    group = import ./types/group.nix testContext;
    member = import ./types/member.nix testContext;
    primitives = import ./types/primitives.nix testContext;
    probe = import ./types/probe.nix testContext;
    wan = import ./types/wan.nix testContext;
  };
}
