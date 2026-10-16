# The dev partition: the formatter, development shells, and checks. The
# root flake publishes these outputs from this partition.
{
  inputs,
  lib,
  self,
  ...
}:
{
  perSystem =
    {
      config,
      pkgs,
      system,
      ...
    }:
    let
      treefmt = inputs.treefmt-nix.lib.evalModule pkgs ./formatter.nix;
    in
    {
      formatter = treefmt.config.build.wrapper;

      devShells = {
        default = pkgs.callPackage ./shell.nix { inherit (config) formatter; };
      }
      // lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
        review = pkgs.callPackage ./review-shell.nix { };
      };

      checks = import ./checks.nix {
        inherit inputs pkgs treefmt;
        packages = self.packages.${system};
      };
    };
}
