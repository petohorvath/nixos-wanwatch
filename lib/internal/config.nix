/*
  Daemon-config renderer, exposed as `wanwatch.config`. Combines global
  settings with WAN and Group values into the JSON document the daemon
  reads at startup (`docs/specs/daemon-config.md`):

    {
      "schema": 1,
      "global": <global.toJSONValue> // paths,
      "wans":   { "<name>": <wan.toJSONValue>, ... },
      "groups": { "<name>": <group.toJSONValue>, ... }
    }

  Rendering also validates the global settings with `global.make`,
  rejects WANs and Groups keyed by another name and Members that
  reference an undeclared WAN, which the daemon would refuse, and
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

  # Bumped, in lockstep with the daemon's SupportedSchema, on every
  # incompatible change to the daemon-config shape
  # (docs/specs/daemon-config.md); the daemon rejects configs with
  # another version.
  schemaVersion = 1;

  # The daemon's paths, which are fixed rather than settings: the NixOS
  # module's sandbox lets the daemon write only under /run/wanwatch
  # (docs/adr/0014-module-owns-identity-and-paths.md).
  paths = {
    statePath = "/run/wanwatch/state.json";
    hooksDir = "/etc/wanwatch/hooks";
    metricsSocket = "/run/wanwatch/metrics.sock";
  };

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
    Reject values keyed by another name. The daemon requires each key
    of `wans` and `groups` to match its value's `name` and refuses the
    file otherwise, so a mismatch would surface as a failed unit
    instead of an evaluation error.

    `field`: the config field holding `values`, such as `"wans"`.
    `values`: an attrset of values with a `name`.

    Returns `values` unchanged. Throws a message listing every value
    whose key differs from its name.
  */
  assertKeyedByName =
    field: values:
    let
      messages = lib.pipe values [
        (lib.filterAttrs (key: value: value.name != key))
        (lib.mapAttrsToList (key: value: "${field}.${key} has name '${value.name}'"))
      ];
    in
    if messages == [ ] then
      values
    else
      throw "wanwatch: ${field} keyed by another name: ${lib.concatStringsSep "; " messages}";

  # Returns one message per Member whose `wan` is not a key of `wans`.
  describeUndeclaredMemberWans =
    wans: groups:
    lib.concatLists (
      lib.mapAttrsToList (
        groupName: groupValue:
        lib.pipe groupValue.members [
          builtins.attrNames
          (builtins.filter (wanName: !(wans ? ${wanName})))
          (map (wanName: "group '${groupName}' references wan '${wanName}'"))
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

    Returns the JSON-shaped attrset, whose `global` also carries
    `paths`. Throws the `global.make` error when the global settings
    are invalid, and throws when a WAN or Group key differs from its
    `name`, when a Member references a WAN that `wans` does not
    declare, or when Groups share a mark or a table.
  */
  render =
    {
      global ? { },
      wans ? { },
      groups ? { },
    }:
    {
      schema = schemaVersion;
      global = internal.global.toJSONValue (internal.global.make global) // paths;
      wans = lib.pipe wans [
        (assertKeyedByName "wans")
        (builtins.mapAttrs (_: wan.toJSONValue))
      ];
      groups = lib.pipe groups [
        (assertKeyedByName "groups")
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
    paths
    render
    schemaVersion
    toJSON
    ;
}
