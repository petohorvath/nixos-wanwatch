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
      "groups": { "<name>": <group.toJSONValue with mark/table
                             resolved>, ... }
    }

  Auto-allocation: any group with `mark = null` or `table = null`
  has those fields filled in by `internal.marks.allocate` /
  `internal.tables.allocate` over the subset of groups whose
  field is null. Groups with explicit user-set values keep them.

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
  reaching for the full `render`. Allocates marks + tables across
  the auto-allocation subset and returns groups with the resolved
  values substituted in.

  Collision detection: explicit `mark` / `table` values
  participate in the same id space as the auto-allocated ones.
  Two groups can't share a mark or a table — whether both are
  explicit, both auto, or one of each. Throws on collision so the
  drift surfaces at module-eval time, not as a silent routing bug
  in production.

  ===== schemaVersion =====

  Current schema version (int). Bumped on any backwards-incompatible
  change to the daemon-config shape. The daemon validates the
  version at startup.
*/
{
  lib,
  libnet,
  internal,
}:
let
  inherit (internal)
    wan
    group
    marks
    tables
    ;

  schemaVersion = 1;

  defaultGlobal = {
    statePath = "/run/wanwatch/state.json";
    hooksDir = "/etc/wanwatch/hooks";
    metricsSocket = "/run/wanwatch/metrics.sock";
    logLevel = "info";
    hookTimeoutMs = 5000;
  };

  resolveAllocations =
    groups:
    let
      names = builtins.attrNames groups;
      explicit =
        field:
        lib.foldl' (
          acc: n:
          let
            v = groups.${n}.${field};
          in
          if v == null then acc else acc // { ${toString v} = n; }
        ) { } names;

      explicitMarks = explicit "mark";
      explicitTables = explicit "table";

      autoMarkNames = builtins.filter (n: groups.${n}.mark == null) names;
      autoTableNames = builtins.filter (n: groups.${n}.table == null) names;

      autoMarks = marks.allocate autoMarkNames;
      autoTables = tables.allocate autoTableNames;

      # Surface collisions between auto-allocated values and the
      # explicit set. Two groups sharing a mark or a table would
      # quietly mis-route in production — fail fast at eval time.
      checkCollision =
        field: assigned: explicitMap:
        lib.foldl' (
          _: n:
          let
            v = assigned.${n};
            owner = explicitMap.${toString v} or null;
          in
          if owner == null then
            null
          else
            builtins.throw "wanwatch: config.resolveAllocations: auto-allocated ${field} ${toString v} for group '${n}' collides with explicit value on group '${owner}'"
        ) null (builtins.attrNames assigned);

      _markCollision = checkCollision "mark" autoMarks explicitMarks;
      _tableCollision = checkCollision "table" autoTables explicitTables;
    in
    builtins.seq _markCollision (
      builtins.seq _tableCollision (
        builtins.mapAttrs (
          name: g:
          g
          // {
            mark = if g.mark == null then autoMarks.${name} else g.mark;
            table = if g.table == null then autoTables.${name} else g.table;
          }
        ) groups
      )
    );

  render =
    {
      global ? { },
      wans ? { },
      groups ? { },
    }:
    let
      resolvedGroups = resolveAllocations groups;
    in
    {
      schema = schemaVersion;
      global = defaultGlobal // global;
      wans = builtins.mapAttrs (_: wan.toJSONValue) wans;
      groups = builtins.mapAttrs (_: group.toJSONValue) resolvedGroups;
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
