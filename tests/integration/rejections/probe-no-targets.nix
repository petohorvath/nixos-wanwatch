/*
  probe-no-targets — a WAN whose Probe declares neither v4 nor v6
  targets must be rejected at module-eval time. This is the
  integration-tier proof that the Nix-side validator
  (probe.tryMake → probeNoTargets) is wired into the live module
  path — unit tests call probe.make directly, but if the module
  ever stopped routing user inputs through `make`, those tests
  would pass and a silent-not-probed WAN would ship.
*/
{
  pkgs,
  nixosModule,
}:

let
  broken = {
    services.wanwatch = {
      enable = true;
      wans.broken = {
        interface = "eth0";
        probe.targets = {
          v4 = [ ];
          v6 = [ ];
        };
      };
      groups.x.members = [
        {
          wan = "broken";
          priority = 1;
        }
      ];
    };

    boot.isContainer = true;
    system.stateVersion = "24.11";
  };

  evaluated = import (pkgs.path + "/nixos/lib/eval-config.nix") {
    inherit (pkgs.stdenv.hostPlatform) system;
    modules = [
      nixosModule
      broken
    ];
  };

  # Force evaluation of the path that drives wan.make under the hood.
  attempt = builtins.tryEval evaluated.config.environment.etc."wanwatch/config.json".text;
in
if attempt.success then
  throw ''
    integration/rejections/probe-no-targets: expected module evaluation
    to throw (probeNoTargets), but it succeeded. The validator may
    have been disconnected from the module path — unit tests call
    probe.make directly and would still pass.
  ''
else
  pkgs.runCommand "wanwatch-rejection-probe-no-targets" { } ''
    echo "ok: empty-targets config rejected at module eval"
    touch $out
  ''
