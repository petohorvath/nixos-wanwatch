/*
  Group value type, exposed as `wanwatch.group`. A Group is a set of
  Members under a Strategy, plus the fwmark and routing table that
  dispatch its traffic; the Strategy picks which Member carries the
  traffic. A group value carries:

    name     — wanwatch identifier
    members  — non-empty attrset of member values keyed by the WAN
               each one references, built from the `members` inputs
    strategy — "primary-backup", the only Strategy
    table    — routing-table ID in [1000, 32767], serving both IPv4
               and IPv6 routes
    mark     — fwmark in [1000, 32767] that selects `table`
*/
{
  lib,
  internal,
}:
let
  inherit (internal) member;
  inherit (internal.primitives)
    check
    checkIdentifier
    checkUnknownFields
    tryErr
    tryOk
    ;

  formatErrors = internal.primitives.formatErrors "group.make";

  knownFields = [
    "name"
    "members"
    "strategy"
    "table"
    "mark"
  ];

  # `table` and `mark` are required, so they have no defaults.
  defaults = {
    strategy = "primary-backup";
  };

  # Matches `fwmark` and `routingTableId` in `lib/types/primitives.nix`;
  # option types do not expose their bounds for reuse.
  markTableMin = 1000;
  markTableMax = 32767;

  isMarkTableInt = value: builtins.isInt value && value >= markTableMin && value <= markTableMax;

  # `types/group.nix` derives its enum option type from this list.
  validStrategies = [ "primary-backup" ];

  # The attribute key names the WAN, so a member input may omit `wan`.
  tryMakeMember =
    wan: input: member.tryMake (if builtins.isAttrs input then { inherit wan; } // input else input);

  # Names the rejected member in its forwarded error.
  wrapMemberError =
    key: result: lib.nameValuePair "groupInvalidMember" "members.${key}: ${result.error}";

  # A member input may set `wan`, but only to its own key.
  checkMemberKey =
    key: value:
    check "groupInvalidMember" (
      value.wan == key
    ) "members.${key}: wan must match the attribute key; got ${builtins.toJSON value.wan}";

  /*
    Parse each member input under its key, so `tryMake` parses every
    member once and reports each failure with its key.

    Returns `{ parsed = { <wan> = <member value>; }; errors; }`, where
    `errors` lists `groupInvalidMember` records for rejected inputs
    and for parsed members whose `wan` differs from their key.
  */
  parseMembers =
    members:
    let
      results = builtins.mapAttrs tryMakeMember members;
      rejected = lib.filterAttrs (_: result: !result.success) results;
      parsed = lib.mapAttrs (_: result: result.value) (
        lib.filterAttrs (_: result: result.success) results
      );
    in
    {
      inherit parsed;
      errors =
        lib.mapAttrsToList wrapMemberError rejected
        ++ lib.concatLists (lib.mapAttrsToList checkMemberKey parsed);
    };

  # Takes `tryMake`'s parsed members so each member is parsed once.
  validateMembers =
    members: parsedMembers:
    if !(builtins.isAttrs members) then
      check "groupInvalidMember" false
        "members must be an attribute set keyed by WAN name; got ${builtins.typeOf members}"
    else if members == { } then
      check "groupNoMembers" false "members must be non-empty"
    else
      parsedMembers.errors;

  validateStrategy =
    strategy:
    check "groupInvalidStrategy" (builtins.elem strategy validStrategies)
      "strategy must be one of ${builtins.toJSON validStrategies}; got ${builtins.toJSON strategy}";

  validateTable =
    table:
    check "groupInvalidTable" (isMarkTableInt table)
      "table is required and must be an integer in [${toString markTableMin}, ${toString markTableMax}]; got ${builtins.toJSON table}";

  validateMark =
    mark:
    check "groupInvalidMark" (isMarkTableInt mark)
      "mark is required and must be an integer in [${toString markTableMin}, ${toString markTableMax}]; got ${builtins.toJSON mark}";

  # Missing `table` and `mark` become null, so their validators report
  # them instead of `make` failing on a missing attribute.
  mergeWithDefaults = input: {
    name = input.name or null;
    members = input.members or { };
    strategy = input.strategy or defaults.strategy;
    table = input.table or null;
    mark = input.mark or null;
  };

  collectErrors =
    input: fields: parsedMembers:
    checkUnknownFields "groupUnknownField" "group" knownFields input
    ++ checkIdentifier "groupInvalidName" "name" fields.name
    ++ validateMembers fields.members parsedMembers
    ++ validateStrategy fields.strategy
    ++ validateTable fields.table
    ++ validateMark fields.mark;

  /*
    Validate Group input without throwing, reporting every violation,
    including each member's, in one message.

    `input`: an attrset with `name`, `members`, `table`, `mark`, and
    optional `strategy` (default "primary-backup"). `members` maps
    each WAN name to a `member.tryMake` input, which may omit `wan`.

    Returns a `tryResult` whose value is the group value. Error kinds:

      groupUnknownField    — a field outside the Group fields
      groupInvalidName     — name is not a valid identifier
      groupNoMembers       — members is empty
      groupInvalidMember   — members is not an attrset,
                             `member.tryMake` rejected a member, or a
                             member's `wan` differs from its key
      groupInvalidStrategy — strategy not in `validStrategies`
      groupInvalidTable    — table missing or outside [1000, 32767]
      groupInvalidMark     — mark missing or outside [1000, 32767]
  */
  tryMake =
    input:
    let
      fields = mergeWithDefaults input;
      parsedMembers = parseMembers (if builtins.isAttrs fields.members then fields.members else { });
      errors = collectErrors input fields parsedMembers;
    in
    if errors == [ ] then
      tryOk (fields // { members = parsedMembers.parsed; })
    else
      tryErr (formatErrors errors);

  /*
    Construct a group value, failing evaluation on invalid input.

    `input`: the attrset accepted by `tryMake`.

    Returns the group value with each member parsed into a member
    value. Throws the aggregated `tryMake` error message when
    validation fails.
  */
  make =
    input:
    let
      result = tryMake input;
    in
    if result.success then result.value else throw result.error;

  /*
    List the WANs a Group references.

    `group`: a group value.

    Returns the WAN names, sorted.
  */
  wans = group: builtins.attrNames group.members;

  /*
    Serialize a Group for the daemon-config JSON.

    `group`: a group value.

    Returns the JSON-shaped attrset embedded by `config.render`, with
    members keyed by WAN name.
  */
  toJSONValue = group: {
    inherit (group)
      mark
      name
      strategy
      table
      ;
    members = builtins.mapAttrs (_: member.toJSONValue) group.members;
  };
in
{
  inherit
    defaults
    make
    toJSONValue
    tryMake
    validStrategies
    wans
    ;
}
