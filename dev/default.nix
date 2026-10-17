# The dev partition: the formatter, development shells, checks, and the
# nix-unit `tests` output. The root flake publishes these outputs from
# this partition.
{ inputs, self, ... }:
{
  imports = [
    inputs.nix-unit.modules.flake.default
    # Sets `formatter` and adds the `treefmt` check.
    inputs.treefmt-nix.flakeModule
  ];

  perSystem =
    {
      config,
      pkgs,
      system,
      ...
    }:
    {
      treefmt.imports = [ ./treefmt.nix ];

      devShells = {
        default = pkgs.callPackage ./shell.nix {
          treefmtShell = config.treefmt.build.devShell;
        };
        review = pkgs.callPackage ./review-shell.nix { };
      };

      checks = import ./checks.nix {
        inherit inputs pkgs;
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
        enableSystemAgnostic = false;
        tests = {
          unit = import ../tests/unit { inherit (inputs) libnet nixpkgs; };
          integration = import ../tests/integration {
            inherit (inputs) libnet nixpkgs self;
            inherit system;
          };
        };
      };
    };
}
