/*
  types/primitives — shared option-type primitives used by the
  per-concept type modules (probe, member, wan, group).

  Each primitive here pairs a `lib.types.*` definition with a
  human-readable `description` so NixOS error messages and
  generated docs read naturally. Matches nftzones' primitives
  pattern (`nix-nftzones/lib/types/primitives.nix`).

  Exported under `wanwatch.types.<name>`:

    identifier   — `[a-zA-Z][a-zA-Z0-9-]*`. Used for wan / group /
                   member.wan references. Same regex as
                   `internal.primitives.isValidName`.
    positiveInt  — integer ≥ 1. Used for weight, priority, table,
                   mark, intervalMs, timeoutMs, windowSize, RTT
                   thresholds, hysteresis counters.
    pctInt       — integer in [0, 100]. Used for loss thresholds.
*/
{ lib, libnet }:
let
  inherit (lib) types;
in
{
  # `strMatching` carries a `name = "strMatchingRegex"`-style
  # description, which is unfriendly in error messages. Override
  # to a human-readable form. The `// { ... }` pattern is the same
  # one nftzones uses; the type's functor stays intact.
  identifier = types.strMatching "[a-zA-Z][a-zA-Z0-9-]*" // {
    description = "wanwatch identifier (matching [a-zA-Z][a-zA-Z0-9-]*)";
  };

  # `ints.positive` and `ints.between` already carry good descriptions
  # from nixpkgs; no override needed (and overriding via `//` triggers
  # functor-recursion edge cases for compound types).
  positiveInt = types.ints.positive;
  pctInt = types.ints.between 0 100;
}
