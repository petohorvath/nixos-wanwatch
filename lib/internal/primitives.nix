/*
  Generic helpers shared by the wanwatch value types: `tryResult`
  constructors, error records, and identifier and integer predicates.
  Exposed as `wanwatch.internal.primitives`; nothing here is
  type-specific.
*/
{ lib }:
{
  /*
    Build a successful `tryResult`, the shape every `tryMake` returns.
    It matches libnet's `tryParse` results, so the two interoperate.

    `value`: the constructed value.

    Returns `{ success = true; value; error = null; }`.
  */
  tryOk = value: {
    success = true;
    inherit value;
    error = null;
  };

  /*
    Build a failed `tryResult`, the shape every `tryMake` returns.

    `error`: the error message string.

    Returns `{ success = false; value = null; error; }`.
  */
  tryErr = error: {
    success = false;
    value = null;
    inherit error;
  };

  /*
    Render error records into the single message every `tryMake`
    failure carries, so users see all violations at once.

    `context`: the failing constructor, such as `"probe.make"`.
    `errors`: a list of `{ name = kind; value = message; }` records.

    Returns `"<context>: [<kind>] <message>; [<kind>] <message>; …"`.
  */
  formatErrors =
    context: errors:
    "${context}: " + lib.concatMapStringsSep "; " (error: "[${error.name}] ${error.value}") errors;

  /*
    Turn one validation rule into an error list, so a validator is a
    `++` chain of `check` calls.

    `kind`: the error kind, such as `"probeInvalidMethod"`.
    `condition`: true when the rule holds.
    `message`: the explanation reported when the rule fails.

    Returns `[ ]` when `condition` holds; otherwise a one-element list
    holding the `{ name = kind; value = message; }` error record.
  */
  check =
    kind: condition: message:
    if condition then [ ] else [ (lib.nameValuePair kind message) ];

  /*
    Apply a `tryResult`-returning parser to every item and keep both
    outcomes, so validators can report errors while constructors use
    the parsed values.

    `parser`: a function returning a `tryResult`.
    `items`: the inputs to parse.

    Returns `{ parsed = [ <values> ]; errors = [ <error strings> ]; }`,
    each in input order.
  */
  partitionTry =
    parser: items:
    let
      results = lib.partition (result: result.success) (map parser items);
    in
    {
      parsed = map (result: result.value) results.right;
      errors = map (result: result.error) results.wrong;
    };

  /*
    Test the wanwatch identifier shape used for WAN, Group, and Member
    references. It is stricter than libnet's interface-name check so
    identifiers stay valid unquoted attribute names.

    `value`: any value.

    Returns true when `value` is a string matching
    `[a-zA-Z][a-zA-Z0-9-]*`.
  */
  isValidName =
    value: builtins.isString value && builtins.match "[a-zA-Z][a-zA-Z0-9-]*" value != null;

  /*
    Test the positive-integer fields shared by the value types, such
    as `weight`, `priority`, and `intervalMs`.

    `value`: any value.

    Returns true when `value` is an integer greater than zero.
  */
  isPositiveInt = value: builtins.isInt value && value > 0;
}
