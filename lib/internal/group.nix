/*
  wanwatch.group — Group value type.

  A Group is an ordered collection of Members under a Strategy,
  plus the fwmark + routing-table id used to dispatch its
  traffic. The Strategy decides which Member carries the group's
  traffic at any given moment.

  Fields (required unless marked optional):

    name     — wanwatch identifier
    members  — non-empty list of Members. Each Member input is an
               attrset passed to `member.make`; the group module
               owns the construction.
    strategy — enum, v1: "primary-backup" only. v2 will add
               "load-balance" once multi-active lands.
    table    — optional int. Routing-table id. Null means
               auto-allocated by `internal.tables.allocate`.
    mark     — optional int. fwmark. Null means auto-allocated
               by `internal.marks.allocate`.

  ===== make =====

  Input:  attrset of fields (any subset of optionals; required
          fields must be present)
  Output: tagged group value `{ _type = "group"; ... }` with each
          member parsed into a member value
  Throws: aggregated error string if validation fails.

  ===== tryMake =====

  Same as `make` but returns the `tryResult` shape. Aggregates
  errors across name, members (and each member's own validation),
  strategy, table, mark, and the duplicate-member cross-check.

  Error kinds:

    groupInvalidName       — name ∉ valid identifier
    groupNoMembers         — empty members list
    groupInvalidMember     — embedded member.make rejected (forwarded)
    groupDuplicateMember   — same WAN referenced by multiple members
    groupInvalidStrategy   — strategy ∉ {"primary-backup"}
    groupInvalidTable      — table set but not a positive integer
    groupInvalidMark       — mark set but not a positive integer

  ===== Accessors =====

  `name`, `members` (list of member values), `strategy`, `table`,
  `mark`, `wans` (derived: list of WAN-name strings referenced by
  the group's members).

  ===== Serialization =====

  `toJSONValue` is the canonical attrset form embedded by
  `config.render`. Members are nested via `member.toJSONValue`
  rather than as nested JSON strings.
*/
{
  lib,
  libnet,
  internal,
}:
let
  inherit (internal.primitives)
    tryOk
    tryErr
    check
    tagError
    partitionTry
    isValidName
    isPositiveInt
    ;
  formatErrors = internal.primitives.formatErrors "group.make";
  inherit (internal) member;

  # ===== Defaults =====

  defaults = {
    strategy = "primary-backup";
    table = null;
    mark = null;
  };

  validStrategies = [ "primary-backup" ];

  # ===== Validation helpers =====

  # member.tryMake speaks the standard tryResult shape; the
  # generic partitionTry handles the partition.
  parseMembers = partitionTry member.tryMake;

  # ===== Field-level validators =====

  validateName =
    name:
    check "groupInvalidName" (isValidName name)
      "name must be a valid wanwatch identifier (matching [a-zA-Z][a-zA-Z0-9-]*); got ${builtins.toJSON name}";

  # Takes the already-parsed members result from `tryMake`'s top-level
  # let so the partition isn't redone here. Without this threading,
  # `parseMembers` ran twice on every happy-path `tryMake` invocation —
  # once at the top, once inside this validator.
  validateMembers =
    members: membersResult:
    if !(builtins.isList members) then
      check "groupInvalidMember" false "members must be a list"
    else if members == [ ] then
      check "groupNoMembers" false "members must be non-empty"
    else
      builtins.map (tagError "groupInvalidMember") membersResult.errors;

  validateStrategy =
    strategy:
    check "groupInvalidStrategy" (builtins.elem strategy validStrategies)
      "strategy must be one of ${builtins.toJSON validStrategies}; got ${builtins.toJSON strategy}";

  validateTable =
    table:
    if table == null then
      [ ]
    else
      check "groupInvalidTable" (isPositiveInt table)
        "table must be a positive integer; got ${builtins.toJSON table}";

  validateMark =
    mark:
    if mark == null then
      [ ]
    else
      check "groupInvalidMark" (isPositiveInt mark)
        "mark must be a positive integer; got ${builtins.toJSON mark}";

  # Cross-check across already-parsed members. Run only when every
  # member parsed cleanly — otherwise the wan list contains nulls.
  detectDuplicateMembers =
    parsedMembers:
    let
      wans = builtins.map member.wan parsedMembers;
      counts = lib.foldl' (acc: w: acc // { ${w} = (acc.${w} or 0) + 1; }) { } wans;
      dups = lib.filterAttrs (_: c: c > 1) counts;
    in
    lib.mapAttrsToList (
      name: _: tagError "groupDuplicateMember" "wan '${name}' is referenced by more than one member"
    ) dups;

  # ===== Aggregated validation + construction =====

  mergeWithDefaults = user: {
    name = user.name or null;
    members = user.members or [ ];
    strategy = user.strategy or defaults.strategy;
    table = user.table or null;
    mark = user.mark or null;
  };

  collectErrors =
    cfg: membersResult:
    let
      membersList = builtins.isList cfg.members;
      membersClean = membersList && cfg.members != [ ] && membersResult.errors == [ ];

      structuralErrs =
        validateName cfg.name
        ++ validateMembers cfg.members membersResult
        ++ validateStrategy cfg.strategy
        ++ validateTable cfg.table
        ++ validateMark cfg.mark;

      duplicateErrs = if membersClean then detectDuplicateMembers membersResult.parsed else [ ];
    in
    structuralErrs ++ duplicateErrs;

  buildValue = cfg: parsedMembers: {
    inherit (cfg)
      name
      strategy
      table
      mark
      ;
    members = parsedMembers;
  };

  tryMake =
    user:
    let
      cfg = mergeWithDefaults user;
      membersResult = parseMembers (if builtins.isList cfg.members then cfg.members else [ ]);
      errors = collectErrors cfg membersResult;
    in
    if errors == [ ] then tryOk (buildValue cfg membersResult.parsed) else tryErr (formatErrors errors);

  make =
    user:
    let
      r = tryMake user;
    in
    if r.success then r.value else builtins.throw r.error;

  # ===== Accessors =====

  name = g: g.name;
  members = g: g.members;
  strategy = g: g.strategy;
  table = g: g.table;
  mark = g: g.mark;
  wans = g: builtins.map member.wan g.members;

  # ===== Serialization =====

  toJSONValue = g: {
    inherit (g)
      name
      strategy
      table
      mark
      ;
    members = builtins.map member.toJSONValue g.members;
  };
in
{
  inherit
    make
    tryMake
    toJSONValue
    ;
  inherit
    name
    members
    strategy
    table
    mark
    wans
    ;
  inherit defaults;
  # Exposed so `types/group.nix` can derive its `groupStrategy`
  # enum from the same list — single source of truth on the Nix side.
  inherit validStrategies;
}
