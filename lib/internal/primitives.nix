/*
  internal/primitives — generic helpers shared across the wanwatch
  library. Exposed under `wanwatch.internal.primitives`.

  Sections:
    - tryResult     — `tryOk`, `tryErr`
    - Validation    — `check`, `tagError`, `parseOptional`,
                      `partitionTry`, `isValidName`,
                      `isPositiveInt`, `formatErrors`

  This module owns nothing type-specific.

  Uses `nixpkgs.lib` freely (`lib.nameValuePair`,
  `lib.concatMapStringsSep`, etc.).

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

  ===== tagError =====

  `tagError kind msg`: returns a single `{name = kind; value = msg;}`
  error record. Equivalent shape to `check kind false msg`'s
  single-element list, but for the case where the caller already
  has a sequence of error strings (e.g. from `partitionTry`) and
  wants to tag each with the same `kind`. Curry-friendly:
  `builtins.map (tagError "kind") errStrings`.

  ===== partitionTry =====

  `partitionTry parser items`: applies a `tryResult`-returning
  parser to every item, partitions, returns
  `{ parsed = [<success values>]; errors = [<error strings>]; }`.
  Used by `probe.parseTargets` and `group.parseMembers` —
  callers that need both halves of the partition.

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
*/
{ lib }:
let
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

  tagError = lib.nameValuePair;

  check =
    kind: cond: msg:
    if cond then [ ] else [ (tagError kind msg) ];

  parseOptional = parser: input: if input == null then tryOk null else parser input;

  partitionTry =
    parser: items:
    let
      p = lib.partition (r: r.success) (builtins.map parser items);
    in
    {
      parsed = builtins.map (r: r.value) p.right;
      errors = builtins.map (r: r.error) p.wrong;
    };

  isValidName = s: builtins.isString s && builtins.match "[a-zA-Z][a-zA-Z0-9-]*" s != null;

  isPositiveInt = x: builtins.isInt x && x > 0;
in
{
  inherit
    tryOk
    tryErr
    formatErrors
    check
    tagError
    parseOptional
    partitionTry
    isValidName
    isPositiveInt
    ;
}
