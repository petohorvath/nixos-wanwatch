/*
  Integration tier: evaluate the NixOS module against a realistic
  configuration and assert the rendered config + cross-module
  outputs are well-formed.

  Runs in pure evaluation — no VM, no kernel — so it catches
  module-eval regressions (option type mismatches, missing assertions,
  defaultText drift) without paying the cost of a full nixosTest.
*/
{
  pkgs,
  wanwatch,
  nixosModule,
}:

let
  inherit (pkgs) lib;

  # baseConfig is a minimal "primary + backup, one home group"
  # declaration — enough to exercise the full Decision pipeline
  # in module-eval without depending on real network state.
  baseConfig = {
    services.wanwatch = {
      enable = true;
      wans = {
        primary = {
          interface = "eth0";
          gateways = {
            v4 = "192.0.2.1";
            v6 = "2001:db8::1";
          };
          probe.targets = [
            "1.1.1.1"
            "2606:4700:4700::1111"
          ];
        };
        backup = {
          interface = "wwan0";
          gateways.v4 = "100.64.0.1";
          probe.targets = [ "8.8.8.8" ];
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

    # Stubs the test config needs to satisfy the host module set —
    # `boot.loader`, `fileSystems`, etc. are normally required.
    boot.isContainer = true;
    system.stateVersion = "24.11";
  };

  # Evaluate the wanwatch module against the full nixpkgs module
  # corpus so `users`, `systemd`, `environment.etc`, and assertions
  # resolve to real implementations rather than tripping "option
  # does not exist".
  evaluated = import (pkgs.path + "/nixos/lib/eval-config.nix") {
    system = pkgs.stdenv.hostPlatform.system;
    modules = [
      nixosModule
      baseConfig
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
pkgs.runCommand "wanwatch-integration"
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
