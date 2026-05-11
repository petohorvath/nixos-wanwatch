/*
  types/wan — NixOS option types for the WAN value.

  Exports (flattened into `wanwatch.types.<name>` by
  `lib/types/default.nix`):

    wanName       — wanwatch identifier; in `wans.<name>` attrsets,
                    derived read-only from the attribute key.
    wanInterface  — `libnet.types.interfaceName` (kernel-`dev_valid_name`
                    parity)
    wanGateways   — submodule { v4 = nullOr libnet.types.ipv4;
                                v6 = nullOr libnet.types.ipv6; }
    wan           — top-level submodule composing probe

  The "at least one gateway" and family-coupling invariants from
  PLAN §5.4 are cross-field — they can't be expressed at this level
  and are enforced by `internal.wan.tryMake` at value-construction
  time.

  Takes `probeTypes` (the result of `import ./probe.nix { … }`) so
  the wan submodule can embed `probeTypes.probe` for the nested
  `probe` field. Avoids re-importing probe.nix internally.
*/
{
  lib,
  libnet,
  primitives,
  internal,
  probeTypes,
}:
let
  inherit (lib) types mkOption;

  wanName = primitives.identifier;
  wanInterface = libnet.types.interfaceName;

  wanGateways = types.submodule {
    options = {
      v4 = mkOption {
        type = types.nullOr libnet.types.ipv4;
        default = null;
        example = "192.0.2.1";
        description = ''
          Optional IPv4 gateway. Null means no v4 default route is
          managed for this WAN.
        '';
      };
      v6 = mkOption {
        type = types.nullOr libnet.types.ipv6;
        default = null;
        example = "2001:db8::1";
        description = ''
          Optional IPv6 gateway. Null means no v6 default route is
          managed for this WAN.
        '';
      };
    };
  };

  wan = types.submodule (
    { name, ... }:
    {
      options = {
        name = mkOption {
          type = wanName;
          readOnly = true;
          default = name;
          description = ''
            WAN identifier. Defaults to the attribute key — e.g.
            `services.wanwatch.wans.primary.name` is `"primary"`.
          '';
        };
        interface = mkOption {
          type = wanInterface;
          example = "eth0";
          description = ''
            Linux interface name. Validated against the kernel's
            `dev_valid_name` rules (length < 16, no `/`, `:`, or
            whitespace).
          '';
        };
        gateways = mkOption {
          type = wanGateways;
          default = { };
          description = ''
            Per-family gateways. At least one of `v4` / `v6` must
            be non-null — enforced by `internal.wan.tryMake` at
            value-construction time, not at the type level.
          '';
        };
        probe = mkOption {
          type = probeTypes.probe;
          description = ''
            Probe configuration for this WAN. Family-coupling
            (every declared gateway family has at least one matching
            target) is enforced by `internal.wan.tryMake`.
          '';
        };
      };
    }
  );
in
{
  inherit
    wanName
    wanInterface
    wanGateways
    wan
    ;
}
