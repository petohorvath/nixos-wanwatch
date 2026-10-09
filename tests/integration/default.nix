/*
  nix-unit integration tests, exposed as the flake's
  `tests.integration.<system>`. Each scenario evaluates the NixOS
  module with a realistic declaration, without a VM or kernel, and
  checks what it renders. The rejections prove that module evaluation
  still routes declarations through the library validators, which the
  unit tests call directly.
*/
{
  nixosModules,
  nixosSystem,
  system,
}:
let
  evaluate =
    modules:
    (nixosSystem {
      modules = [
        nixosModules.default
        {
          nixpkgs.hostPlatform = system;
          boot.isContainer = true;
          system.stateVersion = "24.11";
        }
      ]
      ++ modules;
    }).config;

  # Rendering the config forces `wan.make` and `group.make` on every
  # declaration.
  renderConfig = config: builtins.fromJSON config.environment.etc."wanwatch/config.json".text;

  # A primary and a backup WAN in one Group.
  primaryAndBackup = {
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
  };

  # A WAN whose only fault is its probe targets.
  evaluateWithTargets =
    targets:
    evaluate [
      {
        services.wanwatch = {
          enable = true;
          wans.broken = {
            interface = "eth0";
            probe = { inherit targets; };
          };
          groups.home = {
            members = [ { wan = "broken"; } ];
            mark = 1000;
            table = 1000;
          };
        };
      }
    ];

  baseConfig = evaluate [ primaryAndBackup ];
  rendered = renderConfig baseConfig;

  telegrafConfig = evaluate [
    nixosModules.telegraf
    primaryAndBackup
    {
      services.wanwatch.telegraf.enable = true;
      services.telegraf.enable = true;
    }
  ];
in
{
  base = {
    testSchema = {
      expr = rendered.schema;
      expected = 1;
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
        members = [
          {
            wan = "primary";
            priority = 1;
            weight = 100;
          }
          {
            wan = "backup";
            priority = 2;
            weight = 100;
          }
        ];
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

    testServiceCapabilities = {
      expr = baseConfig.systemd.services.wanwatch.serviceConfig.AmbientCapabilities;
      expected = [
        "CAP_NET_ADMIN"
        "CAP_NET_RAW"
      ];
    };
  };

  telegraf = {
    testScrapesMetricsSocket = {
      expr = telegrafConfig.services.telegraf.extraConfig.inputs.prometheus;
      expected = [
        {
          urls = [ "unix:///run/wanwatch/metrics.sock" ];
          namepass = [ "wanwatch_*" ];
          interval = "10s";
        }
      ];
    };

    # The metrics socket has mode 0660.
    testJoinsWanwatchGroup = {
      expr = builtins.elem "wanwatch" telegrafConfig.users.users.telegraf.extraGroups;
      expected = true;
    };
  };

  rejections = {
    testProbeNoTargets = {
      expr = renderConfig (evaluateWithTargets {
        v4 = [ ];
        v6 = [ ];
      });
      expectedError = {
        type = "ThrownError";
        msg = "\\[probeNoTargets]";
      };
    };

    testProbeFamilyMismatch = {
      expr = renderConfig (evaluateWithTargets {
        v4 = [ "2606:4700:4700::1111" ];
      });
      expectedError = {
        type = "ThrownError";
        msg = "\\[probeTargetFamilyMismatch]";
      };
    };
  };
}
