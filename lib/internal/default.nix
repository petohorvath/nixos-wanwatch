# Composes the value types and renderers that `lib/default.nix` exports
# and `lib/types` builds on. Each module receives only the lower-level
# modules it uses.
{ lib, libnet }:
let
  primitives = import ./primitives.nix { inherit lib; };
  probe = import ./probe.nix {
    inherit lib libnet;
    internal = { inherit primitives; };
  };
  member = import ./member.nix {
    internal = { inherit primitives; };
  };
  wan = import ./wan.nix {
    inherit libnet;
    internal = { inherit primitives probe; };
  };
  group = import ./group.nix {
    inherit lib;
    internal = { inherit member primitives; };
  };
  global = import ./global.nix {
    internal = { inherit primitives; };
  };
in
{
  inherit
    global
    group
    member
    primitives
    probe
    wan
    ;
  selector = import ./selector.nix { inherit lib; };
  config = import ./config.nix {
    inherit lib;
    internal = { inherit global group wan; };
  };
}
