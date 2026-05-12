/*
  hooks — wire a captured-env-vars hook into /etc/wanwatch/hooks/
  switch.d/, trigger a carrier-driven Decision, and verify the
  hook saw the PLAN §5.5 env vars (WANWATCH_EVENT, _GROUP,
  _WAN_OLD/_NEW, _IFACE_OLD/_NEW, _GATEWAY_V4_OLD/_NEW,
  _GATEWAY_V6_OLD/_NEW, _FAMILIES, _TABLE, _MARK, _TS).
*/
{
  pkgs,
  nixosModule,
}:

let
  # The hook writes every WANWATCH_* env var to a known file.
  # Hooks run as the wanwatch user — keep the capture path under
  # the daemon's hooksDir so the daemon's writable view of /etc is
  # not needed.
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
      networking.useNetworkd = true;
      networking.useDHCP = false;
      networking.firewall.enable = lib.mkForce false;

      environment.systemPackages = [
        pkgs.jq
        pkgs.iproute2
      ];

      # Install the capture hook into every event directory so the
      # test can read /run/wanwatch/last-hook.env regardless of
      # whether the trigger is up/down/switch.
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
            gateways = {
              v4 = "192.0.2.1";
              v6 = "2001:db8::1";
            };
            probe = {
              targets = [
                "192.0.2.1"
                "2001:db8::1"
              ];
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
            gateways = {
              v4 = "100.64.0.1";
              v6 = "2001:db8:1::1";
            };
            probe = {
              targets = [
                "100.64.0.1"
                "2001:db8:1::1"
              ];
              intervalMs = 600000;
              timeoutMs = 30000;
              hysteresis = {
                consecutiveDown = 10;
                consecutiveUp = 10;
              };
            };
          };
        };
        groups.home-uplink.members = [
          {
            wan = "primary";
            priority = 1;
          }
          {
            wan = "backup";
            priority = 2;
          }
        ];
      };
    };

  testScript = ''
    import json


    def wait_for_active(router, want, timeout=15):
        for _ in range(timeout * 4):
            out = router.succeed("cat /run/wanwatch/state.json")
            active = json.loads(out)["groups"]["home-uplink"]["active"]
            if active == want:
                return
            router.execute("sleep 0.25")
        raise AssertionError(
            f"active never reached {want!r}; last state =\n{out}"
        )


    router.wait_for_unit("wanwatch.service")
    router.wait_for_unit("systemd-networkd.service")

    router.succeed("ip link set wan0 up")
    router.succeed("ip link set wan1 up")
    wait_for_active(router, "primary")

    # The initial up Decision should have fired the up.d hook —
    # remove the captured file so the next assertion only sees
    # the switch event.
    router.succeed("rm -f /run/wanwatch/last-hook.env")

    if router.execute("ip link set wan0 carrier off")[0] != 0:
        router.succeed("ip link set wan0 down")
    wait_for_active(router, "backup")
    router.wait_for_file("/run/wanwatch/last-hook.env")

    captured = router.succeed("cat /run/wanwatch/last-hook.env")
    print("captured hook env:\n" + captured)

    env_dict = {}
    for line in captured.splitlines():
        if "=" in line:
            k, _, v = line.partition("=")
            env_dict[k] = v

    expectations = {
        "WANWATCH_EVENT": "switch",
        "WANWATCH_GROUP": "home-uplink",
        "WANWATCH_WAN_OLD": "primary",
        "WANWATCH_WAN_NEW": "backup",
        "WANWATCH_IFACE_OLD": "wan0",
        "WANWATCH_IFACE_NEW": "wan1",
        "WANWATCH_GATEWAY_V4_OLD": "192.0.2.1",
        "WANWATCH_GATEWAY_V4_NEW": "100.64.0.1",
        "WANWATCH_GATEWAY_V6_OLD": "2001:db8::1",
        "WANWATCH_GATEWAY_V6_NEW": "2001:db8:1::1",
    }
    for key, want in expectations.items():
        assert env_dict.get(key) == want, (
            f"{key} = {env_dict.get(key)!r}, want {want!r}\n"
            f"full env:\n{captured}"
        )

    # WANWATCH_FAMILIES is set-valued (comma-joined), not pinned
    # to a particular order — assert membership instead.
    fams = set(env_dict.get("WANWATCH_FAMILIES", "").split(","))
    assert fams == {"v4", "v6"}, f"WANWATCH_FAMILIES = {fams}, want {{'v4','v6'}}"

    # _TABLE and _MARK must be non-empty integers; _TS must parse
    # as RFC3339Nano (Go time.Format(time.RFC3339Nano)).
    assert env_dict["WANWATCH_TABLE"].isdigit(), f"WANWATCH_TABLE = {env_dict['WANWATCH_TABLE']!r}"
    assert env_dict["WANWATCH_MARK"].isdigit(), f"WANWATCH_MARK = {env_dict['WANWATCH_MARK']!r}"
    assert "T" in env_dict["WANWATCH_TS"] and "Z" in env_dict["WANWATCH_TS"], (
        f"WANWATCH_TS = {env_dict['WANWATCH_TS']!r} doesn't look like RFC3339"
    )
  '';
}
