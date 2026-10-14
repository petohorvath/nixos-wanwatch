/*
  Option types for Group Members, exported through `wanwatch.types`:

    memberWan      — wanwatch identifier of a declared WAN; in
                     `members.<wan>` it is the read-only attribute key,
                     and the daemon checks the reference when it loads
                     its config
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

  member = types.submodule (
    { name, ... }:
    {
      options = {
        wan = mkOption {
          type = memberWan;
          readOnly = true;
          default = name;
          description = ''
            Name of the WAN this Member references, taken from the
            attribute key: `members.primary.wan` is `"primary"`. It
            must name a WAN declared in the same `services.wanwatch`
            configuration.
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
    }
  );
in
{
  inherit
    member
    memberPriority
    memberWan
    ;
}
