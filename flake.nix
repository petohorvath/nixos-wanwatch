{
  description = "nixos-wanwatch — multi-WAN monitoring and failover for NixOS";

  inputs = {
    /*
      The stable release branch, the only nixpkgs input. The other
      inputs follow it so their `lib` outputs match. CI also runs the
      checks with the project policy's stable and unstable pins by
      overriding this input (ADR-0012, ADR-0013).
    */
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

    flake-parts = {
      url = "github:hercules-ci/flake-parts";
      inputs.nixpkgs-lib.follows = "nixpkgs";
    };

    /*
      IP, CIDR, and interface-name validation used throughout `lib/`.
      To develop against a local checkout, pass
      `--override-input libnet path:/absolute/path/to/nix-libnet`;
      relative paths resolve inside the store copy of this flake.
    */
    libnet = {
      url = "github:petohorvath/nix-libnet/v0.1.0";
      inputs = {
        nixpkgs.follows = "nixpkgs";
        flake-parts.follows = "flake-parts";
      };
    };

    # Used only by the nftzones-integration VM scenario. Pinned by
    # commit until nixos-nftzones publishes a release tag.
    nftzones = {
      url = "github:petohorvath/nixos-nftzones?rev=dc5d2eff8877865de79c7577c2255191636090b6";
      inputs = {
        nixpkgs.follows = "nixpkgs";
        libnet.follows = "libnet";
        nftypes.url = "github:petohorvath/nix-nftypes/v0.1.0";
        nftypes.inputs = {
          nixpkgs.follows = "nixpkgs";
          flake-parts.follows = "flake-parts";
        };
      };
    };

    treefmt-nix.url = "github:numtide/treefmt-nix";
    treefmt-nix.inputs.nixpkgs.follows = "nixpkgs";

    # Runs the nix-unit suites through its flake-parts module. Its
    # nixpkgs follows the root input, and the dev partition takes the
    # nix-unit package from nixpkgs.
    nix-unit = {
      url = "github:nix-community/nix-unit";
      inputs = {
        nixpkgs.follows = "nixpkgs";
        treefmt-nix.follows = "";
        nix-github-actions.follows = "";
      };
    };
  };

  outputs =
    inputs@{
      flake-parts,
      libnet,
      nixpkgs,
      self,
      ...
    }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      # The daemon and the NixOS module are Linux-only.
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];

      # The development outputs evaluate in a separate partition, so
      # the library, modules, and packages evaluate without `dev/`.
      imports = [ flake-parts.flakeModules.partitions ];

      partitions.dev.module = ./dev;

      partitionedAttrs = {
        checks = "dev";
        devShells = "dev";
        formatter = "dev";
        tests = "dev";
      };

      perSystem =
        { pkgs, ... }:
        {
          packages = import ./packages {
            inherit pkgs;
            # The version comes from `lib/default.nix`, its single source.
            inherit (self.lib) version;
          };
        };

      flake = {
        lib = import ./lib {
          inherit (nixpkgs) lib;
          libnet = libnet.lib.withLib nixpkgs.lib;
        };

        nixosModules = {
          default = flake-parts.lib.importApply ./nixos/module.nix { wanwatch = self.lib; };
          wanwatch = self.nixosModules.default;
          telegraf = flake-parts.lib.importApply ./nixos/telegraf.nix { wanwatch = self.lib; };
        };
      };
    };
}
