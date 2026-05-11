/*
  internal/types — pure-Nix primitives shared across the wanwatch
  library. Exposed under `wanwatch.internal.types`.

  Sections:
    - Tagging       — `tags`, `hasTag`, `is<Type>`, `ensureTag`
    - tryResult     — `tryOk`, `tryErr`
    - Error records — `nameValuePair`, `formatErrors`
    - Validation    — `check`, `parseOptional`, `isValidName`
    - Ordering      — `mkOrdering`, `compareByString`,
                      `orderingByString`

  Pure-Nix — no `nixpkgs.lib`, no `builtins.path`, no I/O. Safe to
  import from any layer of the lib without bringing in pkgs.

  ===== tags =====

  Canonical tag strings. Exported as a named attrset so callers can
  reach `tags.wan` rather than open-coding the string `"wan"` —
  catches typos at evaluation time and lets the test suite assert
  the full set in one place.

  ===== hasTag =====

  Inputs:
    tag — the tag string to test against (e.g. `tags.wan`)
    v   — any value

  Output: true iff `v` is an attrset, has a `_type` attr, and
  `_type == tag`. Returns false for non-attrs (strings, ints, null,
  lists), for attrs without `_type`, and for attrs whose `_type`
  is a different string.

  ===== is<Type> =====

  Convenience curries of `hasTag` over the canonical tag set:
    isWan / isProbe / isGroup / isMember. Each is curried so it
  can be used as a list predicate (`builtins.filter isWan xs`).

  ===== tryOk / tryErr =====

  The two constructors of the `tryResult` shape used by every
  `tryMake` function in the lib:

    tryOk  value : { success = true;  value;        error = null;  }
    tryErr error : { success = false; value = null; inherit error; }

  Callers of `tryMake` dispatch on `.success`; on failure they
  surface `.error` to the user. The `make` variant of each type
  throws on the same conditions; both paths go through identical
  validation and share the same error strings.

  ===== ensureTag =====

  Inputs:  tag, ctx, v  — see the section banner below for details.
  Output:  `v` unchanged if it matches; otherwise throws with a
           message that mentions both the expected and observed
           shape. Used by internal helpers that consume a tagged
           value to fail loudly rather than silently propagating
           a wrong-typed input.

  ===== nameValuePair =====

  Constructor for the `{ name; value; }` records used as error
  entries by every `tryMake` validator. Aggregated into a list and
  rendered by `formatErrors`.

  ===== formatErrors =====

  Inputs:
    ctx    — caller's name (e.g. `"probe.make"`)
    errors — list of `nameValuePair` records

  Output: a single string of the shape
    `<ctx>: [<kind>] <msg>; [<kind2>] <msg2>; ...`
  Used by every `tryMake` to render the aggregated error list.

  ===== check =====

  Inputs:
    kind — error-kind tag (e.g. `"probeInvalidMethod"`)
    cond — bool; true ⇒ valid
    msg  — error message string

  Output: `[]` when `cond` is true, otherwise a one-element list
  with the matching `nameValuePair`. Designed for chaining with `++`
  in validator helpers: each rule collapses to a single line, and
  the full validator becomes a `++` cascade of `check ...` calls.

  ===== parseOptional =====

  Inputs:
    parser — a function returning a `tryResult` (libnet's tryParse
             variants fit)
    input  — the value to parse, may be null

  Output: `tryOk null` when input is null, otherwise `parser input`.
  Null-passthrough adapter for optional fields (e.g. v4/v6 gateway
  slots where either may be absent).

  ===== isValidName =====

  True iff the input is a non-empty string matching the wanwatch
  identifier shape `[a-zA-Z][a-zA-Z0-9-]*` — used by `wan.name`,
  `group.name`, and `member.<key>` validators. Matches nftzones'
  `primitives.identifier` regex.

  ===== mkOrdering =====

  Inputs:
    compare — function `T → T → -1 | 0 | 1`

  Output: `{ compare; lt; le; gt; ge; min; max; }` with the
  remaining six predicates derived in the usual way. Eliminates
  the per-type ordering-boilerplate block.

  Note: `eq` is intentionally not derived — it's structural
  equality (`==`), not order-based equivalence. The two happen to
  agree for value types whose `toJSON` is canonical, but conflating
  them blurs intent.

  ===== compareByString =====

  Inputs:
    toString — function `T → string` (e.g. `toJSON`)

  Output: a `compare` function. Useful for value types without a
  natural ordering — JSON canonical form gives a stable total
  order.

  ===== orderingByString =====

  Convenience: `orderingByString toString = mkOrdering
  (compareByString toString)` — the single call value-types use
  to get their ordering machinery.
*/
let
  tags = {
    wan = "wan";
    probe = "probe";
    group = "group";
    member = "member";
  };

  hasTag = tag: v: builtins.isAttrs v && v ? _type && v._type == tag;

  isWan = hasTag tags.wan;
  isProbe = hasTag tags.probe;
  isGroup = hasTag tags.group;
  isMember = hasTag tags.member;

  tryOk = value: {
    success = true;
    inherit value;
    error = null;
  };

  tryErr = error: {
    success = false;
    value = null;
    inherit error;
  };

  ensureTag =
    tag: ctx: v:
    if hasTag tag v then
      v
    else
      builtins.throw "wanwatch: ${ctx}: expected ${tag} value, got ${
        if builtins.isAttrs v && v ? _type then "${v._type} value" else builtins.typeOf v
      }";

  nameValuePair = name: value: { inherit name value; };

  formatErrors =
    ctx: errors:
    "${ctx}: " + builtins.concatStringsSep "; " (builtins.map (e: "[${e.name}] ${e.value}") errors);

  check =
    kind: cond: msg:
    if cond then [ ] else [ (nameValuePair kind msg) ];

  parseOptional = parser: input: if input == null then tryOk null else parser input;

  isValidName = s: builtins.isString s && builtins.match "[a-zA-Z][a-zA-Z0-9-]*" s != null;

  mkOrdering = compare: {
    inherit compare;
    lt = a: b: compare a b == -1;
    le = a: b: compare a b <= 0;
    gt = a: b: compare a b == 1;
    ge = a: b: compare a b >= 0;
    min = a: b: if compare a b <= 0 then a else b;
    max = a: b: if compare a b >= 0 then a else b;
  };

  compareByString =
    toString: a: b:
    let
      sa = toString a;
      sb = toString b;
    in
    if sa < sb then
      -1
    else if sa > sb then
      1
    else
      0;

  orderingByString = toString: mkOrdering (compareByString toString);
in
{
  inherit tags hasTag;
  inherit
    isWan
    isProbe
    isGroup
    isMember
    ;
  inherit tryOk tryErr ensureTag;
  inherit
    nameValuePair
    formatErrors
    check
    parseOptional
    isValidName
    mkOrdering
    compareByString
    orderingByString
    ;
}
