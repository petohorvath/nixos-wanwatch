# Generic helpers shared by the wanwatch value types: `tryResult`
# constructors, error records, and identifier and integer predicates.
# Not exported from `lib/default.nix`; nothing here is type-specific.
{ lib }:
let
  # Shared by `isValidName` and `checkIdentifier`, so the predicate and
  # its message agree.
  identifierPattern = "[a-zA-Z][a-zA-Z0-9-]*";

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
    Test the wanwatch identifier shape used for WAN, Group, and Member
    references. It is stricter than libnet's interface-name check so
    identifiers stay valid unquoted attribute names.

    `value`: any value.

    Returns true when `value` is a string matching
    `[a-zA-Z][a-zA-Z0-9-]*`.
  */
  isValidName = value: builtins.isString value && builtins.match identifierPattern value != null;

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
    Reject a `tryMake` input that isn't an attrset before any field is
    read, so it fails with its own kind instead of falling back to the
    defaults or reporting a misleading field error.

    `kind`: the error kind, such as `"probeNotAnAttrset"`.
    `formatErrors`: the type's error formatter, such as
    `formatErrors "probe.make"`.
    `tryMakeFields`: the type's `tryMake` for an attrset input.
    `input`: any value.

    Returns `tryMakeFields input` when `input` is an attrset; otherwise
    a failed `tryResult` holding the `kind` error.
  */
  requireAttrset =
    kind: formatErrors: tryMakeFields: input:
    if builtins.isAttrs input then
      tryMakeFields input
    else
      tryErr (
        formatErrors (check kind false "input must be an attribute set; got ${builtins.typeOf input}")
      );
in
{
  inherit
    check
    isValidName
    requireAttrset
    tryErr
    ;

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
    Check one identifier field, so WANs, Groups, and Members report an
    invalid identifier with the same message.

    `kind`: the error kind, such as `"wanInvalidName"`.
    `field`: the field name the message reports, such as `"name"`.
    `value`: the field's value.

    Returns `[ ]` when `value` is a valid identifier (`isValidName`);
    otherwise a one-element list holding the error record.
  */
  checkIdentifier =
    kind: field: value:
    check kind (isValidName value)
      "${field} must be a valid wanwatch identifier (matching ${identifierPattern}); got ${builtins.toJSON value}";

  /*
    Check that an attrset has only known fields, so a misspelled
    optional field fails instead of falling back to its default.

    `kind`: the error kind, such as `"probeUnknownField"`.
    `name`: the attrset's name in the message, such as `"thresholds"`.
    `knownFields`: the field names the attrset may have.
    `attrs`: the attrset to check. Any other value passes, so the
    caller can report it under its own kind.

    Returns `[ ]` when every field is known; otherwise a one-element
    list holding an error record that lists the unknown fields.
  */
  checkUnknownFields =
    kind: name: knownFields: attrs:
    let
      unknownFields = lib.subtractLists knownFields (builtins.attrNames attrs);
    in
    lib.optionals (builtins.isAttrs attrs) (
      check kind (unknownFields == [ ])
        "${name} has unknown fields ${builtins.toJSON unknownFields}; known fields are ${builtins.toJSON knownFields}"
    );

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
    Test the positive-integer fields shared by the value types, such
    as `priority` and `intervalMs`.

    `value`: any value.

    Returns true when `value` is an integer greater than zero.
  */
  isPositiveInt = value: builtins.isInt value && value > 0;
}
