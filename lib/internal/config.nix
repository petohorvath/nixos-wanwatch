/*
  internal/config — daemon-config JSON renderer.

  Composes wans + groups + global settings into the single JSON
  artifact the daemon reads at startup. Schema described in PLAN §5.5
  / `docs/specs/daemon-config.md` (Pass 6).

  Renders:

    {
      "schema": 1,
      "global": { statePath, hooksDir, metricsSocket, logLevel, hookTimeoutMs },
      "wans":   { "<name>": <wan.toJSONValue>, ... },
      "groups": { "<name>": <group.toJSONValue>, ... }
    }

  Marks + tables: each group's `mark` and `table` are user-required
  integers (validated by `internal.group.tryMake` and
  `wanwatch.types.{fwmark,routingTableId}`). This module's job is
  the cross-group duplicate check — two groups can't share a mark
  or a table, otherwise their traffic would mis-route. The check
  fires at module-eval time.

  ===== render =====

  `render { global ? {}; wans ? {}; groups ? {}; } → attrset`

  `wans` / `groups` are attrsets of already-constructed value-type
  values (i.e. `wan.make` / `group.make` outputs); the renderer
  serializes them via the module's own `toJSONValue` accessors
  rather than re-parsing user inputs.

  Returns the JSON-shape attrset. Caller passes through
  `builtins.toJSON` for the string form. Both convenience wrappers
  are exposed below.

  ===== toJSONValue =====

  Alias for `render` — kept for symmetry with the value-type
  modules' `toJSONValue` exports.

  ===== toJSON =====

  `toJSON config → string`. Convenience: `builtins.toJSON (render config)`.

  ===== defaultGlobal =====

  Exposed for tests and module-option defaults.

  ===== resolveAllocations =====

  Internal helper, exposed so unit tests can exercise it without
  reaching for the full `render`. After the auto-allocator removal
  this is a pure validator — it returns the input groups unchanged
  when the mark + table sets contain no duplicates, and throws
  otherwise. The name is kept for backwards compatibility with the
  module's call site.

  ===== schemaVersion =====

  Current schema version (int). Bumped on any backwards-incompatible
  change to the daemon-config shape. The daemon validates the
  version at startup.
*/
{
  lib,
  internal,
}:
let
  inherit (internal) wan group;

  schemaVersion = 1;

  defaultGlobal = {
    statePath = "/run/wanwatch/state.json";
    hooksDir = "/etc/wanwatch/hooks";
    metricsSocket = "/run/wanwatch/metrics.sock";
    logLevel = "info";
    hookTimeoutMs = 5000;
  };

  # findDuplicates : { <group-name> = <int>; } → [ { value, names } ]
  # Returns one entry per duplicated integer with the group names
  # that share it. Empty list when the mapping is collision-free.
  findDuplicates =
    groupValues:
    let
      names = builtins.attrNames groupValues;
      byValue = lib.foldl' (
        acc: n:
        let
          v = toString groupValues.${n};
        in
        acc
        // {
          ${v} = (acc.${v} or [ ]) ++ [ n ];
        }
      ) { } names;
      dups = lib.filterAttrs (_: ns: builtins.length ns > 1) byValue;
    in
    lib.mapAttrsToList (value: ns: {
      inherit value;
      names = ns;
    }) dups;

  formatDup =
    field: dup:
    "${field} ${dup.value} is shared by groups [${
      lib.concatMapStringsSep ", " (n: "'${n}'") dup.names
    }]";

  resolveAllocations =
    groups:
    let
      markValues = builtins.mapAttrs (_: g: g.mark) groups;
      tableValues = builtins.mapAttrs (_: g: g.table) groups;
      markDups = findDuplicates markValues;
      tableDups = findDuplicates tableValues;
      messages = builtins.map (formatDup "mark") markDups ++ builtins.map (formatDup "table") tableDups;
    in
    if messages == [ ] then
      groups
    else
      builtins.throw "wanwatch: duplicate mark or table across groups: ${lib.concatStringsSep "; " messages}";

  render =
    {
      global ? { },
      wans ? { },
      groups ? { },
    }:
    let
      validatedGroups = resolveAllocations groups;
    in
    {
      schema = schemaVersion;
      global = defaultGlobal // global;
      wans = builtins.mapAttrs (_: wan.toJSONValue) wans;
      groups = builtins.mapAttrs (_: group.toJSONValue) validatedGroups;
    };

  toJSONValue = render;
  toJSON = config: builtins.toJSON (render config);
in
{
  inherit
    render
    toJSONValue
    toJSON
    defaultGlobal
    schemaVersion
    resolveAllocations
    ;
}
