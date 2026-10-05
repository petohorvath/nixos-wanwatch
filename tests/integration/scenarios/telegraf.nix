/*
  telegraf — happy-path module evaluation with the Telegraf companion
  module. Asserts the Prometheus input scrapes the daemon's metrics
  socket and the telegraf user joins the wanwatch group for read
  access.
*/
{
  nixosModule,
  pkgs,
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

  prometheusInput = builtins.head evaluated.config.services.telegraf.extraConfig.inputs.prometheus;
  telegrafGroups = evaluated.config.users.users.telegraf.extraGroups;
in
pkgs.runCommand "wanwatch-integration-telegraf" { } ''
  set -eu

  # Prometheus input points at the daemon's metrics socket.
  test "${builtins.head prometheusInput.urls}" = \
    "unix:///run/wanwatch/metrics.sock"
  test "${builtins.head prometheusInput.namepass}" = "wanwatch_*"
  test "${prometheusInput.interval}" = "10s"

  # Telegraf user joins the wanwatch group for socket read access.
  case " ${pkgs.lib.concatStringsSep " " telegrafGroups} " in
    *" wanwatch "*) ;;
    *) echo "telegraf user not in wanwatch group"; exit 1 ;;
  esac

  touch $out
''
