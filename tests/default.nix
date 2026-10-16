# The build and VM entries for one system; `dev/checks.nix` adds them
# to the checks on Linux only, because the daemon and the VMs are
# Linux-only.
{ inputs, pkgs }:
let
  inherit (pkgs) lib;
  testArgs = { inherit inputs pkgs; };
  prefixNames = prefix: lib.mapAttrs' (name: lib.nameValuePair "${prefix}-${name}");
in
lib.attrsets.unionOfDisjoint (prefixNames "build" (import ./build testArgs)) (
  prefixNames "vm" (import ./vm testArgs)
)
