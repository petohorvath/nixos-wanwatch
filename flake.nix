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

    # Installs the Git hooks declared in `preCommitCheckFor` when
    # `nix develop` starts.
    git-hooks.url = "github:cachix/git-hooks.nix";
    git-hooks.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs =
    {
      self,
      nixpkgs,
      nixpkgs-unstable,
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

      forAllSystems =
        makeOutput: nixpkgs.lib.genAttrs systems (system: makeOutput nixpkgs.legacyPackages.${system});

      treefmtFor = pkgs: treefmt-nix.lib.evalModule pkgs ./treefmt.nix;

      /*
        Git hooks for `pkgs`. Pre-commit runs the fast checks (treefmt,
        statix, deadnix, go vet); pre-push runs golangci-lint and, on
        Linux, the unit, integration, race, and coverage checks that CI
        also gates on.
      */
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
            # statix and deadnix scan the whole repository, so findings
            # in unstaged files cannot slip past the hook.
            statix = {
              enable = true;
              pass_filenames = false;
              entry = "${pkgs.lib.getExe pkgs.statix} check .";
            };
            deadnix = {
              enable = true;
              pass_filenames = false;
              entry = "${pkgs.lib.getExe pkgs.deadnix} --fail .";
            };
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
              # golangci-lint runs `go` (and gcc for cgo), and the hook may
              # run outside `nix develop`.
              entry = ''
                ${pkgs.runtimeShell} -c 'export PATH="${
                  pkgs.lib.makeBinPath [
                    pkgs.go
                    pkgs.gcc
                  ]
                }:$PATH" && cd daemon &&
                  ${pkgs.golangci-lint}/bin/golangci-lint run ./...'
              '';
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

      unstablePkgsFor = system: nixpkgs-unstable.legacyPackages.${system};

      # Every VM scenario built against `pkgs`. `extraArgs` supplies the
      # additional modules and libraries some scenarios take.
      makeVmChecks =
        pkgs:
        let
          scenario =
            name: extraArgs:
            import (./tests/vm + "/${name}.nix") (
              {
                inherit pkgs;
                nixosModule = self.nixosModules.default;
              }
              // extraArgs
            );
        in
        {
          smoke = scenario "smoke" { };
          failover-v4 = scenario "failover-v4" { };
          failover-v6 = scenario "failover-v6" { };
          failover-dual-stack = scenario "failover-dual-stack" { };
          failover-probe-loss = scenario "failover-probe-loss" { };
          failover-probe-loss-v6 = scenario "failover-probe-loss-v6" { };
          cold-start = scenario "cold-start" { };
          recovery = scenario "recovery" { };
          recovery-v6 = scenario "recovery-v6" { };
          hooks = scenario "hooks" { };
          metrics = scenario "metrics" {
            telegrafModule = self.nixosModules.telegraf;
          };
          family-health-policy = scenario "family-health-policy" { };
          gateway-discovery = scenario "gateway-discovery" { };
          gateway-discovery-v6 = scenario "gateway-discovery-v6" { };
          nftzones-integration = scenario "nftzones-integration" {
            nftzonesModule = nftzones.nixosModules.default;
            nftypes = nftzones.inputs.nftypes.lib;
          };
        };

      prefixNames =
        prefix: attrs:
        nixpkgs.lib.mapAttrs' (name: value: nixpkgs.lib.nameValuePair "${prefix}${name}" value) attrs;

      /*
        A sandboxed `go test` run over `./daemon`. The vendored modules
        and disabled proxy make any network access fail. Go refuses a
        go.mod directly in the build's temporary root, so the script
        copies the source into a subdirectory first.

        `pkgs`: the package set providing Go.
        `name`: the derivation name.
        `cgo`: whether to enable cgo and add gcc, as `-race` requires.
        `script`: shell commands run in the source copy.

        Returns a derivation that builds when `script` succeeds.
      */
      runGoTests =
        pkgs:
        {
          name,
          cgo ? false,
          script,
        }:
        pkgs.runCommand name
          {
            src = ./daemon;
            nativeBuildInputs = [ pkgs.go ] ++ nixpkgs.lib.optional cgo pkgs.gcc;
            GOFLAGS = "-mod=vendor";
            GOPROXY = "off";
            GOSUMDB = "off";
            # Only `-race` needs cgo; netns, the one cgo dependency, is
            # unreachable from wanwatch.
            CGO_ENABLED = if cgo then "1" else "0";
          }
          ''
            export HOME=$TMPDIR
            export GOCACHE=$TMPDIR/gocache
            mkdir -p source
            cp -r $src/* source/
            chmod -R u+w source
            cd source
            ${script}
            touch $out
          '';
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
        let
          # The version comes from `lib/default.nix`, its single source.
          wanwatchd = pkgs.callPackage ./pkgs/wanwatchd.nix {
            inherit (self.lib) version;
            revision = "unknown";
          };
        in
        nixpkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
          inherit wanwatchd;
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
          # Tests the VM observation helpers without booting a VM.
          observation = pkgs.runCommand "wanwatch-observation-tests" { } ''
            ${pkgs.python3}/bin/python3 -B -m unittest discover \
              -s ${./tests/vm} -p test_observation.py -v
            touch $out
          '';
          pre-commit = preCommitCheckFor pkgs;
        }
        // nixpkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
          daemon = runGoTests pkgs {
            name = "wanwatch-daemon-tests";
            script = "go test -v ./...";
          };

          /*
            Per-package coverage floors (AGENTS.md); `cmd/wanwatchd` is
            exempt because the VM tier exercises it. Floors track measured
            coverage: raise them as coverage improves, and lower one only
            with a comment explaining the regression.
          */
          coverage = runGoTests pkgs {
            name = "wanwatch-daemon-coverage";
            script = ''
              cat > coverage.thresholds <<'EOF'
              internal/apply:90
              internal/config:100
              internal/decision:100
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
                  # Skip blank lines and heredoc indentation.
                  pkg=$(echo "$pkg" | tr -d '[:space:]')
                  floor=$(echo "$floor" | tr -d '[:space:]')
                  [ -z "$pkg" ] && continue

                  # Matches lines such as
                  #   ok  <module>/<pkg>  0.012s  coverage: 88.6% of ...
                  line=$(grep "/$pkg[[:space:]]" coverage.out || true)
                  if [ -z "$line" ]; then
                      echo "coverage: $pkg — no test output found" >&2
                      fail=1
                      continue
                  fi
                  pct=$(echo "$line" |
                      sed -n 's/.*coverage: \([0-9.]*\)%.*/\1/p')
                  if [ -z "$pct" ]; then
                      echo "coverage: $pkg — could not parse line: $line" >&2
                      fail=1
                      continue
                  fi
                  # awk compares the fractional percentages.
                  if awk -v p="$pct" -v f="$floor" \
                      'BEGIN{ exit !(p+0 < f+0) }'; then
                      printf 'coverage: %-22s %5s%% < floor %s%% — FAIL\n' \
                          "$pkg" "$pct" "$floor" >&2
                      fail=1
                  else
                      printf 'coverage: %-22s %5s%% ≥ floor %s%% — ok\n' \
                          "$pkg" "$pct" "$floor"
                  fi
              done < coverage.thresholds

              if [ "$fail" -ne 0 ]; then
                  echo "coverage: one or more packages regressed below" \
                      "their floor" >&2
                  exit 1
              fi
            '';
          };

          race = runGoTests pkgs {
            name = "wanwatch-daemon-race";
            cgo = true;
            script = "go test -race -timeout 120s ./...";
          };

          # Catches packaging regressions, such as a file missing from
          # the fileset, in `nix flake check`.
          package = self.packages.${pkgs.stdenv.hostPlatform.system}.wanwatchd;

          integration = import ./tests/integration {
            inherit pkgs;
            nixosModule = self.nixosModules.default;
            telegrafModule = self.nixosModules.telegraf;
          };
        }
        // nixpkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux (
          /*
            End-to-end scenarios in NixOS VMs, covering what evaluation
            cannot: capabilities, hardening, netlink, and socket modes.
            `vm-*` uses stable nixpkgs, as releases do; `vm-unstable-*`
            previews the kernel and systemd that stable will get next.
          */
          prefixNames "vm-" (makeVmChecks pkgs)
          // prefixNames "vm-unstable-" (makeVmChecks (unstablePkgsFor pkgs.stdenv.hostPlatform.system))
        )
      );

      devShells = forAllSystems (
        pkgs:
        let
          # mkShell rather than mkShellNoCC: `go test -race` needs gcc.
          packages = [
            (treefmtFor pkgs).config.build.wrapper
            pkgs.nixfmt
            pkgs.go
            pkgs.gopls
            pkgs.gotools
            pkgs.golangci-lint
            pkgs.gofumpt
            pkgs.statix
            pkgs.deadnix
          ];
          auditPkgs = unstablePkgsFor pkgs.stdenv.hostPlatform.system;
        in
        {
          # The shell hook installs the Git hooks on every entry.
          default = pkgs.mkShell {
            inherit packages;
            inherit (preCommitCheckFor pkgs) shellHook;
          };

          # Scanners for the vulnerability audit workflow, kept out of
          # the default shell.
          audit = pkgs.mkShellNoCC {
            packages = [
              auditPkgs.govulncheck
              auditPkgs.vulnix
            ];
          };
        }
        // nixpkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
          /*
            Review tools without the default shell's Git hooks. mkShell
            supplies gcc for race tests; Go and its modules come from the
            lock file and daemon/vendor, not from a preinstalled SDK or
            the module proxy.
          */
          review = pkgs.mkShell {
            packages = [ pkgs.go ];
            GOTOOLCHAIN = "local";
            GOENV = "off";
            GOFLAGS = "-mod=vendor";
            GOPROXY = "off";
            GOSUMDB = "off";
            CGO_ENABLED = "1";
          };
        }
      );
    };
}
