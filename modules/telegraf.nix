/*
  `services.wanwatch.telegraf`: optional Telegraf scraping of the
  daemon's metrics (docs/metrics.md). Adds a Prometheus input that reads
  `wanwatch_*` metrics from the daemon's Unix socket and adds the
  `telegraf` user to the daemon's group so it can open the socket.
  Import it alongside the main wanwatch module:

    services.telegraf.enable = true;
    services.wanwatch.enable = true;
    services.wanwatch.telegraf.enable = true;
*/
{
  config,
  lib,
  ...
}:

let
  cfg = config.services.wanwatch;
  telegrafCfg = cfg.telegraf;
in
{
  options.services.wanwatch.telegraf = {
    enable = lib.mkEnableOption "Telegraf scrape of wanwatch metrics";

    interval = lib.mkOption {
      type = lib.types.str;
      default = "10s";
      example = "30s";
      description = ''
        Scrape interval for Telegraf's `[[inputs.prometheus]]` block.
        Intervals under 10s load the daemon without improving
        observability, so use them only for debugging.
      '';
    };
  };

  config = lib.mkIf telegrafCfg.enable {
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
        # Current Telegraf treats a `:/metrics` suffix as part of the
        # socket path; a bare `unix://` URL uses the default `/metrics`.
        urls = [ "unix://${cfg.global.metricsSocket}" ];
        inherit (telegrafCfg) interval;
        namepass = [ "wanwatch_*" ];
      }
    ];

    # The metrics socket has mode 0660; group membership grants access
    # without relaxing it.
    users.users.telegraf.extraGroups = [ cfg.group ];
  };
}
