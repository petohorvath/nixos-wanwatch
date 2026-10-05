/*
  Integration tier: module-evaluation scenarios and rejection cases
  (PLAN §9.3), aggregated into one derivation whose symlinks record
  each realized check.

    scenarios/  — evaluate the module against a realistic declaration
                  and assert the rendered config and module outputs.
    rejections/ — declare an invalid config and prove that module
                  evaluation throws.
*/
{
  nixosModule,
  pkgs,
  telegrafModule,
}:

let
  inherit (pkgs) lib;

  scenarios = {
    base = import ./scenarios/base.nix { inherit nixosModule pkgs; };
    telegraf = import ./scenarios/telegraf.nix {
      inherit nixosModule pkgs telegrafModule;
    };
  };

  rejections = {
    probe-no-targets = import ./rejections/probe-no-targets.nix {
      inherit nixosModule pkgs;
    };
    probe-family-mismatch = import ./rejections/probe-family-mismatch.nix {
      inherit nixosModule pkgs;
    };
  };

  toSymlinkCommands =
    directory: prefix: checks:
    lib.concatMapAttrsStringSep "\n" (
      name: drv: "ln -s ${drv} $out/${directory}/${prefix}-${name}"
    ) checks;
in
pkgs.runCommand "wanwatch-integration" { } ''
  set -eu
  mkdir -p $out/scenarios $out/rejections
  ${toSymlinkCommands "scenarios" "scenario" scenarios}
  ${toSymlinkCommands "rejections" "rejection" rejections}
''
