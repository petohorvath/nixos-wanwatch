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
      `--override-input nix-libnet path:/absolute/path/to/nix-libnet`;
      relative paths resolve inside the store copy of this flake.
    */
    nix-libnet = {
      url = "github:petohorvath/nix-libnet/v0.1.0";
      inputs = {
        nixpkgs.follows = "nixpkgs";
        flake-parts.follows = "flake-parts";
      };
    };

    # Used only by the nftzones-integration VM scenario. Pinned by
    # commit until nixos-nftzones publishes a release tag. The pinned
    # commit still names its inputs `libnet` and `nftypes`.
    nixos-nftzones = {
      url = "github:petohorvath/nixos-nftzones?rev=dc5d2eff8877865de79c7577c2255191636090b6";
      inputs = {
        nixpkgs.follows = "nixpkgs";
        libnet.follows = "nix-libnet";
        nftypes.url = "github:petohorvath/nix-nftypes/v0.1.0";
        nftypes.inputs = {
          nixpkgs.follows = "nixpkgs";
          flake-parts.follows = "flake-parts";
        };
      };
    };

    # Formats and lints the repository through its flake-parts module:
    # `nix fmt` and the `treefmt` check. Its nixpkgs follows the root
    # input, so the tools come from the locked nixpkgs.
    treefmt-nix = {
      url = "github:numtide/treefmt-nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };

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
      nix-libnet,
      nixpkgs,
      self,
      ...
    }:
    let
      # The revision linked into wanwatchd for `wanwatch_build_info`,
      # for both the `packages` output and the module's default
      # package. A dirty tree has only a dirty short revision, and a
      # source without Git metadata has none.
      revision = self.shortRev or self.dirtyShortRev or "unknown";
    in
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
            inherit pkgs revision;
            # The version comes from `lib/default.nix`, its single source.
            inherit (self.lib) version;
          };
        };

      flake = {
        lib = import ./lib {
          inherit (nixpkgs) lib;
          libnet = nix-libnet.lib.withLib nixpkgs.lib;
        };

        nixosModules = {
          default = flake-parts.lib.importApply ./nixos/module.nix {
            wanwatch = self.lib;
            inherit revision;
          };
          wanwatch = self.nixosModules.default;
          telegraf = flake-parts.lib.importApply ./nixos/telegraf.nix { wanwatch = self.lib; };
        };
      };
    };
}
