/*
  The dev partition: the formatter, development shells, checks, and the
  nix-unit `tests` output. The root flake publishes these outputs from
  this partition.
*/
{
  config,
  inputs,
  lib,
  self,
  ...
}:
{
  # nix-unit tests: `nix-unit --flake .#tests`. The `unit` and
  # `integration` checks run them in the sandbox.
  flake.tests = {
    unit = import ../tests/unit {
      inherit (inputs.nixpkgs) lib;
      libnet = inputs.libnet.lib.withLib inputs.nixpkgs.lib;
      wanwatch = self.lib;
    };
    integration = lib.genAttrs (lib.filter (lib.hasSuffix "-linux") config.systems) (
      system:
      import ../tests/integration {
        inherit system;
        inherit (inputs.nixpkgs.lib) nixosSystem;
        inherit (self) nixosModules;
      }
    );
  };

  perSystem =
    {
      config,
      pkgs,
      system,
      ...
    }:
    let
      treefmt = inputs.treefmt-nix.lib.evalModule pkgs ./formatter.nix;
      unstablePkgs = inputs.nixpkgs-unstable.legacyPackages.${system};
    in
    {
      formatter = treefmt.config.build.wrapper;

      devShells = {
        default = pkgs.callPackage ./shell.nix { inherit (config) formatter; };
        audit = pkgs.callPackage ./audit-shell.nix {
          inherit (unstablePkgs) govulncheck vulnix;
        };
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
