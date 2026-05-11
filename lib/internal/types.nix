/*
  internal/types — type-tagging primitives for the wanwatch pure-Nix
  library. Exposed under `wanwatch.internal.types`.

  Every value type produced by the lib (`wan`, `probe`, `group`,
  `member`) is a tagged attrset carrying `_type = "<name>"`. This
  module is the single source of truth for those tag strings, the
  predicates that recognise them, and the `tryResult` shape used by
  every `tryMake` function.

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
  lists), for attrs without `_type`, and for attrs whose `_type` is
  a different string.

  ===== is<Type> =====

  Convenience curries of `hasTag` over the canonical tag set:
    isWan / isProbe / isGroup / isMember.

  Each is curried so it can be used as a list predicate
  (`builtins.filter isWan xs`).

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

  Inputs:
    tag — required tag (e.g. `tags.wan`)
    ctx — caller's name (for the throw message)
    v   — value to check

  Output: `v` unchanged if it matches; otherwise throws with a
  message that mentions both the expected and observed shape:
    `wanwatch: <ctx>: expected wan value, got probe value`
    `wanwatch: <ctx>: expected wan value, got string`

  Used by every internal helper that consumes a tagged value to
  fail loudly rather than silently propagating a wrong-typed input.
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
}
