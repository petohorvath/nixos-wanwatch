/*
  types/group — NixOS option types for the Group value.

  Exports (flattened into `wanwatch.types.<name>` by
  `lib/types/default.nix`):

    groupName      — wanwatch identifier; in `groups.<name>`
                     attrsets, derived read-only from the attribute
                     key.
    groupStrategy  — enum [ "primary-backup" ] (v1)
    groupTable     — `nullOr primitives.positiveInt` (null =
                     auto-allocated by `internal.tables.allocate`)
    groupMark      — `nullOr primitives.positiveInt` (null =
                     auto-allocated by `internal.marks.allocate`)
    group          — top-level submodule (composes member)

  Cross-field invariants — non-empty members, no duplicate WAN
  references — live in `internal.group.tryMake`. The type system
  doesn't reach across fields, and forwarding the responsibility
  keeps the validators consolidated.

  Takes `memberTypes` so the group submodule can use
  `memberTypes.member` for the elements of its `members` list.
*/
{
  lib,
  libnet,
  primitives,
  internal,
  memberTypes,
}:
let
  inherit (lib) types mkOption;
  inherit (internal.group) defaults;

  groupName = primitives.identifier;

  # Single source of truth: the enum derives from
  # `internal.group.validStrategies` so the option type and the
  # validator stay aligned.
  groupStrategy = types.enum internal.group.validStrategies;

  groupTable = types.nullOr primitives.positiveInt;
  groupMark = types.nullOr primitives.positiveInt;

  group = types.submodule (
    { name, ... }:
    {
      options = {
        name = mkOption {
          type = groupName;
          readOnly = true;
          default = name;
          description = ''
            Group identifier. Defaults to the attribute key —
            `services.wanwatch.groups.home-uplink.name` is
            `"home-uplink"`.
          '';
        };
        members = mkOption {
          type = types.listOf memberTypes.member;
          example = lib.literalExpression ''
            [
              { wan = "primary"; priority = 1; }
              { wan = "backup";  priority = 2; }
            ]
          '';
          description = ''
            Ordered list of Members participating in this group.
            Must be non-empty and contain no duplicate WAN
            references — enforced by `internal.group.tryMake`.
          '';
        };
        strategy = mkOption {
          type = groupStrategy;
          default = defaults.strategy;
          description = ''
            Selection strategy. v1 supports `"primary-backup"`
            only — picks the lowest-priority healthy Member.
          '';
        };
        table = mkOption {
          type = groupTable;
          default = defaults.table;
          example = 100;
          description = ''
            Routing-table id for this group's policy-routed
            traffic. Null = auto-allocated by
            `internal.tables.allocate` (deterministic hash over
            the group-name set, in range [100, 32766] minus
            253/254/255).
          '';
        };
        mark = mkOption {
          type = groupMark;
          default = defaults.mark;
          example = 100;
          description = ''
            fwmark used to dispatch traffic to `table`. Null =
            auto-allocated by `internal.marks.allocate`
            (deterministic hash over the group-name set, in range
            [100, 32767]).
          '';
        };
      };
    }
  );
in
{
  inherit
    groupName
    groupStrategy
    groupTable
    groupMark
    group
    ;
}
