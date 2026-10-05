/*
  probe-no-targets — module evaluation must reject a WAN whose Probe
  declares neither v4 nor v6 targets. Unit tests call `probe.make`
  directly, so only this check proves the module still routes user
  inputs through the validator (probeNoTargets).
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
        probe.targets = {
          v4 = [ ];
          v6 = [ ];
        };
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

  # Rendering the config forces `wan.make` on every declared WAN.
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
