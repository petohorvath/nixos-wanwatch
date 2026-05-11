{
  description = "nixos-wanwatch — multi-WAN monitoring and failover for NixOS";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

    treefmt-nix.url = "github:numtide/treefmt-nix";
    treefmt-nix.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs =
    {
      self,
      nixpkgs,
      treefmt-nix,
    }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];

      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});

      treefmtFor = pkgs: treefmt-nix.lib.evalModule pkgs ./treefmt.nix;
    in
    {
      lib = import ./lib;

      formatter = forAllSystems (pkgs: (treefmtFor pkgs).config.build.wrapper);

      checks = forAllSystems (pkgs: {
        format = (treefmtFor pkgs).config.build.check self;
        unit = import ./tests/unit { inherit pkgs; };
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShellNoCC {
          packages = [
            (treefmtFor pkgs).config.build.wrapper
            pkgs.nixfmt
            pkgs.go
            pkgs.gopls
            pkgs.gotools
            pkgs.golangci-lint
            pkgs.gofumpt
          ];
        };
      });
    };
}
