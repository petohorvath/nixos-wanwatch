# The build entry point: tests that run a real program in a derivation.
{ inputs, pkgs }:
let
  testArgs = { inherit inputs pkgs; };
in
{
  daemon = import ./daemon.nix testArgs;
  observation = import ./observation.nix testArgs;
}
