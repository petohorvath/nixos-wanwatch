/*
  base — happy-path module evaluation for a primary + backup WAN pair
  in one Group. Asserts the rendered daemon config shape, that the
  user-declared mark and table reach both the config and the
  `services.wanwatch.marks` / `.tables` outputs, and the systemd
  unit's capabilities. Module evaluation only; no VM or kernel.
*/
{
  nixosModule,
  pkgs,
}:

let
  inherit (pkgs) lib;

  config = {
    services.wanwatch = {
      enable = true;
      wans = {
        primary = {
          interface = "eth0";
          probe.targets = {
            v4 = [ "1.1.1.1" ];
            v6 = [ "2606:4700:4700::1111" ];
          };
        };
        backup = {
          interface = "wwan0";
          probe.targets.v4 = [ "8.8.8.8" ];
        };
      };
      groups.home-uplink = {
        members = [
          {
            wan = "primary";
            priority = 1;
          }
          {
            wan = "backup";
            priority = 2;
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
      config
    ];
  };

  rendered = lib.pipe evaluated.config.environment.etc."wanwatch/config.json".text [
    (pkgs.writeText "config.json")
    builtins.readFile
    builtins.fromJSON
  ];

  serviceConfig = evaluated.config.systemd.services.wanwatch.serviceConfig;
  ambientCapabilities = lib.concatStringsSep " " serviceConfig.AmbientCapabilities;
in
pkgs.runCommand "wanwatch-integration-base"
  {
    passAsFile = [ "renderedJSON" ];
    renderedJSON = builtins.toJSON rendered;
  }
  ''
    set -eu

    jqRendered() {
      ${pkgs.jq}/bin/jq "$@" < "$renderedJSONPath"
    }

    # 1. Rendered config has the expected schema version.
    test "$(jqRendered -r '.schema')" = "1"

    # 2. Both WANs are present.
    jqRendered -e '.wans.primary.interface == "eth0"'
    jqRendered -e '.wans.backup.interface == "wwan0"'

    # 3. The group is present with both members.
    jqRendered -e '.groups."home-uplink".members | length == 2'

    # 4. The user-declared mark and table are rendered as numbers.
    jqRendered -e '.groups."home-uplink".mark | type == "number"'
    jqRendered -e '.groups."home-uplink".table | type == "number"'

    # 5. Cross-module outputs match the rendered values.
    mark='${toString evaluated.config.services.wanwatch.marks.home-uplink}'
    table='${toString evaluated.config.services.wanwatch.tables.home-uplink}'
    test "$(jqRendered -r '.groups."home-uplink".mark')" = "$mark"
    test "$(jqRendered -r '.groups."home-uplink".table')" = "$table"

    # 6. systemd unit is wired with the right capabilities.
    test "${ambientCapabilities}" = "CAP_NET_ADMIN CAP_NET_RAW"

    touch $out
  ''
