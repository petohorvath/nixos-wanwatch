/*
  probe-family-mismatch — module evaluation must reject a v6 literal
  in the v4 target bucket, proving the module reaches the per-bucket
  family check in `probe.tryMake` (probeTargetFamilyMismatch).
*/
{
  nixosModule,
  pkgs,
}:

let
  invalidConfig = {
    services.wanwatch = {
      enable = true;
      wans.broken = {
        interface = "eth0";
        probe.targets.v4 = [ "2606:4700:4700::1111" ];
      };
      groups.uplink = {
        members = [
          {
            wan = "broken";
            priority = 1;
          }
        ];
        mark = 1000;
        table = 1000;
      };
    };

    boot.isContainer = true;
    system.stateVersion = "24.11";
  };

  evaluated = import (pkgs.path + "/nixos/lib/eval-config.nix") {
    inherit (pkgs.stdenv.hostPlatform) system;
    modules = [
      nixosModule
      invalidConfig
    ];
  };

  attempt = builtins.tryEval evaluated.config.environment.etc."wanwatch/config.json".text;
in
if attempt.success then
  throw ''
    integration/rejections/probe-family-mismatch: expected module
    evaluation to throw (probeTargetFamilyMismatch), but it succeeded.
  ''
else
  pkgs.runCommand "wanwatch-rejection-probe-family-mismatch" { } ''
    echo "ok: v6-literal-in-v4-bucket config rejected at module eval"
    touch $out
  ''
