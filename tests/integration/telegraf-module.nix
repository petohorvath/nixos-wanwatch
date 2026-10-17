# The Telegraf module beside the NixOS module in a NixOS system.
{ fixtures, helpers, ... }:
let
  telegrafConfig = helpers.evalNixosWith { telegraf = true; } [
    fixtures.nixos.primaryAndBackup
    {
      services.wanwatch.telegraf.enable = true;
      services.telegraf.enable = true;
    }
  ];
in
{
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
}
