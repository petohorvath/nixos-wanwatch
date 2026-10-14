/*
  Daemon-config renderer, exposed as `wanwatch.config`. Combines global
  settings with WAN and Group values into the JSON document the daemon
  reads at startup (`docs/specs/daemon-config.md`):

    {
      "schema": 1,
      "global": <global.toJSONValue>,
      "wans":   { "<name>": <wan.toJSONValue>, ... },
      "groups": { "<name>": <group.toJSONValue>, ... }
    }

  Rendering also validates the global settings with `global.make` and
  rejects Groups that share a mark or a table, because their traffic
  would be routed through the wrong table.
*/
{
  lib,
  internal,
}:
let
  # `render` takes an argument named `global`, so the global value type
  # stays qualified as `internal.global`.
  inherit (internal) group wan;

  # Bumped on every incompatible change to the daemon-config shape; the
  # daemon rejects configs with another version.
  schemaVersion = 1;

  # Returns one message per `field` value that several groups share.
  describeSharedValues =
    field: groups:
    lib.pipe groups [
      builtins.attrNames
      (lib.groupBy (name: toString groups.${name}.${field}))
      (lib.filterAttrs (_: names: builtins.length names > 1))
      (lib.mapAttrsToList (
        value: names:
        "${field} ${value} is shared by groups [${lib.concatMapStringsSep ", " (name: "'${name}'") names}]"
      ))
    ];

  /*
    Reject Groups that share a mark or a table, because their traffic
    would be routed through the wrong table.

    `groups`: an attrset of group values keyed by name.

    Returns `groups` unchanged. Throws a message listing every shared
    mark and table.
  */
  assertUniqueMarksAndTables =
    groups:
    let
      messages = describeSharedValues "mark" groups ++ describeSharedValues "table" groups;
    in
    if messages == [ ] then
      groups
    else
      throw "wanwatch: duplicate mark or table across groups: ${lib.concatStringsSep "; " messages}";

  /*
    Render the daemon configuration.

    `global`: an input for `global.make`, such as a global value;
    default `{ }`. Unset fields take `global.defaults`.
    `wans`: an attrset of WAN values from `wan.make`; default `{ }`.
    `groups`: an attrset of group values from `group.make`; default
    `{ }`.

    Returns the JSON-shaped attrset. Throws the `global.make` error when
    the global settings are invalid, and throws when Groups share a mark
    or a table.
  */
  render =
    {
      global ? { },
      wans ? { },
      groups ? { },
    }:
    {
      schema = schemaVersion;
      global = internal.global.toJSONValue (internal.global.make global);
      wans = builtins.mapAttrs (_: wan.toJSONValue) wans;
      groups = builtins.mapAttrs (_: group.toJSONValue) (assertUniqueMarksAndTables groups);
    };

  /*
    Render the daemon configuration as a JSON string.

    `config`: the attrset accepted by `render`.

    Returns `builtins.toJSON (render config)`.
  */
  toJSON = config: builtins.toJSON (render config);
in
{
  inherit
    assertUniqueMarksAndTables
    render
    schemaVersion
    toJSON
    ;

  /*
    Deprecated name of `assertUniqueMarksAndTables`, from when marks and
    tables were allocated automatically. Warns when used.

    `groups`: an attrset of group values keyed by name.

    Returns `groups` unchanged. Throws when Groups share a mark or a
    table.
  */
  resolveAllocations = lib.warn "wanwatch: config.resolveAllocations is deprecated; use config.assertUniqueMarksAndTables" assertUniqueMarksAndTables;

  /*
    Alias of `render`, so the renderer exports the same `toJSONValue`
    name as the value types.

    `config`: the attrset accepted by `render`.

    Returns the JSON-shaped attrset `render` returns.
  */
  toJSONValue = render;
}
