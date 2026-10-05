/*
  Option types for Group Members, exported through `wanwatch.types`:

    memberWan      — wanwatch identifier of a declared WAN; the module
                     checks the reference, not the type
    memberWeight   — positive integer, default 100
    memberPriority — positive integer, default 1; lower is preferred
    member         — the complete Member submodule
*/
{
  lib,
  primitives,
  internal,
}:
let
  inherit (internal.member) defaults;
  inherit (lib) mkOption types;

  memberWan = primitives.identifier;
  memberWeight = primitives.positiveInt;
  memberPriority = primitives.positiveInt;

  member = types.submodule {
    options = {
      wan = mkOption {
        type = memberWan;
        example = "primary";
        description = ''
          Name of the WAN this Member references. It must name a WAN
          declared in the same `services.wanwatch` configuration.
        '';
      };
      weight = mkOption {
        type = memberWeight;
        default = defaults.weight;
        description = ''
          Tiebreaker among Members with equal priority. The
          primary-backup strategy ignores it; it matters once
          multi-active strategies exist.
        '';
      };
      priority = mkOption {
        type = memberPriority;
        default = defaults.priority;
        description = ''
          Preference order for the primary-backup strategy; lower is
          preferred. Ties go to the lexicographically smaller `wan`.
        '';
      };
    };
  };
in
{
  inherit
    member
    memberPriority
    memberWan
    memberWeight
    ;
}
