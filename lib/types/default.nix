/*
  types — NixOS option types for module consumers. Exposed under
  `wanwatch.types`.

  Each value-type concept gets its own file (matching `internal/`):
    primitives.nix — shared option-type primitives (identifier, …)
    probe.nix      — probe-related option types
    member.nix     — member-related option types
    wan.nix        — wan-related option types
    group.nix      — group-related option types

  Aggregates them flat via `lib.mergeAttrsList` — same pattern as
  `nix-nftzones/lib/types/default.nix`. Consumers reach
  `wanwatch.types.<name>` regardless of which file the type was
  declared in.

  Cross-file references (wan embeds probe; group embeds member)
  are threaded through the per-file imports — see the
  `probeTypes` / `memberTypes` args below.
*/
{
  lib,
  libnet,
  internal,
}:
let
  primitives = import ./primitives.nix { inherit lib; };
  probe = import ./probe.nix {
    inherit
      lib
      libnet
      primitives
      internal
      ;
  };
  member = import ./member.nix {
    inherit
      lib
      primitives
      internal
      ;
  };
  wan = import ./wan.nix {
    inherit
      lib
      libnet
      primitives
      ;
    probeTypes = probe;
  };
  group = import ./group.nix {
    inherit
      lib
      primitives
      internal
      ;
    memberTypes = member;
  };
in
lib.mergeAttrsList [
  primitives
  probe
  member
  wan
  group
]
