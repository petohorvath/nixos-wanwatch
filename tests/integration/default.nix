/*
  Integration tier — pure-eval scenarios + rejections (PLAN §9.3).

    scenarios/  — each file evaluates the module against a realistic
                  declaration and asserts the rendered config + cross-
                  module outputs are well-formed.
    rejections/ — each file declares an intentionally-invalid config
                  and proves module evaluation throws. Confirms the
                  Nix-side validators are wired into the live module
                  path, not just unit-tested in isolation.

  This file aggregates them into a single derivation so flake.nix
  keeps its existing `checks.<system>.integration` attribute. Each
  child still builds independently — symlinks below trace which
  scenarios + rejections were realized.
*/
{
  pkgs,
  nixosModule,
  telegrafModule,
}:

let
  inherit (pkgs) lib;

  scenarios = {
    base = import ./scenarios/base.nix { inherit pkgs nixosModule; };
    telegraf = import ./scenarios/telegraf.nix {
      inherit pkgs nixosModule telegrafModule;
    };
  };

  rejections = {
    probe-no-targets = import ./rejections/probe-no-targets.nix {
      inherit pkgs nixosModule;
    };
    probe-family-mismatch = import ./rejections/probe-family-mismatch.nix {
      inherit pkgs nixosModule;
    };
  };

  symlinkLines =
    group: prefix: drvs:
    lib.concatStringsSep "\n" (
      lib.mapAttrsToList (n: drv: "ln -s ${drv} $out/${group}/${prefix}-${n}") drvs
    );
in
pkgs.runCommand "wanwatch-integration" { } ''
  set -eu
  mkdir -p $out/scenarios $out/rejections
  ${symlinkLines "scenarios" "scenario" scenarios}
  ${symlinkLines "rejections" "rejection" rejections}
''
