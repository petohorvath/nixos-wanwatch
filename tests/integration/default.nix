/*
  The integration entry point: nix-unit suites that evaluate the
  wanwatch NixOS modules in NixOS systems, and the flake's own outputs,
  without a VM or kernel. `dev/default.nix` passes the flake's inputs,
  the flake itself as `self`, and the system.
*/
{
  nix-libnet,
  nixpkgs,
  self,
  system,
}:
let
  testContext = import ../test-context.nix {
    inherit
      nix-libnet
      nixpkgs
      self
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
