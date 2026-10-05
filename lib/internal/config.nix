/*
  Daemon-config renderer, exposed as `wanwatch.config`. Combines WAN
  and Group values with global settings into the JSON document the
  daemon reads at startup (`docs/specs/daemon-config.md`):

    {
      "schema": 1,
      "global": { statePath, hooksDir, metricsSocket, logLevel,
                  hookTimeoutMs },
      "wans":   { "<name>": <wan.toJSONValue>, ... },
      "groups": { "<name>": <group.toJSONValue>, ... }
    }

  Rendering also rejects Groups that share a mark or a table, because
  their traffic would be routed through the wrong table.
*/
{
  lib,
  internal,
}:
let
  inherit (internal) group wan;

  # Bumped on every incompatible change to the daemon-config shape; the
  # daemon rejects configs with another version.
  schemaVersion = 1;

  # Also the module's option defaults; `render` merges caller settings
  # over them.
  defaultGlobal = {
    statePath = "/run/wanwatch/state.json";
    hooksDir = "/etc/wanwatch/hooks";
    metricsSocket = "/run/wanwatch/metrics.sock";
    logLevel = "info";
    hookTimeoutMs = 5000;
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
    Check that no two Groups share a mark or a table. The name predates
    the removal of automatic allocation and is kept for compatibility;
    the function only validates.

    `groups`: an attrset of group values keyed by name.

    Returns `groups` unchanged. Throws a message listing every shared
    mark and table.
  */
  resolveAllocations =
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

    `global`: settings merged over `defaultGlobal`; default `{ }`.
    `wans`: an attrset of WAN values from `wan.make`; default `{ }`.
    `groups`: an attrset of group values from `group.make`; default
    `{ }`.

    Returns the JSON-shaped attrset. Throws when Groups share a mark or
    a table.
  */
  render =
    {
      global ? { },
      wans ? { },
      groups ? { },
    }:
    {
      schema = schemaVersion;
      global = defaultGlobal // global;
      wans = builtins.mapAttrs (_: wan.toJSONValue) wans;
      groups = builtins.mapAttrs (_: group.toJSONValue) (resolveAllocations groups);
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
    defaultGlobal
    render
    resolveAllocations
    schemaVersion
    toJSON
    ;

  /*
    Alias of `render`, so the renderer exports the same `toJSONValue`
    name as the value types.

    `config`: the attrset accepted by `render`.

    Returns the JSON-shaped attrset `render` returns.
  */
  toJSONValue = render;
}
