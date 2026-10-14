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
        members = {
          primary.priority = 1;
          backup.priority = 2;
        };
        mark = 1000;
        table = 1000;
      };
    };
  };

  # One WAN in one Group, faulty only in the given probe targets or
  # Members.
  evaluateOneWan =
    {
      targets ? {
        v4 = [ "1.1.1.1" ];
      },
      members ? {
        broken = { };
      },
    }:
    evaluate [
      {
        services.wanwatch = {
          enable = true;
          wans.broken = {
            interface = "eth0";
            probe = { inherit targets; };
          };
          groups.home = {
            inherit members;
            mark = 1000;
            table = 1000;
          };
        };
      }
    ];

  # ADR-0014: the module owns the daemon's identity and paths, so
  # setting a former option fails evaluation.
  rejectsRemovedOption = optionPattern: definition: {
    expr = renderConfig (evaluate [
      primaryAndBackup
      { services.wanwatch = definition; }
    ]);
    expectedError = {
      type = "ThrownError";
      msg = "${optionPattern}' does not exist";
    };
  };

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
        (renderConfig (evaluate [
          primaryAndBackup
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
      expr = renderConfig (evaluateOneWan {
        targets = {
          v4 = [ ];
          v6 = [ ];
        };
      });
      expectedError = {
        type = "ThrownError";
        msg = "\\[probeNoTargets]";
      };
    };

    testProbeFamilyMismatch = {
      expr = renderConfig (evaluateOneWan {
        targets.v4 = [ "2606:4700:4700::1111" ];
      });
      expectedError = {
        type = "ThrownError";
        msg = "\\[probeTargetFamilyMismatch]";
      };
    };

    testGroupNoMembers = {
      expr = renderConfig (evaluateOneWan {
        members = { };
      });
      expectedError = {
        type = "ThrownError";
        msg = "\\[groupNoMembers]";
      };
    };

    removedOptions = {
      testUser = rejectsRemovedOption "wanwatch\\.user" { user = "root"; };
      testGroup = rejectsRemovedOption "wanwatch\\.group" { group = "network"; };
      testStatePath = rejectsRemovedOption "global\\.statePath" {
        global.statePath = "/var/lib/wanwatch/state.json";
      };
      testHooksDir = rejectsRemovedOption "global\\.hooksDir" {
        global.hooksDir = "/var/lib/wanwatch/hooks";
      };
      testMetricsSocket = rejectsRemovedOption "global\\.metricsSocket" {
        global.metricsSocket = "/var/lib/wanwatch/metrics.sock";
      };
    };

    # A typo in a Member's WAN fails here rather than as a daemon
    # restart loop.
    testMemberUndeclaredWan = {
      expr = renderConfig (evaluate [
        primaryAndBackup
        {
          services.wanwatch.groups.home-uplink.members.bakcup = { };
        }
      ]);
      expectedError = {
        type = "ThrownError";
        msg = "group 'home-uplink' references wan 'bakcup'";
      };
    };
  };
}
