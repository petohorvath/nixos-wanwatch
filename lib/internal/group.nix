/*
  Group value type, exposed as `wanwatch.group`. A Group is an ordered
  list of Members under a Strategy, plus the fwmark and routing table
  that dispatch its traffic; the Strategy picks which Member carries
  the traffic. A group value carries:

    name     — wanwatch identifier
    members  — non-empty list of member values, built from the
               `members` inputs
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
    isValidName
    partitionTry
    tryErr
    tryOk
    ;

  formatErrors = internal.primitives.formatErrors "group.make";

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

  parseMembers = partitionTry member.tryMake;

  validateName =
    name:
    check "groupInvalidName" (isValidName name)
      "name must be a valid wanwatch identifier (matching [a-zA-Z][a-zA-Z0-9-]*); got ${builtins.toJSON name}";

  # Takes `tryMake`'s parsed members so each member is parsed once.
  validateMembers =
    members: parsedMembers:
    if !(builtins.isList members) then
      check "groupInvalidMember" false "members must be a list"
    else if members == [ ] then
      check "groupNoMembers" false "members must be non-empty"
    else
      map (lib.nameValuePair "groupInvalidMember") parsedMembers.errors;

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

  # Requires cleanly parsed members; otherwise WAN names may be null.
  findDuplicateMembers =
    members:
    lib.pipe members [
      (lib.catAttrs "wan")
      (lib.groupBy lib.id)
      (lib.filterAttrs (_: references: builtins.length references > 1))
      (lib.mapAttrsToList (
        wan: _:
        lib.nameValuePair "groupDuplicateMember" "wan '${wan}' is referenced by more than one member"
      ))
    ];

  # Missing `table` and `mark` become null, so their validators report
  # them instead of `make` failing on a missing attribute.
  mergeWithDefaults = input: {
    name = input.name or null;
    members = input.members or [ ];
    strategy = input.strategy or defaults.strategy;
    table = input.table or null;
    mark = input.mark or null;
  };

  collectErrors =
    fields: parsedMembers:
    let
      hasCleanMembers =
        builtins.isList fields.members && fields.members != [ ] && parsedMembers.errors == [ ];
    in
    validateName fields.name
    ++ validateMembers fields.members parsedMembers
    ++ validateStrategy fields.strategy
    ++ validateTable fields.table
    ++ validateMark fields.mark
    ++ lib.optionals hasCleanMembers (findDuplicateMembers parsedMembers.parsed);

  /*
    Validate Group input without throwing, reporting every violation,
    including each member's, in one message.

    `input`: an attrset with `name`, `members` (inputs for
    `member.tryMake`), `table`, `mark`, and optional `strategy`
    (default "primary-backup").

    Returns a `tryResult` whose value is the group value. Error kinds:

      groupInvalidName     — name is not a valid identifier
      groupNoMembers       — members is empty
      groupInvalidMember   — members is not a list, or
                             `member.tryMake` rejected a member
      groupDuplicateMember — several members reference the same WAN
      groupInvalidStrategy — strategy not in `validStrategies`
      groupInvalidTable    — table missing or outside [1000, 32767]
      groupInvalidMark     — mark missing or outside [1000, 32767]
  */
  tryMake =
    input:
    let
      fields = mergeWithDefaults input;
      parsedMembers = parseMembers (if builtins.isList fields.members then fields.members else [ ]);
      errors = collectErrors fields parsedMembers;
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
    List the WANs a Group references, in member order.

    `group`: a group value.

    Returns a list of WAN names.
  */
  wans = group: lib.catAttrs "wan" group.members;

  /*
    Serialize a Group for the daemon-config JSON.

    `group`: a group value.

    Returns the JSON-shaped attrset embedded by `config.render`, with
    members nested as attrsets.
  */
  toJSONValue = group: {
    inherit (group)
      mark
      name
      strategy
      table
      ;
    members = map member.toJSONValue group.members;
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
