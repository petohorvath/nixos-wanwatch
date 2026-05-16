/*
  types/primitives — shared option-type primitives used by the
  per-concept type modules (probe, member, wan, group).

  Each primitive here pairs a `lib.types.*` definition with a
  human-readable `description` so NixOS error messages and
  generated docs read naturally. Matches nftzones' primitives
  pattern (`nix-nftzones/lib/types/primitives.nix`).

  Exported under `wanwatch.types.<name>`:

    identifier      — `[a-zA-Z][a-zA-Z0-9-]*`. Used for wan / group /
                      member.wan references. Same regex as
                      `internal.primitives.isValidName`.
    positiveInt     — integer ≥ 1. Used for weight, priority,
                      intervalMs, timeoutMs, windowSize, RTT
                      thresholds, hysteresis counters.
    pctInt          — integer in [0, 100]. Used for loss thresholds.
    fwmark          — Linux netfilter fwmark; integer in
                      [1000, 32767]. Used for `groups.<name>.mark`.
    routingTableId  — Linux routing-table id; integer in
                      [1000, 32767]. Used for `groups.<name>.table`.

  TODO(v0.2): migrate `fwmark` and `routingTableId` into
  `nix-libnet/lib/types/primitives.nix` — they're kernel-level
  primitives, useful to nftzones and any other rule-/route-
  installing module. See TODO.md.
*/
{ lib }:
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

  # fwmark and routingTableId share the same range by construction:
  # `[1000, 32767]` is wide enough to comfortably name every group a
  # router would manage, and the lower bound buries the
  # kernel-reserved table ids `{253, 254, 255}` plus the small
  # integers ad-hoc operator scripts use, so neither type needs an
  # exclusion check. The two are still distinct option types so a
  # mistyped `mark = config.services.wanwatch.tables.<g>;` would
  # type-check, but conceptually-distinct integer kinds are clearer
  # named separately and easier to migrate to libnet later.
  fwmark = types.ints.between 1000 32767;
  routingTableId = types.ints.between 1000 32767;
}
