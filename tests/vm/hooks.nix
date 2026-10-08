/*
  hooks — trigger a carrier-driven switch Decision and verify that a
  capturing Hook receives the docs/specs/daemon-state.md WANWATCH_* environment. Both
  WANs are pointToPoint, so the gateway variables are empty.
*/
{
  pkgs,
  nixosModule,
}:

let
  # Hooks run as the wanwatch user, which can write only under
  # /run/wanwatch.
  captureHook = pkgs.writeShellScript "capture-env.sh" ''
    set -eu
    env | grep '^WANWATCH_' | sort > /run/wanwatch/last-hook.env
  '';
in
pkgs.testers.runNixOSTest {
  name = "wanwatch-hooks";

  nodes.router =
    { lib, ... }:
    {
      imports = [ nixosModule ];

      boot.kernelModules = [ "dummy" ];

      systemd.network.netdevs = {
        "10-wan0".netdevConfig = {
          Kind = "dummy";
          Name = "wan0";
        };
        "10-wan1".netdevConfig = {
          Kind = "dummy";
          Name = "wan1";
        };
      };
      systemd.network.networks = {
        "20-wan0" = {
          matchConfig.Name = "wan0";
          networkConfig.LinkLocalAddressing = "ipv6";
          linkConfig.RequiredForOnline = "no";
          address = [
            "192.0.2.10/24"
            "2001:db8::10/64"
          ];
        };
        "20-wan1" = {
          matchConfig.Name = "wan1";
          networkConfig.LinkLocalAddressing = "ipv6";
          linkConfig.RequiredForOnline = "no";
          address = [
            "100.64.0.10/24"
            "2001:db8:1::10/64"
          ];
        };
      };
      networking = {
        useNetworkd = true;
        useDHCP = false;
        firewall.enable = lib.mkForce false;
      };

      environment.systemPackages = [
        pkgs.jq
        pkgs.iproute2
      ];

      # Capture every event type in one file.
      environment.etc = {
        "wanwatch/hooks/up.d/capture".source = captureHook;
        "wanwatch/hooks/down.d/capture".source = captureHook;
        "wanwatch/hooks/switch.d/capture".source = captureHook;
      };

      services.wanwatch = {
        enable = true;
        wans = {
          primary = {
            interface = "wan0";
            pointToPoint = true;
            probe = {
              targets = {
                v4 = [ "192.0.2.1" ];
                v6 = [ "2001:db8::1" ];
              };
              intervalMs = 600000;
              timeoutMs = 30000;
              hysteresis = {
                consecutiveDown = 10;
                consecutiveUp = 10;
              };
            };
          };
          backup = {
            interface = "wan1";
            pointToPoint = true;
            probe = {
              targets = {
                v4 = [ "100.64.0.1" ];
                v6 = [ "2001:db8:1::1" ];
              };
              intervalMs = 600000;
              timeoutMs = 30000;
              hysteresis = {
                consecutiveDown = 10;
                consecutiveUp = 10;
              };
            };
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

  testScript = (builtins.readFile ./observation.py) + ''
    observe = Observation(router, curl="${pkgs.curl}/bin/curl")

    router.wait_for_unit("wanwatch.service")
    router.wait_for_unit("systemd-networkd.service")

    router.succeed("ip link set wan0 up")
    router.succeed("ip link set wan1 up")
    observe.wait_active("home-uplink", "primary")

    # Discard the capture from the initial up Decision so the next
    # read sees only the switch.
    router.succeed("rm -f /run/wanwatch/last-hook.env")

    if router.execute("ip link set wan0 carrier off")[0] != 0:
        router.succeed("ip link set wan0 down")
    observe.wait_active("home-uplink", "backup")
    router.wait_for_file("/run/wanwatch/last-hook.env")

    captured = router.succeed("cat /run/wanwatch/last-hook.env")
    print("captured hook env:\n" + captured)

    hook_env = {}
    for line in captured.splitlines():
        if "=" in line:
            key, _, value = line.partition("=")
            hook_env[key] = value

    expectations = {
        "WANWATCH_EVENT": "switch",
        "WANWATCH_GROUP": "home-uplink",
        "WANWATCH_WAN_OLD": "primary",
        "WANWATCH_WAN_NEW": "backup",
        "WANWATCH_IFACE_OLD": "wan0",
        "WANWATCH_IFACE_NEW": "wan1",
    }
    for key, want in expectations.items():
        assert hook_env.get(key) == want, (
            f"{key} = {hook_env.get(key)!r}, want {want!r}\n"
            f"full env:\n{captured}"
        )

    # Gateway variables are emitted but empty for pointToPoint WANs.
    gateway_keys = [
        "WANWATCH_GATEWAY_V4_OLD",
        "WANWATCH_GATEWAY_V4_NEW",
        "WANWATCH_GATEWAY_V6_OLD",
        "WANWATCH_GATEWAY_V6_NEW",
    ]
    for key in gateway_keys:
        assert key in hook_env, f"{key} not emitted; full env:\n{captured}"
        assert hook_env[key] == "", (
            f"{key} = {hook_env[key]!r}, want empty (pointToPoint)"
        )

    # WANWATCH_FAMILIES is comma-joined in no particular order.
    families = set(hook_env.get("WANWATCH_FAMILIES", "").split(","))
    assert families == {"v4", "v6"}, (
        f"WANWATCH_FAMILIES = {families}, want {{'v4','v6'}}"
    )

    # _TABLE and _MARK are integers; _TS is Go's RFC3339Nano.
    for key in ("WANWATCH_TABLE", "WANWATCH_MARK"):
        assert hook_env[key].isdigit(), f"{key} = {hook_env[key]!r}"
    timestamp = hook_env["WANWATCH_TS"]
    assert "T" in timestamp and "Z" in timestamp, (
        f"WANWATCH_TS = {timestamp!r} doesn't look like RFC3339"
    )
  '';
}
