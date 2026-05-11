/*
  types — NixOS option types for module consumers. Exposed under
  `wanwatch.types`.

  Each value-type concept gets its own file (matching `internal/`):
    primitives.nix — shared option-type primitives (identifier, …)
    probe.nix      — probe-related option types
    wan.nix        — wan-related option types

  This file aggregates them flat via `lib.mergeAttrsList` — same
  pattern as `nix-nftzones/lib/types/default.nix`. Consumers reach
  `wanwatch.types.<name>` regardless of which file the type
  was declared in.

  Pass 1 boundary: every per-type file is an empty stub. Real
  types land in Pass 5 (PLAN.md §10), at which point each file
  exports the option types for its concept.
*/
{
  lib,
  libnet,
  internal,
}:
let
  primitives = import ./primitives.nix { inherit lib libnet; };
  probe = import ./probe.nix {
    inherit
      lib
      libnet
      primitives
      internal
      ;
  };
  wan = import ./wan.nix {
    inherit
      lib
      libnet
      primitives
      internal
      ;
  };
in
lib.mergeAttrsList [
  primitives
  probe
  wan
]
