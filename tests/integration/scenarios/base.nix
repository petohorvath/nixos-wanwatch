/*
  base — happy-path module-eval scenario. Declares a minimal
  "primary + backup, one home group" config, evaluates the wanwatch
  module against the full nixpkgs module corpus, and asserts:

    - the rendered daemon config carries the expected schema + shape
    - mark / table allocators produced non-null ints
    - `services.wanwatch.marks` / `.tables` echo the rendered values
    - the systemd unit is wired with the required capabilities

  Module-eval only — no VM, no kernel. Catches option type / assertion
  / defaultText drift before the VM tier pays the cost of a real boot.
*/
{
  pkgs,
  nixosModule,
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
      groups.home-uplink.members = [
        {
          wan = "primary";
          priority = 1;
        }
        {
          wan = "backup";
          priority = 2;
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
      config
    ];
  };

  rendered = builtins.fromJSON (
    builtins.readFile (
      pkgs.writeText "config.json" evaluated.config.environment.etc."wanwatch/config.json".text
    )
  );

  serviceCfg = evaluated.config.systemd.services.wanwatch.serviceConfig;
  ambientCaps = lib.concatStringsSep " " serviceCfg.AmbientCapabilities;
in
pkgs.runCommand "wanwatch-integration-base"
  {
    passAsFile = [ "renderedJSON" ];
    renderedJSON = builtins.toJSON rendered;
  }
  ''
    set -eu

    # 1. Rendered config has the expected schema version + top-level shape.
    test "$(${pkgs.jq}/bin/jq -r '.schema' < "$renderedJSONPath")" = "1"

    # 2. Both WANs are present.
    ${pkgs.jq}/bin/jq -e '.wans.primary.interface == "eth0"' < "$renderedJSONPath"
    ${pkgs.jq}/bin/jq -e '.wans.backup.interface == "wwan0"' < "$renderedJSONPath"

    # 3. The group is present and members are in priority order.
    ${pkgs.jq}/bin/jq -e '.groups."home-uplink".members | length == 2' < "$renderedJSONPath"

    # 4. mark + table got allocated (non-null ints).
    ${pkgs.jq}/bin/jq -e '.groups."home-uplink".mark | type == "number"' < "$renderedJSONPath"
    ${pkgs.jq}/bin/jq -e '.groups."home-uplink".table | type == "number"' < "$renderedJSONPath"

    # 5. Cross-module outputs match the rendered values.
    mark='${toString evaluated.config.services.wanwatch.marks.home-uplink}'
    table='${toString evaluated.config.services.wanwatch.tables.home-uplink}'
    test "$(${pkgs.jq}/bin/jq -r '.groups."home-uplink".mark' < "$renderedJSONPath")" = "$mark"
    test "$(${pkgs.jq}/bin/jq -r '.groups."home-uplink".table' < "$renderedJSONPath")" = "$table"

    # 6. systemd unit is wired with the right capabilities.
    test "${ambientCaps}" = "CAP_NET_ADMIN CAP_NET_RAW"

    touch $out
  ''
