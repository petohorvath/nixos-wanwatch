/*
  wanwatch.member — a WAN's per-Group membership.

  A Member is a labelled reference to a WAN inside a particular
  Group. It carries per-Group attributes (weight, priority) but
  not the WAN itself — Members are constructed standalone, and the
  Group config layer resolves the `wan` field against the global
  WAN registry. This keeps Member a leaf type with no upstream
  dependency on `wan`.

  Fields:

    wan      — string; valid wanwatch identifier
               (`[a-zA-Z][a-zA-Z0-9-]*`). Must match the `name` of
               a WAN declared in the surrounding config.
    weight   — positive int, default 100. Tiebreaker among members
               with equal priority (Pass 3 strategies only consult
               priority; weight matters once multi-active lands).
    priority — positive int, default 1. Lower preferred; the
               primary-backup strategy picks the lowest-priority
               healthy Member.

  ===== make =====

  Input:  attrset of fields (any subset; missing fields take defaults)
  Output: tagged member value `{ _type = "member"; ... }`
  Throws: aggregated error string if any field fails validation.

  ===== tryMake =====

  Same as `make` but returns the `tryResult` shape instead of
  throwing. Errors are aggregated nftzones-style.

  Error kinds:

    memberInvalidWan       — wan ∉ valid-identifier
    memberInvalidWeight    — weight is not a positive int
    memberInvalidPriority  — priority is not a positive int

  ===== Accessors =====

  `wan`, `weight`, `priority`.

  ===== Equality and ordering =====

  `eq` is structural attrset equality. `compare` derives from the
  canonical JSON form via `primitives.orderingByString`.
  `lt`/`le`/`gt`/`ge`/`min`/`max` derive from `compare`.

  ===== toJSON =====

  Returns a JSON string suitable for the daemon config. Keys are
  sorted alphabetically by `builtins.toJSON`.
*/
{
  lib,
  libnet,
  internal,
}:
let
  inherit (internal.primitives)
    hasTag
    tryOk
    tryErr
    check
    isValidName
    orderingByString
    ;
  formatErrors = internal.primitives.formatErrors "member.make";

  tag = "member";
  isMember = hasTag tag;

  # ===== Defaults =====

  defaults = {
    weight = 100;
    priority = 1;
  };

  # ===== Validation helpers =====

  isPositiveInt = x: builtins.isInt x && x > 0;

  # ===== Field-level validators =====

  validateWan =
    wan:
    check "memberInvalidWan" (isValidName wan)
      "wan must be a valid wanwatch identifier (matching [a-zA-Z][a-zA-Z0-9-]*); got ${builtins.toJSON wan}";

  validateWeight =
    w:
    check "memberInvalidWeight" (isPositiveInt w)
      "weight must be a positive integer; got ${builtins.toJSON w}";

  validatePriority =
    p:
    check "memberInvalidPriority" (isPositiveInt p)
      "priority must be a positive integer; got ${builtins.toJSON p}";

  # ===== Aggregated validation + construction =====

  mergeWithDefaults = user: {
    wan = user.wan or null;
    weight = user.weight or defaults.weight;
    priority = user.priority or defaults.priority;
  };

  collectErrors =
    cfg: validateWan cfg.wan ++ validateWeight cfg.weight ++ validatePriority cfg.priority;

  buildValue = cfg: {
    _type = tag;
    inherit (cfg) wan weight priority;
  };

  tryMake =
    user:
    let
      cfg = mergeWithDefaults user;
      errors = collectErrors cfg;
    in
    if errors == [ ] then tryOk (buildValue cfg) else tryErr (formatErrors errors);

  make =
    user:
    let
      r = tryMake user;
    in
    if r.success then r.value else builtins.throw r.error;

  # ===== Accessors =====

  wan = m: m.wan;
  weight = m: m.weight;
  priority = m: m.priority;

  # ===== Serialization =====

  toJSONValue = m: {
    inherit (m)
      _type
      wan
      weight
      priority
      ;
  };

  toJSON = m: builtins.toJSON (toJSONValue m);

  # ===== Equality and ordering =====

  eq = a: b: a == b;
  inherit (orderingByString toJSON)
    compare
    lt
    le
    gt
    ge
    min
    max
    ;
in
{
  inherit
    make
    tryMake
    isMember
    toJSON
    toJSONValue
    ;
  inherit wan weight priority;
  inherit
    eq
    compare
    lt
    le
    gt
    ge
    min
    max
    ;
  inherit defaults;
}
