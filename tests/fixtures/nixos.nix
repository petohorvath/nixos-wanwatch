/*
  `services.wanwatch` declarations shared by the integration suites;
  `test-context.nix` passes them to every suite in `fixtures.nixos`.

    primaryAndBackup — a primary and a backup WAN in one Group.
    oneWan           — one WAN in one Group, faulty only in the given
                       Targets or Members.
*/
{
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

  oneWan =
    {
      targets ? {
        v4 = [ "1.1.1.1" ];
      },
      members ? {
        broken = { };
      },
    }:
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
    };
}
