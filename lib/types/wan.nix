/*
  Option types for WANs, exported through `wanwatch.types`:

    wanName      — wanwatch identifier; in `wans.<name>` it is the
                   read-only attribute key
    wanInterface — Linux interface name, checked against the kernel's
                   `dev_valid_name` rules
    wan          — the complete WAN submodule, embedding
                   `probeTypes.probe`
*/
{
  lib,
  libnet,
  primitives,
  probeTypes,
}:
let
  inherit (lib) mkOption types;

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
            WAN identifier, taken from the attribute key:
            `services.wanwatch.wans.primary.name` is `"primary"`.
          '';
        };
        interface = mkOption {
          type = wanInterface;
          example = "eth0";
          description = ''
            Linux interface name, checked against the kernel's
            `dev_valid_name` rules (shorter than 16 characters, with no
            `/`, `:`, or whitespace).
          '';
        };
        pointToPoint = mkOption {
          type = types.bool;
          default = false;
          example = true;
          description = ''
            Whether the daemon installs scope-link default routes for
            this WAN, as needed by PPP, WireGuard, GRE, tun, and other
            links without a broadcast next hop. When false, the daemon
            uses the interface's default-route gateway from the main
            routing table, discovered through netlink.
          '';
        };
        probe = mkOption {
          type = probeTypes.probe;
          description = ''
            Probe configuration for this WAN. The WAN serves the address
            families with non-empty `probe.targets`.
          '';
        };
      };
    }
  );
in
{
  inherit
    wan
    wanInterface
    wanName
    ;
}
