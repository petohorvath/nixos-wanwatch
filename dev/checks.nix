/*
  The flake's checks for one system: the build and VM entries from
  `tests/`, plus the lint and package entries. The nix-unit and
  treefmt-nix modules add the `nix-unit` and `treefmt` entries beside
  them.

  `inputs`: the flake inputs, including `self`.
  `packages`: the flake's packages for this system.
  `pkgs`: the package set for this system.

  Returns an attrset of check derivations.
*/
{
  inputs,
  packages,
  pkgs,
}:
let
  inherit (pkgs) lib;
  runGoCheck = import ../tests/helpers/go-check.nix { inherit pkgs; };
in
lib.attrsets.unionOfDisjoint (import ../tests { inherit inputs pkgs; }) {
  # The linters that treefmt cannot run correctly: `statix fix` skips
  # the lints it cannot fix, and golangci-lint analyzes whole packages.
  lint = pkgs.linkFarm "wanwatch-lint" {
    nix =
      pkgs.runCommand "wanwatch-lint-nix"
        {
          src = ../.;
          nativeBuildInputs = [ pkgs.statix ];
        }
        ''
          cd "$src"
          statix check .
          touch "$out"
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
