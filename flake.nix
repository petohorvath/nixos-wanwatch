{
  description = "nixos-wanwatch — multi-WAN monitoring and failover for NixOS";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

    # nix-libnet provides IP/CIDR/interface validation primitives
    # used throughout `lib/`. Pinned to the GitHub default so a
    # fresh clone works without further configuration. For
    # iterate-on-both-repos local development, override with
    # `--override-input libnet path:/abs/path/to/nix-libnet` (or
    # add to a per-tree `.envrc`); a relative `path:../nix-libnet`
    # does not resolve cleanly under pure evaluation because the
    # working copy is staged to `/nix/store/...` before `..` is
    # resolved.
    libnet.url = "github:petohorvath/nix-libnet";
    libnet.inputs.nixpkgs.follows = "nixpkgs";

    # nix-nftzones is only used by the nftzones-integration VM
    # scenario (tests/vm/nftzones-integration.nix). Same
    # local-dev override pattern as libnet.
    nftzones.url = "github:petohorvath/nix-nftzones";
    nftzones.inputs.nixpkgs.follows = "nixpkgs";
    nftzones.inputs.libnet.follows = "libnet";
    nftzones.inputs.nftypes.url = "github:petohorvath/nix-nftypes";
    nftzones.inputs.nftypes.inputs.nixpkgs.follows = "nixpkgs";

    treefmt-nix.url = "github:numtide/treefmt-nix";
    treefmt-nix.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs =
    {
      self,
      nixpkgs,
      libnet,
      nftzones,
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
      lib = import ./lib {
        inherit (nixpkgs) lib;
        libnet = libnet.lib.withLib nixpkgs.lib;
      };

      nixosModules = {
        default = import ./modules/wanwatch.nix { wanwatch = self.lib; };
        wanwatch = self.nixosModules.default;
        telegraf = import ./modules/telegraf.nix;
      };

      formatter = forAllSystems (pkgs: (treefmtFor pkgs).config.build.wrapper);

      packages = forAllSystems (
        pkgs:
        nixpkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux rec {
          wanwatchd = pkgs.callPackage ./pkgs/wanwatchd.nix { };
          default = wanwatchd;
        }
      );

      checks = forAllSystems (
        pkgs:
        {
          format = (treefmtFor pkgs).config.build.check self;
          unit = import ./tests/unit {
            inherit pkgs;
            libnet = libnet.lib.withLib pkgs.lib;
          };
        }
        // nixpkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
          daemon =
            pkgs.runCommand "wanwatch-daemon-tests"
              {
                src = ./daemon;
                nativeBuildInputs = [ pkgs.go ];
                # External deps are vendored under `daemon/vendor/` so the
                # build stays hermetic — Go's proxy/sumdb fetches are
                # disabled to make any accidental network access fail
                # loudly instead of silently downloading.
                GOFLAGS = "-mod=vendor";
                GOPROXY = "off";
                GOSUMDB = "off";
                # vishvananda/netlink pulls in netns, which uses cgo at
                # build time even though wanwatch never touches netns.
                # Pure-Go is sufficient for everything we need.
                CGO_ENABLED = "0";
              }
              ''
                # Go 1.24+ refuses to honour go.mod that sits directly in a
                # well-known system temp root (/build, /tmp). Stage the
                # source under a sub-directory to side-step that mitigation.
                export HOME=$TMPDIR
                export GOCACHE=$TMPDIR/gocache
                mkdir -p source
                cp -r $src/* source/
                chmod -R u+w source
                cd source
                go test -v ./...
                touch $out
              '';

          # Build the daemon as part of `nix flake check` so a
          # regression in `pkgs/wanwatchd.nix` (e.g. a missing source
          # file under `fileset`, a vendored-dep drift) fails CI
          # rather than waiting for an actual `nix build` invocation.
          package = self.packages.${pkgs.stdenv.hostPlatform.system}.wanwatchd;

          # Evaluate the NixOS module against a realistic
          # declaration and assert the rendered config + module
          # outputs are well-formed.
          integration = import ./tests/integration {
            inherit pkgs;
            wanwatch = self.lib;
            nixosModule = self.nixosModules.default;
            telegrafModule = self.nixosModules.telegraf;
          };

          # VM tier: boot a real NixOS VM, start the daemon, and
          # assert end-to-end behavior the unit + integration tiers
          # can't reach (capabilities, systemd hardening,
          # netlink-bound apply, real socket modes). Linux+KVM only.
          vm-smoke = import ./tests/vm/smoke.nix {
            inherit pkgs;
            nixosModule = self.nixosModules.default;
          };
          vm-failover-v4 = import ./tests/vm/failover-v4.nix {
            inherit pkgs;
            nixosModule = self.nixosModules.default;
          };
          vm-failover-v6 = import ./tests/vm/failover-v6.nix {
            inherit pkgs;
            nixosModule = self.nixosModules.default;
          };
          vm-failover-dual-stack = import ./tests/vm/failover-dual-stack.nix {
            inherit pkgs;
            nixosModule = self.nixosModules.default;
          };
          vm-recovery = import ./tests/vm/recovery.nix {
            inherit pkgs;
            nixosModule = self.nixosModules.default;
          };
          vm-hooks = import ./tests/vm/hooks.nix {
            inherit pkgs;
            nixosModule = self.nixosModules.default;
          };
          vm-metrics = import ./tests/vm/metrics.nix {
            inherit pkgs;
            nixosModule = self.nixosModules.default;
            telegrafModule = self.nixosModules.telegraf;
          };
          vm-family-health-policy = import ./tests/vm/family-health-policy.nix {
            inherit pkgs;
            nixosModule = self.nixosModules.default;
          };
          vm-gateway-discovery = import ./tests/vm/gateway-discovery.nix {
            inherit pkgs;
            nixosModule = self.nixosModules.default;
          };
          vm-nftzones-integration = import ./tests/vm/nftzones-integration.nix {
            inherit pkgs;
            nixosModule = self.nixosModules.default;
            nftzonesModule = nftzones.nixosModules.default;
            nftypes = nftzones.inputs.nftypes.lib;
          };
        }
      );

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
