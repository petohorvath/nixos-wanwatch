# The NixOS module in a NixOS system: the daemon config it renders,
# the values it publishes, and the service it defines.
{ fixtures, helpers, ... }:
let
  inherit (helpers) evalNixos evalNixosOptions renderConfig;

  baseConfig = evalNixos [ fixtures.nixos.primaryAndBackup ];
  debugConfig = evalNixos [
    fixtures.nixos.primaryAndBackup
    { services.wanwatch.global.logLevel = "debug"; }
  ];
  rendered = renderConfig baseConfig;

  options = (evalNixosOptions [ fixtures.nixos.primaryAndBackup ]).services.wanwatch;

  # The unit file that `switch-to-configuration` compares to decide
  # whether to restart the service.
  unitText = config: config.systemd.units."wanwatch.service".text;
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
    expr = (renderConfig debugConfig).global.logLevel;
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

  # A config-only change leaves ExecStart as it was, so the unit
  # changes only through its restart trigger.
  testConfigChangeRestartsService = {
    expr = unitText baseConfig == unitText debugConfig;
    expected = false;
  };

  testSameConfigKeepsService = {
    expr = unitText baseConfig == unitText (evalNixos [ fixtures.nixos.primaryAndBackup ]);
    expected = true;
  };

  testServiceCapabilities = {
    expr = baseConfig.systemd.services.wanwatch.serviceConfig.AmbientCapabilities;
    expected = [
      "CAP_NET_ADMIN"
      "CAP_NET_RAW"
    ];
  };

  # The option docs name the package a consumer gets, not a path
  # inside this repository.
  testPackageDefaultText = {
    expr = options.package.defaultText;
    expected = {
      _type = "literalMD";
      text = "`wanwatchd` from this flake's `packages`, built with the system's `pkgs`";
    };
  };

  # The option types document the range of the published values.
  testMarksType = {
    expr = options.marks.type.description;
    expected = "attribute set of integer between 1000 and 32767 (both inclusive)";
  };

  testTablesType = {
    expr = options.tables.type.description;
    expected = "attribute set of integer between 1000 and 32767 (both inclusive)";
  };
}
