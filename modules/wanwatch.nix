/*
  services.wanwatch — declarative multi-WAN failover for NixOS.

  Renders the user's `wans` + `groups` declarations into the daemon
  config JSON (via `wanwatch.config.toJSON`), creates the
  wanwatch:wanwatch system user, and emits a hardened systemd unit
  for wanwatchd.

  Example:

    services.wanwatch = {
      enable = true;
      wans.primary = {
        interface = "eth0";
        probe.targets.v4 = [ "1.1.1.1" ];
      };
      groups.home-uplink.members = [
        { wan = "primary"; priority = 1; }
      ];
    };

  Cross-module wiring: `config.services.wanwatch.marks.<group>` and
  `.tables.<group>` expose the allocated fwmark / routing-table id
  for each group as read-only outputs, so nftzones (or any other
  consumer) can reference them by name without hard-coding numbers
  (PLAN §6).
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

  defaultPackage = pkgs.callPackage ../pkgs/wanwatchd.nix { };

  # The option types accept already-validated user inputs; round-trip
  # through `wanwatch.<type>.make` to get the tagged value form that
  # the JSON renderer's accessors expect.
  wanValues = lib.mapAttrs (_: w: wanwatch.wan.make w) cfg.wans;
  groupValues = lib.mapAttrs (_: g: wanwatch.group.make g) cfg.groups;

  resolved = wanwatch.config.resolveAllocations groupValues;

  renderedConfig = wanwatch.config.toJSON {
    global = cfg.global;
    wans = wanValues;
    groups = groupValues;
  };

  globalSubmodule = lib.types.submodule {
    options = {
      statePath = lib.mkOption {
        type = lib.types.str;
        default = wanwatch.config.defaultGlobal.statePath;
        description = ''
          Path the daemon writes state.json to atomically on every
          Decision. Lives under `/run` by default — the systemd
          unit creates the directory via RuntimeDirectory.
        '';
      };
      hooksDir = lib.mkOption {
        type = lib.types.str;
        default = wanwatch.config.defaultGlobal.hooksDir;
        description = ''
          Root of the hook-script tree. The daemon dispatches
          `<hooksDir>/{up,down,switch}.d/*` on every Decision per
          PLAN §5.5.
        '';
      };
      metricsSocket = lib.mkOption {
        type = lib.types.str;
        default = wanwatch.config.defaultGlobal.metricsSocket;
        description = ''
          Filesystem path the daemon listens on for Prometheus
          scrapes. Mode 0660 — Telegraf reads via supplementary
          group membership.
        '';
      };
      logLevel = lib.mkOption {
        type = lib.types.enum [
          "debug"
          "info"
          "warn"
          "error"
        ];
        default = wanwatch.config.defaultGlobal.logLevel;
        description = ''
          Minimum slog level emitted by the daemon. The `-log-level`
          flag overrides this at runtime.
        '';
      };
      hookTimeoutMs = lib.mkOption {
        type = lib.types.ints.positive;
        default = wanwatch.config.defaultGlobal.hookTimeoutMs;
        description = ''
          Per-hook execution deadline in milliseconds. A hook still
          running past this is killed (its process group is sent
          SIGKILL) and reported as a timeout. Applies to every script
          under `hooksDir`.
        '';
      };
    };
  };
in
{
  options.services.wanwatch = {
    enable = lib.mkEnableOption "the wanwatch multi-WAN failover daemon";

    package = lib.mkOption {
      type = lib.types.package;
      default = defaultPackage;
      defaultText = lib.literalExpression "pkgs.callPackage ../pkgs/wanwatchd.nix { }";
      description = "The wanwatchd derivation to run.";
    };

    user = lib.mkOption {
      type = lib.types.str;
      default = "wanwatch";
      description = ''
        Unix user the daemon runs as. The default `wanwatch` is
        created automatically; an override skips user creation and
        assumes the caller manages the account.
      '';
    };

    group = lib.mkOption {
      type = lib.types.str;
      default = "wanwatch";
      description = ''
        Unix group the daemon runs as. Telegraf scraping the metrics
        socket should join this group.
      '';
    };

    global = lib.mkOption {
      type = globalSubmodule;
      default = { };
      description = ''
        Global daemon settings — paths, log level, hook timeout.
        Each field defaults to `wanwatch.config.defaultGlobal`.
      '';
    };

    wans = lib.mkOption {
      type = lib.types.attrsOf wanwatch.types.wan;
      default = { };
      description = ''
        WAN declarations — one per uplink the daemon manages.
        The attribute key becomes the WAN's identifier.
      '';
    };

    groups = lib.mkOption {
      type = lib.types.attrsOf wanwatch.types.group;
      default = { };
      description = ''
        Group declarations — each is an ordered set of Members
        under a Strategy. The attribute key becomes the Group's
        identifier.
      '';
    };

    marks = lib.mkOption {
      type = lib.types.attrsOf lib.types.int;
      readOnly = true;
      default = lib.mapAttrs (_: g: g.mark) resolved;
      defaultText = lib.literalMD ''
        Computed from `groups` via `wanwatch.config.resolveAllocations`.
      '';
      description = ''
        Per-group fwmark, after auto-allocation + collision
        resolution. Read-only output for cross-module reference
        (e.g. nftzones).
      '';
    };

    tables = lib.mkOption {
      type = lib.types.attrsOf lib.types.int;
      readOnly = true;
      default = lib.mapAttrs (_: g: g.table) resolved;
      defaultText = lib.literalMD ''
        Computed from `groups` via `wanwatch.config.resolveAllocations`.
      '';
      description = ''
        Per-group routing-table id, after auto-allocation + collision
        resolution. Shared across v4 and v6 RIBs per PLAN §6.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    environment.etc."wanwatch/config.json".text = renderedConfig;

    # Tell systemd-networkd to leave foreign routing-policy rules
    # alone. The wanwatch daemon installs fwmark rules at bootstrap
    # and depends on them surviving until shutdown; networkd's
    # default `ManageForeignRoutingPolicyRules=yes` would delete
    # them during its periodic reconciliation pass. Only takes
    # effect when networkd is in use, so this is conditional on
    # the option existing — networkd-free deployments are
    # unaffected.
    systemd.network.config.networkConfig.ManageForeignRoutingPolicyRules = lib.mkDefault false;

    users.users = lib.mkIf (cfg.user == "wanwatch") {
      wanwatch = {
        isSystemUser = true;
        group = cfg.group;
        description = "wanwatch multi-WAN failover daemon";
      };
    };

    users.groups = lib.mkIf (cfg.group == "wanwatch") {
      wanwatch = { };
    };

    systemd.services.wanwatch = {
      description = "wanwatch multi-WAN failover daemon";
      documentation = [ "https://github.com/petohorvath/nixos-wanwatch" ];
      wantedBy = [ "multi-user.target" ];
      after = [ "network-pre.target" ];

      # A subsystem goroutine that dies cancels the daemon context
      # and forces a non-zero exit (cmd/wanwatchd/main.go), so
      # Restart=on-failure restarts the whole process. Bound the
      # loop: a persistent failure (missing capability, kernel
      # rejecting the netlink subscription, broken config) trips
      # StartLimitBurst and lands the unit in `failed` — surfacing
      # it to alerting instead of looping silently every RestartSec.
      startLimitIntervalSec = 300;
      startLimitBurst = 5;

      serviceConfig = {
        # Type=notify: the daemon sends sd_notify READY=1 once every
        # subsystem is wired, then a WATCHDOG=1 keepalive at half of
        # WatchdogSec — a stuck event loop trips the watchdog and
        # systemd restarts the unit.
        Type = "notify";
        ExecStart = "${cfg.package}/bin/wanwatchd -config /etc/wanwatch/config.json";
        Restart = "on-failure";
        RestartSec = "5s";
        WatchdogSec = "30s";

        User = cfg.user;
        Group = cfg.group;

        # CAP_NET_ADMIN for route/rule writes; CAP_NET_RAW for the
        # ICMP probe socket binding (PLAN §8).
        AmbientCapabilities = [
          "CAP_NET_ADMIN"
          "CAP_NET_RAW"
        ];
        CapabilityBoundingSet = [
          "CAP_NET_ADMIN"
          "CAP_NET_RAW"
        ];

        # Runtime dirs — systemd creates them with the daemon's
        # User:Group and tears them down on stop. statePath +
        # metricsSocket land here by default.
        RuntimeDirectory = "wanwatch";
        RuntimeDirectoryMode = "0755";

        # Hardening — drop everything the daemon doesn't need.
        # Netlink sockets need AF_NETLINK; ICMP probes need AF_INET
        # and AF_INET6; the metrics listener needs AF_UNIX.
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
