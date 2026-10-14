/*
  `services.wanwatch`: multi-WAN monitoring and failover. Renders the
  declared WANs and Groups into the daemon configuration, creates the
  `wanwatch` system user and group, and runs wanwatchd as a hardened
  systemd service. The module fixes the user, the group, and the
  daemon's paths, because the sandbox leaves `/run/wanwatch` as the
  only writable directory other processes can see
  (docs/adr/0014-module-owns-identity-and-paths.md).

  Example:

    services.wanwatch = {
      enable = true;
      wans.primary = {
        interface = "eth0";
        probe.targets.v4 = [ "1.1.1.1" ];
      };
      groups.home-uplink = {
        members.primary = { };
        mark = 1000;
        table = 1000;
      };
    };

  The read-only `marks.<group>` and `tables.<group>` options echo each
  Group's fwmark and routing table, so other modules such as nftzones
  can reference them by name (docs/nftzones-integration.md).
*/
{ wanwatch }:

{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.services.wanwatch;

  defaultPackage = pkgs.callPackage ../packages/wanwatchd/package.nix {
    inherit (wanwatch) version;
  };

  # The renderer takes value-type values, and `make` also applies the
  # cross-field checks that option types cannot express.
  wanValues = lib.mapAttrs (_: wanwatch.wan.make) cfg.wans;
  groupValues = lib.mapAttrs (_: wanwatch.group.make) cfg.groups;

  validatedGroups = wanwatch.config.assertUniqueMarksAndTables groupValues;

  renderedConfig = wanwatch.config.toJSON {
    inherit (cfg) global;
    wans = wanValues;
    groups = groupValues;
  };
in
{
  options.services.wanwatch = {
    enable = lib.mkEnableOption "the wanwatch multi-WAN failover daemon";

    package = lib.mkOption {
      type = lib.types.package;
      default = defaultPackage;
      defaultText = lib.literalExpression "pkgs.callPackage ../packages/wanwatchd/package.nix { }";
      description = "The wanwatchd derivation to run.";
    };

    global = lib.mkOption {
      type = wanwatch.types.global;
      default = { };
      description = ''
        Global daemon settings: log level and Hook timeout. Each field
        defaults to `wanwatch.global.defaults`.
      '';
    };

    wans = lib.mkOption {
      type = lib.types.attrsOf wanwatch.types.wan;
      default = { };
      description = ''
        WANs the daemon monitors, one per uplink. Each attribute name
        is the WAN's identifier.
      '';
    };

    groups = lib.mkOption {
      type = lib.types.attrsOf wanwatch.types.group;
      default = { };
      description = ''
        Groups, each a set of Members under a Strategy. Each attribute
        name is the Group's identifier.
      '';
    };

    marks = lib.mkOption {
      type = lib.types.attrsOf lib.types.int;
      readOnly = true;
      default = lib.mapAttrs (_: group: group.mark) validatedGroups;
      defaultText = lib.literalMD ''
        Each Group's declared value, after
        `wanwatch.config.assertUniqueMarksAndTables` rejects
        duplicates.
      '';
      description = ''
        Read-only copy of each `services.wanwatch.groups.<group>.mark`.
        Other modules, such as nftzones, should reference these
        values rather than repeat the integers.
      '';
    };

    tables = lib.mkOption {
      type = lib.types.attrsOf lib.types.int;
      readOnly = true;
      default = lib.mapAttrs (_: group: group.table) validatedGroups;
      defaultText = lib.literalMD ''
        Each Group's declared value, after
        `wanwatch.config.assertUniqueMarksAndTables` rejects
        duplicates.
      '';
      description = ''
        Read-only copy of each `services.wanwatch.groups.<group>.table`.
        Each table ID serves both IPv4 and IPv6 routes (docs/nftzones-integration.md).
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    environment.etc."wanwatch/config.json".text = renderedConfig;

    /*
      systemd-networkd deletes foreign routing-policy rules and routes
      by default when it reconciles, removing the daemon's rules and
      routes between Decisions; VM tests saw State report an active
      WAN whose table was empty. The settings only affect hosts that
      run networkd.
    */
    systemd.network.config.networkConfig = {
      ManageForeignRoutingPolicyRules = lib.mkDefault false;
      ManageForeignRoutes = lib.mkDefault false;
    };

    # Hooks run as this user; grant them extra access through
    # `users.users.wanwatch.extraGroups`.
    users.users.wanwatch = {
      isSystemUser = true;
      group = "wanwatch";
      description = "wanwatch multi-WAN failover daemon";
    };

    # Metrics scrapers such as Telegraf join this group.
    users.groups.wanwatch = { };

    systemd.services.wanwatch = {
      description = "wanwatch multi-WAN failover daemon";
      documentation = [ "https://github.com/petohorvath/nixos-wanwatch" ];
      wantedBy = [ "multi-user.target" ];
      after = [ "network-pre.target" ];

      /*
        A failed daemon subsystem makes the process exit non-zero, and
        Restart=on-failure restarts it. The start limit turns a
        persistent failure, such as a missing capability or a broken
        config, into a `failed` unit that alerting can see.
      */
      startLimitIntervalSec = 300;
      startLimitBurst = 5;

      serviceConfig = {
        # The daemon reports READY=1 once every subsystem runs, then sends
        # WATCHDOG=1 at half of WatchdogSec, so systemd restarts a stuck
        # event loop.
        Type = "notify";
        ExecStart = "${cfg.package}/bin/wanwatchd -config /etc/wanwatch/config.json";
        Restart = "on-failure";
        RestartSec = "5s";
        WatchdogSec = "30s";

        User = "wanwatch";
        Group = "wanwatch";

        # CAP_NET_ADMIN for route/rule writes; CAP_NET_RAW for the
        # ICMP probe socket binding (docs/specs/probe-algorithm.md).
        AmbientCapabilities = [
          "CAP_NET_ADMIN"
          "CAP_NET_RAW"
        ];
        CapabilityBoundingSet = [
          "CAP_NET_ADMIN"
          "CAP_NET_RAW"
        ];

        # Holds state.json and the metrics socket; ProtectSystem=strict
        # leaves no other writable place that other processes can see.
        # systemd creates it for the service user and removes it on stop.
        RuntimeDirectory = "wanwatch";
        RuntimeDirectoryMode = "0755";

        # Hardening. Netlink needs AF_NETLINK, ICMP probes need AF_INET
        # and AF_INET6, and the metrics listener needs AF_UNIX.
        NoNewPrivileges = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
        PrivateDevices = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectKernelLogs = true;
        ProtectControlGroups = true;
        ProtectClock = true;
        ProtectHostname = true;
        ProtectProc = "invisible";
        RestrictAddressFamilies = [
          "AF_INET"
          "AF_INET6"
          "AF_NETLINK"
          "AF_UNIX"
        ];
        RestrictNamespaces = true;
        RestrictRealtime = true;
        RestrictSUIDSGID = true;
        LockPersonality = true;
        MemoryDenyWriteExecute = true;
        SystemCallArchitectures = "native";
        SystemCallFilter = [
          "@system-service"
          "~@privileged"
          "~@resources"
        ];
      };
    };

    assertions = [
      {
        assertion = cfg.wans != { } -> cfg.groups != { };
        message = ''
          services.wanwatch: declared `wans` but no `groups`. A WAN
          with no Group never carries traffic — declare at least one
          group, or remove the WAN.
        '';
      }
    ];
  };
}
