/*
  telegraf — happy-path module-eval with the opt-in Telegraf
  companion module enabled. Asserts that the prometheus input is
  wired to the daemon's metrics socket with the expected scrape
  parameters and that the telegraf user is added to the wanwatch
  group (so its supplementary-group membership grants socket read).
*/
{
  pkgs,
  nixosModule,
  telegrafModule,
}:

let
  config = {
    services.wanwatch = {
      enable = true;
      wans.primary = {
        interface = "eth0";
        probe.targets.v4 = [ "1.1.1.1" ];
      };
      groups.home-uplink = {
        members = [
          {
            wan = "primary";
            priority = 1;
          }
        ];
        mark = 1000;
        table = 1000;
      };
      telegraf.enable = true;
    };
    services.telegraf.enable = true;

    boot.isContainer = true;
    system.stateVersion = "24.11";
  };

  evaluated = import (pkgs.path + "/nixos/lib/eval-config.nix") {
    inherit (pkgs.stdenv.hostPlatform) system;
    modules = [
      nixosModule
      telegrafModule
      config
    ];
  };

  promInputs = evaluated.config.services.telegraf.extraConfig.inputs.prometheus;
  promInput = builtins.head promInputs;
  telegrafGroups = evaluated.config.users.users.telegraf.extraGroups;
in
pkgs.runCommand "wanwatch-integration-telegraf" { } ''
  set -eu

  # Prometheus input points at the daemon's metrics socket.
  test "${builtins.head promInput.urls}" = "unix:///run/wanwatch/metrics.sock"
  test "${builtins.head promInput.namepass}" = "wanwatch_*"
  test "${promInput.interval}" = "10s"

  # Telegraf user joins the wanwatch group for socket read access.
  case " ${pkgs.lib.concatStringsSep " " telegrafGroups} " in
    *" wanwatch "*) ;;
    *) echo "telegraf user not in wanwatch group"; exit 1 ;;
  esac

  touch $out
''
