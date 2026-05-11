/*
  internal/primitives — generic helpers shared across the wanwatch
  library. Exposed under `wanwatch.internal.primitives`.

  Sections:
    - Tagging       — `hasTag`, `ensureTag`
    - tryResult     — `tryOk`, `tryErr`
    - Validation    — `check`, `parseOptional`, `isValidName`,
                      `formatErrors`
    - Ordering      — `mkOrdering`, `compareByString`,
                      `orderingByString`

  This module owns nothing type-specific. Each value type
  (probe, wan, …) owns its own `is<Type>` predicate and its
  own `_type` tag string; this file provides only the
  type-agnostic infrastructure those modules build on.

  Uses `nixpkgs.lib` freely (`lib.nameValuePair`,
  `lib.concatMapStringsSep`, etc.).

  ===== hasTag =====

  `hasTag tag v`: true iff `v` is an attrset with `_type == tag`.
  Returns false for non-attrs, attrs without `_type`, and attrs
  with a different `_type`. Generic — each value type binds it
  to its own tag string via `is<Type> = primitives.hasTag "<type>"`.

  ===== ensureTag =====

  `ensureTag tag ctx v`: returns `v` if it matches `tag`; otherwise
  throws with a message mentioning both the expected and the
  observed shape, prefixed with `ctx` so users can locate the
  call site without a stack trace.

  ===== tryOk / tryErr =====

  Constructors of the `tryResult` shape used by every `tryMake`:

    tryOk  value : { success = true;  value;        error = null;  }
    tryErr error : { success = false; value = null; inherit error; }

  Same shape as libnet's `tryParse` result — interoperable.

  ===== check =====

  `check kind cond msg`: returns `[]` when `cond` is true,
  otherwise a one-element list with a `{name = kind; value = msg;}`
  error record. Designed for chaining with `++` so each validation
  rule collapses to a single line and the full validator becomes
  a `++` cascade.

  ===== parseOptional =====

  `parseOptional parser input`: null-passthrough adapter. When
  `input` is null, returns `tryOk null`. Otherwise delegates to
  `parser input`. Designed for optional fields like
  `gateways.{v4,v6}` where either may be absent.

  ===== isValidName =====

  True iff the input is a non-empty string matching the wanwatch
  identifier shape `[a-zA-Z][a-zA-Z0-9-]*` — used by `wan.name`,
  `group.name`, and similar entity-key validators. Stricter than
  libnet's interface-name check on purpose: identifiers must be
  unquoted-attr-key-clean, and the regex matches nftzones'
  `primitives.identifier`.

  ===== isPositiveInt =====

  True iff the input is an integer strictly greater than zero.
  Used by every value-type's positive-int field validators
  (`weight`, `priority`, `intervalMs`, `windowSize`, `table`,
  `mark`, …).

  ===== formatErrors =====

  `formatErrors ctx errors`: renders a list of `{name; value;}`
  error records into the canonical aggregated string
    `<ctx>: [<kind>] <msg>; [<kind2>] <msg2>; …`
  used by every `tryMake` failure path.

  ===== mkOrdering =====

  `mkOrdering compare`: derives `{lt; le; gt; ge; min; max;}` from
  a `compare : T → T → -1|0|1` function. Returns the input
  `compare` alongside the derived predicates. Eliminates the
  per-type ordering-boilerplate block.

  Note: `eq` is intentionally not derived — it's structural
  equality (`==`), not order-based equivalence. The two agree for
  value types with canonical `toJSON`, but conflating them blurs
  intent.

  ===== compareByString =====

  `compareByString toString a b`: lex-compare via the supplied
  stringifier. Useful for value types without a natural ordering —
  JSON canonical form gives a stable, deterministic, round-trippable
  total order.

  ===== orderingByString =====

  Convenience composition: `orderingByString toString = mkOrdering
  (compareByString toString)` — what every value-type module calls
  to get its ordering machinery in one line.
*/
{ lib }:
let
  hasTag = tag: v: builtins.isAttrs v && v ? _type && v._type == tag;

  ensureTag =
    tag: ctx: v:
    if hasTag tag v then
      v
    else
      builtins.throw "wanwatch: ${ctx}: expected ${tag} value, got ${
        if builtins.isAttrs v && v ? _type then "${v._type} value" else builtins.typeOf v
      }";

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

  formatErrors =
    ctx: errors: "${ctx}: " + lib.concatMapStringsSep "; " (e: "[${e.name}] ${e.value}") errors;

  check =
    kind: cond: msg:
    if cond then [ ] else [ (lib.nameValuePair kind msg) ];

  parseOptional = parser: input: if input == null then tryOk null else parser input;

  isValidName = s: builtins.isString s && builtins.match "[a-zA-Z][a-zA-Z0-9-]*" s != null;

  isPositiveInt = x: builtins.isInt x && x > 0;

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
  inherit
    hasTag
    ensureTag
    tryOk
    tryErr
    formatErrors
    check
    parseOptional
    isValidName
    isPositiveInt
    mkOrdering
    compareByString
    orderingByString
    ;
}
