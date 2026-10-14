/*
  The wanwatch library: validated value types, NixOS option types, the
  selector mirror, and the daemon-config renderer.

    wanwatch = import ./lib {
      inherit (nixpkgs) lib;
      libnet = libnet.lib.withLib nixpkgs.lib;
    };
    wanwatch.wan.make {
      name = "primary";
      interface = "eth0";
      probe.targets.v4 = [ "1.1.1.1" ];
    }

  `libnet` is nix-libnet with its option types (`withLib`). The value
  types, the selector, and the renderer are exported at the top level
  (`wanwatch.wan`), and `wanwatch.types` holds the flattened option
  types. The shared helpers in `internal/primitives.nix` stay private.
*/
{ lib, libnet }:
let
  internal = import ./internal { inherit lib libnet; };
in
{
  inherit (internal)
    config
    global
    group
    member
    probe
    selector
    wan
    ;
  types = import ./types { inherit internal lib libnet; };
  version = "0.1.0";
}
