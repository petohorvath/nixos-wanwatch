# The unit entry point: nix-unit suites that evaluate the wanwatch
# library and its option types. Every argument defaults to the root
# flake's locked input, so `nix-unit tests/unit` needs no arguments.
let
  rootInputs = (builtins.getFlake (toString ../..)).inputs;
  rebuild = import ../helpers/evaluation-inputs.nix;
in
{
  nixpkgs ? rootInputs.nixpkgs,
  # libnet follows nixpkgs, so rebuild it against the selected one.
  libnet ? rebuild {
    path = rootInputs.libnet.outPath;
    inputs = {
      inherit nixpkgs;
      flake-parts = rebuild {
        path = rootInputs.flake-parts.outPath;
        inputs.nixpkgs-lib = nixpkgs;
      };
    };
  },
  system ? builtins.currentSystem,
}:
let
  testContext = import ../test-context.nix { inherit libnet nixpkgs system; };
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
