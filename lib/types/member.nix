/*
  types/member — NixOS option types for the Member value.

  Exports (flattened into `wanwatch.types.<name>` by
  `lib/types/default.nix`):

    memberWan       — wanwatch identifier (must match the `name`
                      of a WAN declared in the surrounding config —
                      cross-check happens at config-eval time, not
                      at the type level)
    memberWeight    — positive int (default 100)
    memberPriority  — positive int (default 1; lower = preferred
                      by the primary-backup strategy)
    member          — top-level submodule
*/
{
  lib,
  libnet,
  primitives,
  internal,
}:
let
  inherit (lib) types mkOption;
  inherit (internal.member) defaults;

  memberWan = primitives.identifier;
  memberWeight = primitives.positiveInt;
  memberPriority = primitives.positiveInt;

  member = types.submodule {
    options = {
      wan = mkOption {
        type = memberWan;
        example = "primary";
        description = ''
          Name of the WAN this Member references. Must match the
          `name` of a WAN declared in the same `services.wanwatch`
          config; the cross-check happens at config-eval time.
        '';
      };
      weight = mkOption {
        type = memberWeight;
        default = defaults.weight;
        description = ''
          Tiebreaker among Members with equal priority. v1's
          primary-backup strategy ignores weight; it matters once
          multi-active (load-balance) lands in v2.
        '';
      };
      priority = mkOption {
        type = memberPriority;
        default = defaults.priority;
        description = ''
          Lower = preferred by the primary-backup strategy. Ties
          broken lexicographically by `wan` for determinism.
        '';
      };
    };
  };
in
{
  inherit
    memberWan
    memberWeight
    memberPriority
    member
    ;
}
