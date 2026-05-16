/*
  probe-family-mismatch — a Probe with a v6 literal placed in the
  v4 target bucket (or vice versa) must be rejected at module-eval
  time. Proves probe.tryMake's per-bucket family-predicate check
  (probeTargetFamilyMismatch) is reached by the live module path.
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
        probe.targets.v4 = [ "2606:4700:4700::1111" ];
      };
      groups.x = {
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
      broken
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
