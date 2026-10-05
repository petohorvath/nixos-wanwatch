/*
  Option types for Groups, exported through `wanwatch.types`:

    groupName     — wanwatch identifier; in `groups.<name>` it is the
                    read-only attribute key
    groupStrategy — enum of `internal.group.validStrategies`
    groupTable    — `primitives.routingTableId`
    groupMark     — `primitives.fwmark`
    group         — the complete Group submodule, with
                    `memberTypes.member` elements

  Option types cannot compare fields or sibling attributes, so
  `internal.group.tryMake` checks members and
  `internal.config.resolveAllocations` checks marks and tables across
  Groups.
*/
{
  lib,
  primitives,
  internal,
  memberTypes,
}:
let
  inherit (internal.group) defaults;
  inherit (lib) mkOption types;

  groupName = primitives.identifier;
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
            Group identifier, taken from the attribute key:
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
            Members of this Group. The list must be non-empty and must
            not reference a WAN twice.
          '';
        };
        strategy = mkOption {
          type = groupStrategy;
          default = defaults.strategy;
          description = ''
            Selection strategy. Only `"primary-backup"`, which picks the
            healthy Member with the lowest priority, is supported.
          '';
        };
        table = mkOption {
          type = groupTable;
          example = 1000;
          description = ''
            Routing-table ID for this Group's policy-routed traffic,
            shared by the IPv4 and IPv6 routing tables (PLAN §6.1). No
            two Groups may share a table.
          '';
        };
        mark = mkOption {
          type = groupMark;
          example = 1000;
          description = ''
            fwmark that dispatches traffic to `table`. No two Groups may
            share a mark.
          '';
        };
      };
    }
  );
in
{
  inherit
    group
    groupMark
    groupName
    groupStrategy
    groupTable
    ;
}
