/*
  Option types for Group Members, exported through `wanwatch.types`:

    memberWan      — wanwatch identifier of a declared WAN; the daemon
                     checks the reference when it loads its config
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
    ;
}
