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

  Rendering also validates the global settings with `global.make`,
  rejects Members that reference an undeclared WAN, which the daemon
  would refuse, and rejects Groups that share a mark or a table,
  because their traffic would be routed through the wrong table.
*/
{
  lib,
  internal,
}:
let
  # `render` takes an argument named `global`, so the global value type
  # stays qualified as `internal.global`.
  inherit (internal) group wan;

  # Pinned at 1 until the first tagged release, then bumped on every
  # incompatible change to the daemon-config shape; the daemon rejects
  # configs with another version.
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

  # Returns one message per Member whose `wan` is not a key of `wans`.
  describeUndeclaredMemberWans =
    wans: groups:
    lib.concatLists (
      lib.mapAttrsToList (
        name: group:
        lib.pipe group.members [
          builtins.attrNames
          (builtins.filter (wan: !(wans ? ${wan})))
          (map (wan: "group '${name}' references wan '${wan}'"))
        ]
      ) groups
    );

  /*
    Reject Members that reference a WAN `wans` does not declare. The
    daemon refuses such a config at startup, so a typo would otherwise
    surface as a failed unit instead of an evaluation error.

    `wans`: an attrset of WAN values keyed by name.
    `groups`: an attrset of group values keyed by name.

    Returns `groups` unchanged. Throws a message listing every
    undeclared reference.
  */
  assertDeclaredMemberWans =
    wans: groups:
    let
      messages = describeUndeclaredMemberWans wans groups;
    in
    if messages == [ ] then
      groups
    else
      throw "wanwatch: group members reference undeclared WANs: ${lib.concatStringsSep "; " messages}";

  /*
    Render the daemon configuration.

    `global`: an input for `global.make`, such as a global value;
    default `{ }`. Unset fields take `global.defaults`.
    `wans`: an attrset of WAN values from `wan.make`; default `{ }`.
    `groups`: an attrset of group values from `group.make`; default
    `{ }`.

    Returns the JSON-shaped attrset. Throws the `global.make` error when
    the global settings are invalid, and throws when a Member references
    a WAN that `wans` does not declare or when Groups share a mark or a
    table.
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
      groups = lib.pipe groups [
        (assertDeclaredMemberWans wans)
        assertUniqueMarksAndTables
        (builtins.mapAttrs (_: group.toJSONValue))
      ];
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
}
