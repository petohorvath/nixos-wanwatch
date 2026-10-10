{
  description = "nixos-wanwatch — multi-WAN monitoring and failover for NixOS";

  inputs = {
    # The stable release branch. Every output except the `vm-unstable-*`
    # checks and the audit shell builds against it, and the other inputs
    # follow it so their `lib` outputs match.
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

    # The nixos-unstable branch, for the `vm-unstable-*` checks that
    # surface kernel, networkd, and iproute2 changes before stable gets
    # them, and for the audit shell's scanners.
    nixpkgs-unstable.url = "github:NixOS/nixpkgs/nixos-unstable";

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
    libnet.url = "github:petohorvath/nix-libnet";
    libnet.inputs.nixpkgs.follows = "nixpkgs";

    # Used only by the nftzones-integration VM scenario.
    nftzones = {
      url = "github:petohorvath/nix-nftzones";
      inputs = {
        nixpkgs.follows = "nixpkgs";
        libnet.follows = "libnet";
        nftypes.url = "github:petohorvath/nix-nftypes";
        nftypes.inputs.nixpkgs.follows = "nixpkgs";
      };
    };

    treefmt-nix.url = "github:numtide/treefmt-nix";
    treefmt-nix.inputs.nixpkgs.follows = "nixpkgs";
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
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
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
          telegraf = ./nixos/telegraf.nix;
        };
      };
    };
}
