{
  description = "nixos-wanwatch — multi-WAN monitoring and failover for NixOS";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

    # A second nixpkgs pinned to the current stable channel. Used
    # exclusively by the `vm-stable-*` flake checks, which boot
    # NixOS VMs built against stable so kernel / systemd-networkd
    # / iproute2 regressions that hit unstable first don't ship
    # silently to stable users. Sibling-flake inputs (libnet,
    # nftzones, nftypes) continue to follow `nixpkgs` (unstable)
    # because their lib outputs are pure-Nix and don't depend on
    # any nixpkgs.lib API delta between channels.
    nixpkgs-stable.url = "github:NixOS/nixpkgs/nixos-25.11";

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

    # git-hooks.nix (formerly cachix/pre-commit-hooks.nix) installs
    # the pre-commit framework into .git/hooks on `nix develop` and
    # runs the hooks declared in `preCommitCheckFor` below: fast
    # checks at commit time, the heavy nix-flake-check / golangci-lint
    # run at push time.
    git-hooks.url = "github:cachix/git-hooks.nix";
    git-hooks.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs =
    {
      self,
      nixpkgs,
      nixpkgs-stable,
      libnet,
      nftzones,
      treefmt-nix,
      git-hooks,
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

      # preCommitCheckFor builds the git-hooks.nix hook set for `pkgs`.
      # Two stages, declared in one place so the lock-step between
      # what runs locally and what CI gates stays visible.
      #
      #   pre-commit (fast, ~1–2 s on a warm cache):
      #     - treefmt    — nixfmt + gofumpt + goimports
      #     - statix     — Nix anti-pattern lint
      #     - deadnix    — unused-binding lint
      #     - go-vet     — daemon-side go vet ./...
      #
      #   pre-push (heavy, ~10–15 s on a warm cache):
      #     - go-lint    — full golangci-lint suite
      #     - nix-checks — `nix build` of unit + integration + race +
      #                    coverage; same gates CI runs, so a regression
      #                    is caught before it leaves the laptop.
      #
      # `nix-checks` is Linux-only — the race + coverage + daemon
      # derivations are gated `optionalAttrs isLinux` in `checks`.
      preCommitCheckFor =
        pkgs:
        let
          inherit (pkgs.stdenv.hostPlatform) system;
        in
        git-hooks.lib.${system}.run {
          src = ./.;
          hooks = {
            treefmt = {
              enable = true;
              package = (treefmtFor pkgs).config.build.wrapper;
            };
            statix.enable = true;
            deadnix.enable = true;
            go-vet = {
              enable = true;
              name = "go vet (daemon)";
              description = "go vet ./... in the daemon module.";
              entry = "${pkgs.runtimeShell} -c 'cd daemon && ${pkgs.go}/bin/go vet ./...'";
              files = "^daemon/.*\\.go$";
              pass_filenames = false;
            };
            go-lint = {
              enable = true;
              name = "golangci-lint (daemon)";
              description = "Full golangci-lint suite on the daemon module.";
              entry = "${pkgs.runtimeShell} -c 'cd daemon && ${pkgs.golangci-lint}/bin/golangci-lint run ./...'";
              files = "^daemon/.*\\.go$";
              pass_filenames = false;
              stages = [ "pre-push" ];
            };
          }
          // nixpkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
            nix-checks = {
              enable = true;
              name = "nix flake checks (unit + integration + race + coverage)";
              description = "Build the Nix unit, integration, daemon race, and Go coverage gates.";
              entry = "${pkgs.runtimeShell} -c 'nix build --no-link .#checks.${system}.unit .#checks.${system}.integration .#checks.${system}.race .#checks.${system}.coverage'";
              pass_filenames = false;
              stages = [ "pre-push" ];
            };
          };
        };

      # Per-channel pkgs lookup. `nixpkgs` is the unstable input
      # everything else uses; `nixpkgs-stable` is consulted only
      # for the `vm-stable-*` checks.
      stablePkgsFor = system: nixpkgs-stable.legacyPackages.${system};

      # mkVmChecks returns the full set of VM scenarios built
      # against `pkgs`. Used twice from `checks`: once with the
      # unstable `pkgs` (the historical default) and once with the
      # stable channel's pkgs.
      mkVmChecks = pkgs: {
        smoke = import ./tests/vm/smoke.nix {
          inherit pkgs;
          nixosModule = self.nixosModules.default;
        };
        failover-v4 = import ./tests/vm/failover-v4.nix {
          inherit pkgs;
          nixosModule = self.nixosModules.default;
        };
        failover-v6 = import ./tests/vm/failover-v6.nix {
          inherit pkgs;
          nixosModule = self.nixosModules.default;
        };
        failover-dual-stack = import ./tests/vm/failover-dual-stack.nix {
          inherit pkgs;
          nixosModule = self.nixosModules.default;
        };
        failover-probe-loss = import ./tests/vm/failover-probe-loss.nix {
          inherit pkgs;
          nixosModule = self.nixosModules.default;
        };
        cold-start = import ./tests/vm/cold-start.nix {
          inherit pkgs;
          nixosModule = self.nixosModules.default;
        };
        recovery = import ./tests/vm/recovery.nix {
          inherit pkgs;
          nixosModule = self.nixosModules.default;
        };
        hooks = import ./tests/vm/hooks.nix {
          inherit pkgs;
          nixosModule = self.nixosModules.default;
        };
        metrics = import ./tests/vm/metrics.nix {
          inherit pkgs;
          nixosModule = self.nixosModules.default;
          telegrafModule = self.nixosModules.telegraf;
        };
        family-health-policy = import ./tests/vm/family-health-policy.nix {
          inherit pkgs;
          nixosModule = self.nixosModules.default;
        };
        gateway-discovery = import ./tests/vm/gateway-discovery.nix {
          inherit pkgs;
          nixosModule = self.nixosModules.default;
        };
        nftzones-integration = import ./tests/vm/nftzones-integration.nix {
          inherit pkgs;
          nixosModule = self.nixosModules.default;
          nftzonesModule = nftzones.nixosModules.default;
          nftypes = nftzones.inputs.nftypes.lib;
        };
      };

      # Flatten {name = drv;} → {vm-${name} = drv;} for one
      # channel's worth of VM scenarios.
      vmChecksWithPrefix =
        prefix: vmChecks:
        nixpkgs.lib.mapAttrs' (name: drv: nixpkgs.lib.nameValuePair "${prefix}${name}" drv) vmChecks;
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
          pre-commit = preCommitCheckFor pkgs;
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

          # Per-package coverage gate per PLAN §9.2. Runs `go test
          # -cover` on every internal package and asserts each is at
          # or above its declared floor. `cmd/wanwatchd/` is exempt
          # per PLAN — it's wiring exercised by the VM tier — so it
          # has no floor entry.
          #
          # Floors are tuned to current measured coverage: a gate
          # that fails on the first PR because the codebase doesn't
          # meet aspirational targets has no signal value. Tighten
          # numbers upward as coverage genuinely improves; loosen
          # only with a doc-comment explaining what regressed and
          # why it was acceptable.
          coverage =
            pkgs.runCommand "wanwatch-daemon-coverage"
              {
                src = ./daemon;
                nativeBuildInputs = [ pkgs.go ];
                GOFLAGS = "-mod=vendor";
                GOPROXY = "off";
                GOSUMDB = "off";
                CGO_ENABLED = "0";
              }
              ''
                export HOME=$TMPDIR
                export GOCACHE=$TMPDIR/gocache
                mkdir -p source
                cp -r $src/* source/
                chmod -R u+w source
                cd source

                # Floor table: "<pkg>:<percent-as-integer>". Format
                # mirrors PLAN §9.2; keep this list as the single
                # source of truth — CI just reads it back.
                cat > coverage.thresholds <<'EOF'
                internal/apply:90
                internal/config:100
                internal/metrics:88
                internal/probe:86
                internal/rtnl:91
                internal/selector:100
                internal/state:94
                EOF

                go test -cover ./internal/... > coverage.out 2>&1 || {
                    cat coverage.out
                    echo "coverage: go test failed" >&2
                    exit 1
                }
                cat coverage.out

                fail=0
                while IFS=: read -r pkg floor; do
                    # Skip blank lines / heredoc-induced whitespace.
                    pkg=$(echo "$pkg" | tr -d '[:space:]')
                    floor=$(echo "$floor" | tr -d '[:space:]')
                    [ -z "$pkg" ] && continue

                    # `go test -cover` prints one line per package:
                    #   ok  <module>/<pkg>  0.012s  coverage: 88.6% of statements
                    line=$(grep "/$pkg[[:space:]]" coverage.out || true)
                    if [ -z "$line" ]; then
                        echo "coverage: $pkg — no test output found" >&2
                        fail=1
                        continue
                    fi
                    pct=$(echo "$line" | sed -n 's/.*coverage: \([0-9.]*\)%.*/\1/p')
                    if [ -z "$pct" ]; then
                        echo "coverage: $pkg — could not parse line: $line" >&2
                        fail=1
                        continue
                    fi
                    # Compare as integer percent (truncate fractions);
                    # awk does the float→bool. `< floor` ⇒ fail.
                    if awk -v p="$pct" -v f="$floor" 'BEGIN{ exit !(p+0 < f+0) }'; then
                        printf 'coverage: %-22s %5s%% < floor %s%% — FAIL\n' "$pkg" "$pct" "$floor" >&2
                        fail=1
                    else
                        printf 'coverage: %-22s %5s%% ≥ floor %s%% — ok\n' "$pkg" "$pct" "$floor"
                    fi
                done < coverage.thresholds

                if [ "$fail" -ne 0 ]; then
                    echo "coverage: one or more packages regressed below their floor" >&2
                    exit 1
                fi
                touch $out
              '';

          # Race-detector pass. `go test -race` links the runtime
          # against the race runtime — needs cgo and a C toolchain
          # in PATH (hence `pkgs.gcc` here; the `daemon` and
          # `coverage` checks run with CGO_ENABLED=0 to keep their
          # closures slim).
          race =
            pkgs.runCommand "wanwatch-daemon-race"
              {
                src = ./daemon;
                nativeBuildInputs = [
                  pkgs.go
                  pkgs.gcc
                ];
                GOFLAGS = "-mod=vendor";
                GOPROXY = "off";
                GOSUMDB = "off";
                # CGO_ENABLED=1 is the whole point — without it,
                # `-race` errors out with "race requires cgo".
                CGO_ENABLED = "1";
              }
              ''
                export HOME=$TMPDIR
                export GOCACHE=$TMPDIR/gocache
                mkdir -p source
                cp -r $src/* source/
                chmod -R u+w source
                cd source
                go test -race -timeout 120s ./...
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
            nixosModule = self.nixosModules.default;
            telegrafModule = self.nixosModules.telegraf;
          };

        }
        // nixpkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux (
          # VM tier: boot a real NixOS VM, start the daemon, and
          # assert end-to-end behavior the unit + integration
          # tiers can't reach (capabilities, systemd hardening,
          # netlink-bound apply, real socket modes). Linux+KVM
          # only.
          #
          # Every scenario is materialized against both the
          # unstable nixpkgs and the current stable channel
          # (`vm-*` vs `vm-stable-*`). Stable catches regressions
          # before they reach release users; unstable surfaces
          # the newer-kernel / newer-systemd issues stable will
          # pick up next.
          vmChecksWithPrefix "vm-" (mkVmChecks pkgs)
          // vmChecksWithPrefix "vm-stable-" (mkVmChecks (stablePkgsFor pkgs.stdenv.hostPlatform.system))
        )
      );

      devShells = forAllSystems (
        pkgs:
        let
          # `go test -race` needs a C toolchain — the race runtime
          # is compiled and linked via cgo. mkShell (not -NoCC)
          # provides the stdenv with gcc on its PATH so `go test
          # -race ./...` Just Works inside `nix develop`.
          base = [
            (treefmtFor pkgs).config.build.wrapper
            pkgs.nixfmt
            pkgs.go
            pkgs.gopls
            pkgs.gotools
            pkgs.golangci-lint
            pkgs.gofumpt
            # Nix linters surfaced for both the pre-commit hook and
            # for manual runs (`statix check`, `deadnix`).
            pkgs.statix
            pkgs.deadnix
          ];
          preCommit = preCommitCheckFor pkgs;
        in
        {
          # The pre-commit shellHook installs .git/hooks/{pre-commit,
          # pre-push} on every `nix develop` entry — declarative
          # config means a fresh clone is fully wired by one
          # `nix develop`.
          default = pkgs.mkShell {
            packages = base;
            inherit (preCommit) shellHook;
          };
        }
      );
    };
}
