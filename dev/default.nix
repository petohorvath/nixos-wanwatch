# The dev partition: the formatter, development shells, checks, and the
# nix-unit `tests` output. The root flake publishes these outputs from
# this partition.
{
  inputs,
  lib,
  self,
  ...
}:
{
  imports = [ inputs.nix-unit.modules.flake.default ];

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

      # The `nix-unit` check runs these suites in the sandbox, with
      # every input passed as an override, so it fetches nothing.
      nix-unit = {
        package = pkgs.nix-unit;
        inputs = removeAttrs inputs [ "self" ] // {
          # nftzones' own input that follows no root input.
          "nftzones/nftypes" = inputs.nftzones.inputs.nftypes;
        };
        tests = {
          unit = import ../tests/unit {
            inherit (inputs) libnet nixpkgs;
            inherit system;
          };
        }
        # The integration suites evaluate NixOS, which is Linux-only.
        // lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
          integration = import ../tests/integration {
            inherit (inputs) libnet nixpkgs self;
            inherit system;
          };
        };
      };
    };
}
