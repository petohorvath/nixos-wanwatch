/*
  The flake's checks for one system: the build and VM entries from
  `tests/`, plus the formatting, lint, and package entries. The nix-unit
  module adds the `nix-unit` entry beside them.

  `inputs`: the flake inputs, including `self`.
  `packages`: the flake's packages for this system.
  `pkgs`: the package set for this system.
  `treefmt`: the evaluated treefmt-nix configuration.

  Returns an attrset of check derivations.
*/
{
  inputs,
  packages,
  pkgs,
  treefmt,
}:
let
  inherit (pkgs) lib;
  runGoCheck = import ../tests/helpers/go-check.nix { inherit pkgs; };
in
lib.attrsets.unionOfDisjoint (import ../tests { inherit inputs pkgs; }) {
  formatting = treefmt.config.build.check inputs.self;

  lint = pkgs.linkFarm "wanwatch-lint" {
    # statix and deadnix scan the whole repository.
    nix =
      pkgs.runCommand "wanwatch-lint-nix"
        {
          nativeBuildInputs = [
            pkgs.deadnix
            pkgs.statix
          ];
        }
        ''
          cd ${inputs.self}
          statix check .
          deadnix --fail .
          touch $out
        '';

    # The `.golangci.yml` linters, govet included. The config lives
    # at the repository root, outside the copied `daemon/` source.
    golangci-lint = runGoCheck {
      name = "wanwatch-lint-golangci";
      script = ''
        export GOLANGCI_LINT_CACHE=$TMPDIR/golangci-lint
        ${lib.getExe pkgs.golangci-lint} run \
          --config ${../.golangci.yml} ./...
      '';
    };
  };

  # Catches packaging regressions, such as a file missing from the
  # fileset, in `nix flake check`.
  package-wanwatchd = packages.wanwatchd;
}
