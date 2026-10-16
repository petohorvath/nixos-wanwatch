/*
  The integration entry point: nix-unit suites that evaluate the
  wanwatch NixOS modules in NixOS systems, and the flake's outputs
  through flake-parts, without a VM or kernel. Every argument defaults
  to the root flake's locked input, so `nix-unit tests/integration`
  needs no arguments.
*/
let
  rootInputs = (builtins.getFlake (toString ../..)).inputs;
  rebuild = import ../helpers/evaluation-inputs.nix;
in
{
  nixpkgs ? rootInputs.nixpkgs,
  # flake-parts and libnet follow nixpkgs, so rebuild them against the
  # selected one.
  flake-parts ? rebuild {
    path = rootInputs.flake-parts.outPath;
    inputs.nixpkgs-lib = nixpkgs;
  },
  libnet ? rebuild {
    path = rootInputs.libnet.outPath;
    inputs = { inherit flake-parts nixpkgs; };
  },
  system ? builtins.currentSystem,
}:
let
  testContext = import ../test-context.nix {
    inherit
      flake-parts
      libnet
      nixpkgs
      system
      ;
  };
in
{
  flake-outputs = import ./flake-outputs.nix testContext;
  nixos-module = import ./nixos-module.nix testContext;
  rejections = import ./rejections.nix testContext;
  telegraf-module = import ./telegraf-module.nix testContext;
}
