/*
  The test entries of the flake's checks for one system; `dev/checks.nix`
  adds the formatting, lint, and package entries. The integration,
  build, and VM entries exist only on Linux, because the daemon and the
  NixOS systems they evaluate are Linux-only.

  `inputs`: the flake inputs, including `self`.
  `pkgs`: the package set for this system.

  Returns an attrset of check derivations.
*/
{ inputs, pkgs }:
let
  inherit (pkgs) lib;
  repo = "wanwatch";
  /*
    The files the nix-unit suites read. NixOS evaluation reaches the
    daemon package through the service's `ExecStart`, so the package
    and its sources are included; the daemon sources also hold the
    rendered-config golden.
  */
  srcDir = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../daemon
      ../flake.nix
      ../lib
      ../nixos
      ../packages
      ./fixtures
      ./helpers
      ./integration
      ./test-context.nix
      ./unit
    ];
  };
  runNixUnit = import ./runners/nix-unit.nix {
    inherit
      inputs
      pkgs
      repo
      srcDir
      ;
  };
  runBuild = import ./runners/build.nix { inherit pkgs repo; };
  runVm = import ./runners/vm.nix { inherit pkgs repo; };
in
lib.foldl' lib.attrsets.unionOfDisjoint { } (
  [
    { unit = runNixUnit { kind = "unit"; }; }
  ]
  ++ lib.optionals pkgs.stdenv.hostPlatform.isLinux [
    { integration = runNixUnit { kind = "integration"; }; }
    (runBuild (import ./build { inherit inputs pkgs; }))
    # The project policy runs these as VM tests, because NixOS tests
    # require the `nixos-test` system feature, and skips them on a
    # runner without KVM (ADR-0013).
    (runVm (import ./vm { inherit inputs pkgs; }))
  ]
)
