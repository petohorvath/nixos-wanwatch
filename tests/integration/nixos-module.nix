# The NixOS module in a NixOS system: the daemon config it renders,
# the values it publishes, and the service it defines.
{ fixtures, helpers, ... }:
let
  inherit (helpers) evalNixos renderConfig;

  baseConfig = evalNixos [ fixtures.nixos.primaryAndBackup ];
  rendered = renderConfig baseConfig;
in
{
  testSchema = {
    expr = rendered.schema;
    expected = 1;
  };

  testRendersGlobalDefaults = {
    expr = rendered.global;
    expected = {
      statePath = "/run/wanwatch/state.json";
      hooksDir = "/etc/wanwatch/hooks";
      metricsSocket = "/run/wanwatch/metrics.sock";
      logLevel = "info";
      hookTimeoutMs = 5000;
    };
  };

  testRendersGlobalSettings = {
    expr =
      (renderConfig (evalNixos [
        fixtures.nixos.primaryAndBackup
        { services.wanwatch.global.logLevel = "debug"; }
      ])).global.logLevel;
    expected = "debug";
  };

  testRendersWans = {
    expr = builtins.mapAttrs (_: wan: {
      inherit (wan) interface pointToPoint;
      inherit (wan.probe) targets;
    }) rendered.wans;
    expected = {
      primary = {
        interface = "eth0";
        pointToPoint = false;
        targets = {
          v4 = [ "1.1.1.1" ];
          v6 = [ "2606:4700:4700::1111" ];
        };
      };
      backup = {
        interface = "wwan0";
        pointToPoint = false;
        targets = {
          v4 = [ "8.8.8.8" ];
          v6 = [ ];
        };
      };
    };
  };

  testRendersGroup = {
    expr = rendered.groups;
    expected.home-uplink = {
      name = "home-uplink";
      strategy = "primary-backup";
      mark = 1000;
      table = 1000;
      members = {
        primary = {
          wan = "primary";
          priority = 1;
        };
        backup = {
          wan = "backup";
          priority = 2;
        };
      };
    };
  };

  # Other modules, such as nftzones, read the published values.
  testPublishesMarksAndTables = {
    expr = {
      inherit (baseConfig.services.wanwatch) marks tables;
    };
    expected = {
      marks.home-uplink = 1000;
      tables.home-uplink = 1000;
    };
  };

  testServiceIdentity = {
    expr = {
      inherit (baseConfig.systemd.services.wanwatch.serviceConfig) User Group RuntimeDirectory;
    };
    expected = {
      User = "wanwatch";
      Group = "wanwatch";
      RuntimeDirectory = "wanwatch";
    };
  };

  # `switch-to-configuration` restarts the service only when its unit
  # changes, and a config-only change leaves ExecStart as it was.
  testConfigChangeChangesRestartTriggers = {
    expr =
      baseConfig.systemd.services.wanwatch.restartTriggers == (evalNixos [
        fixtures.nixos.primaryAndBackup
        { services.wanwatch.global.logLevel = "debug"; }
      ]).systemd.services.wanwatch.restartTriggers;
    expected = false;
  };

  testSameConfigKeepsRestartTriggers = {
    expr =
      baseConfig.systemd.services.wanwatch.restartTriggers
      == (evalNixos [ fixtures.nixos.primaryAndBackup ]).systemd.services.wanwatch.restartTriggers;
    expected = true;
  };

  testServiceCapabilities = {
    expr = baseConfig.systemd.services.wanwatch.serviceConfig.AmbientCapabilities;
    expected = [
      "CAP_NET_ADMIN"
      "CAP_NET_RAW"
    ];
  };
}
