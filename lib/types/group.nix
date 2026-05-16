/*
  types/group — NixOS option types for the Group value.

  Exports (flattened into `wanwatch.types.<name>` by
  `lib/types/default.nix`):

    groupName      — wanwatch identifier; in `groups.<name>`
                     attrsets, derived read-only from the attribute
                     key.
    groupStrategy  — enum [ "primary-backup" ] (v1)
    groupTable     — `primitives.routingTableId` (1000..32767),
                     required per group.
    groupMark      — `primitives.fwmark` (1000..32767), required
                     per group.
    group          — top-level submodule (composes member)

  Cross-field invariants — non-empty members, no duplicate WAN
  references — live in `internal.group.tryMake`. The type system
  doesn't reach across fields, and forwarding the responsibility
  keeps the validators consolidated.

  Duplicate-mark and duplicate-table detection across groups lives
  in `internal.config.resolveAllocations`, not in this submodule
  (the type system can't see across attrset siblings).

  Takes `memberTypes` so the group submodule can use
  `memberTypes.member` for the elements of its `members` list.
*/
{
  lib,
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

  groupTable = primitives.routingTableId;
  groupMark = primitives.fwmark;

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
          example = 1000;
          description = ''
            Routing-table id for this group's policy-routed
            traffic. Required integer in `[1000, 32767]`. Shared
            across v4 and v6 RIBs (PLAN §6.1). The module asserts
            no two groups share the same `table`.
          '';
        };
        mark = mkOption {
          type = groupMark;
          example = 1000;
          description = ''
            fwmark used to dispatch traffic to `table`. Required
            integer in `[1000, 32767]`. The module asserts no two
            groups share the same `mark`.
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
