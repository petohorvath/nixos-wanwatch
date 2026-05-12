/*
  services.wanwatch.telegraf — opt-in Telegraf scrape companion.

  Adds a Prometheus input to the host's Telegraf config that pulls
  `wanwatch_*` metrics from the daemon's Unix socket, and joins the
  telegraf account to the wanwatch group so the socket's 0660 mode
  resolves. PLAN §7.3.

  Example:

    services.telegraf.enable = true;
    services.wanwatch.enable = true;
    services.wanwatch.telegraf.enable = true;

  The wanwatch.nix and telegraf.nix modules are siblings — users
  who don't run Telegraf simply don't import telegraf.nix; the
  daemon's metrics endpoint behaves the same regardless.
*/
{
  config,
  lib,
  ...
}:

let
  cfg = config.services.wanwatch;
  tcfg = cfg.telegraf;
in
{
  options.services.wanwatch.telegraf = {
    enable = lib.mkEnableOption "Telegraf scrape of wanwatch metrics";

    interval = lib.mkOption {
      type = lib.types.str;
      default = "10s";
      example = "30s";
      description = ''
        Scrape interval passed verbatim to Telegraf's `[[inputs.prometheus]]`
        block. Sub-10s intervals stress the daemon's per-cycle hot
        path with no real observability benefit — keep it ≥10s
        unless debugging.
      '';
    };
  };

  config = lib.mkIf tcfg.enable {
    assertions = [
      {
        assertion = cfg.enable;
        message = ''
          services.wanwatch.telegraf.enable requires
          services.wanwatch.enable — the prometheus input has no
          socket to scrape otherwise.
        '';
      }
      {
        assertion = config.services.telegraf.enable;
        message = ''
          services.wanwatch.telegraf.enable requires
          services.telegraf.enable — the input would land in a
          config no service consumes.
        '';
      }
    ];

    services.telegraf.extraConfig.inputs.prometheus = [
      {
        urls = [ "unix://${cfg.global.metricsSocket}:/metrics" ];
        interval = tcfg.interval;
        namepass = [ "wanwatch_*" ];
      }
    ];

    # The daemon's metrics socket is 0660 wanwatch:wanwatch
    # (modules/wanwatch.nix). Telegraf reads via supplementary
    # group membership rather than relaxing the socket's perm.
    users.users.telegraf.extraGroups = [ cfg.group ];
  };
}
