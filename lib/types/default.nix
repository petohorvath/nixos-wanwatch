# Flattens the per-concept option types into `wanwatch.types`. WAN types
# embed the probe types and group types embed the member types.
{
  lib,
  libnet,
  internal,
}:
let
  primitives = import ./primitives.nix { inherit lib; };
  probe = import ./probe.nix {
    inherit
      internal
      lib
      libnet
      primitives
      ;
  };
  member = import ./member.nix { inherit internal lib primitives; };
in
lib.mergeAttrsList [
  primitives
  probe
  member
  (import ./wan.nix {
    inherit lib libnet primitives;
    probeTypes = probe;
  })
  (import ./group.nix {
    inherit internal lib primitives;
    memberTypes = member;
  })
]
