/*
  Member value type, exposed as `wanwatch.member`. A Member is a WAN's
  participation in one Group. It names the WAN rather than embedding
  it, so Member stays a leaf type; the surrounding configuration must
  declare a WAN with that name. A member value carries:

    wan      — wanwatch identifier of the referenced WAN
    priority — positive integer, default 1; primary-backup picks the
               healthy Member with the lowest priority
*/
{
  internal,
}:
let
  inherit (internal.primitives)
    check
    checkIdentifier
    checkUnknownFields
    isPositiveInt
    requireAttrset
    tryErr
    tryOk
    ;

  formatErrors = internal.primitives.formatErrors "member.make";

  defaults = {
    priority = 1;
  };

  knownFields = [
    "wan"
    "priority"
  ];

  validatePriority =
    priority:
    check "memberInvalidPriority" (isPositiveInt priority)
      "priority must be a positive integer; got ${builtins.toJSON priority}";

  mergeWithDefaults = input: {
    wan = input.wan or null;
    priority = input.priority or defaults.priority;
  };

  collectErrors =
    input: fields:
    checkUnknownFields "memberUnknownField" "member" knownFields input
    ++ checkIdentifier "memberInvalidWan" "wan" fields.wan
    ++ validatePriority fields.priority;

  /*
    Validate Member input without throwing, reporting every violation
    in one message.

    `input`: an attrset with `wan` and optional `priority`, which
    defaults to `defaults.priority`.

    Returns a `tryResult` whose value is the member value. Error kinds:

      memberNotAnAttrset    — input is not an attribute set
      memberUnknownField    — a field outside the Member fields
      memberInvalidWan      — wan is not a valid identifier
      memberInvalidPriority — priority is not a positive integer
  */
  tryMake = requireAttrset "memberNotAnAttrset" formatErrors (
    input:
    let
      fields = mergeWithDefaults input;
      errors = collectErrors input fields;
    in
    if errors == [ ] then tryOk fields else tryErr (formatErrors errors)
  );

  /*
    Construct a member value, failing evaluation on invalid input.

    `input`: the attrset accepted by `tryMake`.

    Returns the member value. Throws the aggregated `tryMake` error
    message when validation fails.
  */
  make =
    input:
    let
      result = tryMake input;
    in
    if result.success then result.value else throw result.error;

  /*
    Serialize a Member for the daemon-config JSON.

    `member`: a member value.

    Returns the JSON-shaped attrset embedded by `group.toJSONValue`.
  */
  toJSONValue = member: {
    inherit (member) priority wan;
  };
in
{
  inherit
    defaults
    make
    toJSONValue
    tryMake
    ;
}
