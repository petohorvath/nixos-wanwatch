/*
  types/wan — NixOS option types for the WAN value.

  Exports (flattened into `wanwatch.types.<name>` by
  `lib/types/default.nix`):

    wanName       — wanwatch identifier; in `wans.<name>` attrsets,
                    derived read-only from the attribute key.
    wanInterface  — `libnet.types.interfaceName` (kernel-`dev_valid_name`
                    parity)
    wan           — top-level submodule composing probe

  The WAN serves whichever families its `probe.targets` cover —
  there is no separate gateway / family declaration. The daemon
  discovers the gateway at runtime from the kernel's main routing
  table (`pointToPoint = false`), or installs a scope-link default
  route (`pointToPoint = true`) for PPP / WireGuard / tun-style
  interfaces with no broadcast next-hop.

  Takes `probeTypes` (the result of `import ./probe.nix { … }`) so
  the wan submodule can embed `probeTypes.probe` for the nested
  `probe` field.
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
        pointToPoint = mkOption {
          type = types.bool;
          default = false;
          example = true;
          description = ''
            When true the daemon installs scope-link default routes
            for this WAN — appropriate for PPP, WireGuard, GRE,
            tun, and any other link with no broadcast next-hop.
            When false (default) the daemon discovers the
            interface's current default-route gateway via netlink
            from the kernel's main routing table.
          '';
        };
        probe = mkOption {
          type = probeTypes.probe;
          description = ''
            Probe configuration for this WAN. The families the
            WAN handles are derived from `probe.targets`: a v4
            literal means v4 is served; a v6 literal means v6 is
            served.
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
    wan
    ;
}
