/*
  A check that runs the nix-unit tests at `tests.<attrPath>`, as
  nix-unit documents for flakes. The sandbox cannot fetch, so the inputs
  the tests evaluate are overridden with their store paths.

  `inputs`: the flake inputs, including `self`.
  `pkgs`: the package set providing nix-unit.
  `attrPath`: the attribute path below the flake's `tests` output.

  Returns a derivation that builds when every selected test passes.
*/
{ inputs, pkgs }:
attrPath:
pkgs.runCommand "wanwatch-nix-unit-${attrPath}" { nativeBuildInputs = [ pkgs.nix-unit ]; } ''
  export HOME=$TMPDIR
  nix-unit \
    --eval-store "$HOME" \
    --gc-roots-dir "$HOME/gc-roots" \
    --extra-experimental-features flakes \
    --override-input flake-parts path:${inputs.flake-parts} \
    --override-input libnet path:${inputs.libnet} \
    --override-input nixpkgs path:${inputs.nixpkgs} \
    --flake path:${inputs.self}#tests.${attrPath}
  touch $out
''
