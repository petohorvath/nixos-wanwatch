/*
  Member value type, exposed as `wanwatch.member`. A Member is a WAN's
  participation in one Group. It names the WAN rather than embedding
  it, so Member stays a leaf type; the surrounding configuration must
  declare a WAN with that name. A member value carries:

    wan      — wanwatch identifier of the referenced WAN
    weight   — positive integer, default 100; unused until multi-active
               strategies exist
    priority — positive integer, default 1; primary-backup picks the
               healthy Member with the lowest priority
*/
{
  internal,
}:
let
  inherit (internal.primitives)
    check
    isPositiveInt
    isValidName
    tryErr
    tryOk
    ;

  formatErrors = internal.primitives.formatErrors "member.make";

  defaults = {
    weight = 100;
    priority = 1;
  };

  validateWan =
    wan:
    check "memberInvalidWan" (isValidName wan)
      "wan must be a valid wanwatch identifier (matching [a-zA-Z][a-zA-Z0-9-]*); got ${builtins.toJSON wan}";

  validateWeight =
    weight:
    check "memberInvalidWeight" (isPositiveInt weight)
      "weight must be a positive integer; got ${builtins.toJSON weight}";

  validatePriority =
    priority:
    check "memberInvalidPriority" (isPositiveInt priority)
      "priority must be a positive integer; got ${builtins.toJSON priority}";

  mergeWithDefaults = input: {
    wan = input.wan or null;
    weight = input.weight or defaults.weight;
    priority = input.priority or defaults.priority;
  };

  collectErrors =
    fields: validateWan fields.wan ++ validateWeight fields.weight ++ validatePriority fields.priority;

  /*
    Validate Member input without throwing, reporting every violation
    in one message.

    `input`: an attrset with `wan` and optional `weight` and
    `priority`; missing optional fields take `defaults`.

    Returns a `tryResult` whose value is the member value. Error kinds:

      memberInvalidWan      — wan is not a valid identifier
      memberInvalidWeight   — weight is not a positive integer
      memberInvalidPriority — priority is not a positive integer
  */
  tryMake =
    input:
    let
      fields = mergeWithDefaults input;
      errors = collectErrors fields;
    in
    if errors == [ ] then tryOk fields else tryErr (formatErrors errors);

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
    inherit (member) priority wan weight;
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
