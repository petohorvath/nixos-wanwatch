/*
  Probe value type, exposed as `wanwatch.probe`. A Probe is the
  configuration that describes how a WAN is tested; Samples and
  Windows live in the daemon. A probe value carries:

    method             — probing protocol; only "icmp"
    targets            — { v4; v6; } lists of libnet IP values; at least
                         one family is non-empty
    intervalMs         — milliseconds between probe cycles
    timeoutMs          — timeout for each Sample in milliseconds
    windowSize         — number of Samples in the sliding window
    thresholds         — loss and RTT thresholds in both directions
    hysteresis         — consecutive-cycle counters in both directions
    familyHealthPolicy — "all" or "any": how per-family Health combines
                         into WAN Health (docs/adr/0005-family-health-policy-defaults-to-all.md)

  Only `targets` is required; `defaults` supplies the other fields.
*/
{
  lib,
  libnet,
  internal,
}:
let
  inherit (internal.primitives)
    check
    checkUnknownFields
    isPositiveInt
    partitionTry
    requireAttrset
    tryErr
    tryOk
    ;

  formatErrors = internal.primitives.formatErrors "probe.make";

  defaults = {
    method = "icmp";
    intervalMs = 500;
    timeoutMs = 1000;
    windowSize = 10;
    thresholds = {
      lossPctDown = 30;
      lossPctUp = 10;
      rttMsDown = 500;
      rttMsUp = 250;
    };
    hysteresis = {
      consecutiveDown = 3;
      consecutiveUp = 5;
    };
    familyHealthPolicy = "all";
  };

  # `types/probe.nix` derives its enum option types from these lists,
  # so the option types and the validators accept the same values.
  validMethods = [ "icmp" ];
  validFamilyHealthPolicies = [
    "all"
    "any"
  ];

  knownFields = [
    "method"
    "targets"
    "intervalMs"
    "timeoutMs"
    "windowSize"
    "thresholds"
    "hysteresis"
    "familyHealthPolicy"
  ];

  # The nested `targets`, `thresholds`, and `hysteresis` attrsets also
  # report unknown fields as `probeUnknownField`.
  checkUnknownProbeFields = checkUnknownFields "probeUnknownField";

  isPercentage = value: builtins.isInt value && value >= 0 && value <= 100;

  parseTargets = partitionTry libnet.ip.tryParse;

  validateMethod =
    method:
    check "probeInvalidMethod" (builtins.elem method validMethods)
      "method must be one of ${builtins.toJSON validMethods}; got ${builtins.toJSON method}";

  # Checks one family's target list for parseability, family match, and
  # duplicates. Targets compare in canonical form, so differently
  # written forms of one address are duplicates; each duplicated address
  # is reported once. `checkParsed` adds the family's own checks on the
  # parsed targets. An empty list passes; `validateTargets` requires a
  # non-empty family.
  validateTargetFamily =
    family: isFamily: checkParsed: targets:
    if !(builtins.isList targets) then
      check "probeInvalidTarget" false "targets.${family} must be a list"
    else
      let
        parsedTargets = parseTargets targets;
        parseErrors = map (lib.nameValuePair "probeInvalidTarget") parsedTargets.errors;
        familyMismatches = lib.pipe parsedTargets.parsed [
          (builtins.filter (ip: !(isFamily ip)))
          (map (
            ip:
            lib.nameValuePair "probeTargetFamilyMismatch" "${libnet.ip.toString ip} in targets.${family} is not a ${family} address"
          ))
        ];
        canonicalTargets = map libnet.ip.toString parsedTargets.parsed;
        duplicates = lib.pipe canonicalTargets [
          (builtins.filter (target: lib.count (other: other == target) canonicalTargets > 1))
          lib.unique
          (map (
            target:
            lib.nameValuePair "probeDuplicateTarget" "${target} appears more than once in targets.${family}"
          ))
        ];
      in
      parseErrors ++ familyMismatches ++ duplicates ++ checkParsed parsedTargets.parsed;

  # The daemon reads an IPv4-mapped address (::ffff:0:0/96) as IPv4, so
  # one in `targets.v6` would fail its Family check at runtime. Takes
  # the parsed `targets.v6`.
  validateNoIpv4Mapped =
    parsed:
    lib.pipe parsed [
      (builtins.filter (ip: libnet.ip.isIpv6 ip && libnet.ipv6.isIpv4Mapped ip))
      (map (
        ip:
        lib.nameValuePair "probeTargetIpv4Mapped" "${libnet.ip.toString ip} in targets.v6 is an IPv4-mapped address; list the IPv4 address in targets.v4"
      ))
    ];

  validateTargets =
    targets:
    if !(builtins.isAttrs targets) then
      check "probeInvalidTarget" false "targets must be { v4 = [...]; v6 = [...]; }"
    else
      let
        v4 = targets.v4 or [ ];
        v6 = targets.v6 or [ ];
        hasTargets = !(builtins.isList v4 && builtins.isList v6 && v4 == [ ] && v6 == [ ]);
      in
      checkUnknownProbeFields "targets" [ "v4" "v6" ] targets
      ++ check "probeNoTargets" hasTargets "at least one of targets.v4 or targets.v6 must be non-empty"
      ++ validateTargetFamily "v4" libnet.ip.isIpv4 (_: [ ]) v4
      ++ validateTargetFamily "v6" libnet.ip.isIpv6 validateNoIpv4Mapped v6;

  validateInterval =
    interval:
    check "probeNonPositiveInterval" (isPositiveInt interval)
      "intervalMs must be a positive integer; got ${builtins.toJSON interval}";

  validateTimeout =
    timeout:
    check "probeNonPositiveTimeout" (isPositiveInt timeout)
      "timeoutMs must be a positive integer; got ${builtins.toJSON timeout}";

  validateWindowSize =
    windowSize:
    check "probeNonPositiveWindowSize" (isPositiveInt windowSize)
      "windowSize must be a positive integer; got ${builtins.toJSON windowSize}";

  validateLossThresholds =
    thresholds:
    let
      down = thresholds.lossPctDown or null;
      up = thresholds.lossPctUp or null;
      isDownValid = isPercentage down;
      isUpValid = isPercentage up;
    in
    check "probeLossPctOutOfRange" isDownValid
      "thresholds.lossPctDown must be an int in [0,100]; got ${builtins.toJSON down}"
    ++
      check "probeLossPctOutOfRange" isUpValid
        "thresholds.lossPctUp must be an int in [0,100]; got ${builtins.toJSON up}"
    ++
      check "probeLossThresholdsInverted" (!(isDownValid && isUpValid && up >= down))
        "thresholds.lossPctUp (${builtins.toJSON up}) must be strictly less than thresholds.lossPctDown (${builtins.toJSON down}); recovery threshold must sit below failure threshold to avoid flapping";

  validateRttThresholds =
    thresholds:
    let
      down = thresholds.rttMsDown or null;
      up = thresholds.rttMsUp or null;
      isDownValid = isPositiveInt down;
      isUpValid = isPositiveInt up;
    in
    check "probeNonPositiveRtt" isDownValid
      "thresholds.rttMsDown must be a positive integer; got ${builtins.toJSON down}"
    ++
      check "probeNonPositiveRtt" isUpValid
        "thresholds.rttMsUp must be a positive integer; got ${builtins.toJSON up}"
    ++
      check "probeRttThresholdsInverted" (!(isDownValid && isUpValid && up >= down))
        "thresholds.rttMsUp (${builtins.toJSON up}) must be strictly less than thresholds.rttMsDown (${builtins.toJSON down}); recovery threshold must sit below failure threshold to avoid flapping";

  validateThresholds =
    thresholds:
    if !(builtins.isAttrs thresholds) then
      check "probeInvalidThresholds" false "thresholds must be an attrset"
    else
      checkUnknownProbeFields "thresholds" (builtins.attrNames defaults.thresholds) thresholds
      ++ validateLossThresholds thresholds
      ++ validateRttThresholds thresholds;

  validateHysteresis =
    hysteresis:
    let
      down = hysteresis.consecutiveDown or null;
      up = hysteresis.consecutiveUp or null;
    in
    if !(builtins.isAttrs hysteresis) then
      check "probeInvalidHysteresis" false "hysteresis must be an attrset"
    else
      checkUnknownProbeFields "hysteresis" (builtins.attrNames defaults.hysteresis) hysteresis
      ++
        check "probeNonPositiveHysteresis" (isPositiveInt down)
          "hysteresis.consecutiveDown must be a positive integer; got ${builtins.toJSON down}"
      ++
        check "probeNonPositiveHysteresis" (isPositiveInt up)
          "hysteresis.consecutiveUp must be a positive integer; got ${builtins.toJSON up}";

  validateFamilyHealthPolicy =
    policy:
    check "probeInvalidFamilyHealthPolicy" (builtins.elem policy validFamilyHealthPolicies)
      "familyHealthPolicy must be one of ${builtins.toJSON validFamilyHealthPolicies}; got ${builtins.toJSON policy}";

  # Non-attrset `targets`, `thresholds`, and `hysteresis` pass through
  # unchanged, and attrsets keep unknown fields, so their validators
  # can report them.
  mergeWithDefaults =
    input:
    let
      targets = input.targets or { };
      thresholds = input.thresholds or { };
      hysteresis = input.hysteresis or { };
    in
    {
      method = input.method or defaults.method;
      targets =
        if builtins.isAttrs targets then
          {
            v4 = [ ];
            v6 = [ ];
          }
          // targets
        else
          targets;
      intervalMs = input.intervalMs or defaults.intervalMs;
      timeoutMs = input.timeoutMs or defaults.timeoutMs;
      windowSize = input.windowSize or defaults.windowSize;
      thresholds = if builtins.isAttrs thresholds then defaults.thresholds // thresholds else thresholds;
      hysteresis = if builtins.isAttrs hysteresis then defaults.hysteresis // hysteresis else hysteresis;
      familyHealthPolicy = input.familyHealthPolicy or defaults.familyHealthPolicy;
    };

  collectErrors =
    input: fields:
    checkUnknownProbeFields "probe" knownFields input
    ++ validateMethod fields.method
    ++ validateTargets fields.targets
    ++ validateInterval fields.intervalMs
    ++ validateTimeout fields.timeoutMs
    ++ validateWindowSize fields.windowSize
    ++ validateThresholds fields.thresholds
    ++ validateHysteresis fields.hysteresis
    ++ validateFamilyHealthPolicy fields.familyHealthPolicy;

  /*
    Validate probe input without throwing, so callers such as
    `wan.tryMake` can fold probe errors into their own report. Every
    violation is reported in one message rather than only the first.

    `input`: an attrset with any subset of the probe fields; missing
    fields take `defaults`.

    Returns a `tryResult` whose value is the probe value, with each
    target parsed into a libnet IP value. Error kinds:

      probeNotAnAttrset              — input is not an attribute set
      probeUnknownField              — a field outside the Probe fields,
                                       or in `targets`, `thresholds`,
                                       or `hysteresis`
      probeNoTargets                 — `targets.v4` and `targets.v6` empty
      probeInvalidTarget             — malformed targets attrset or list,
                                       or a target that is not an IP
      probeTargetFamilyMismatch      — v4 literal in `targets.v6`, or v6
                                       literal in `targets.v4`
      probeTargetIpv4Mapped          — IPv4-mapped literal
                                       (::ffff:0:0/96) in `targets.v6`
      probeDuplicateTarget           — an address listed more than once
                                       in one bucket, compared in
                                       canonical form
      probeInvalidThresholds         — `thresholds` is not an attrset
      probeInvalidHysteresis         — `hysteresis` is not an attrset
      probeInvalidMethod             — method not in `validMethods`
      probeNonPositiveInterval       — intervalMs ≤ 0
      probeNonPositiveTimeout        — timeoutMs ≤ 0
      probeNonPositiveWindowSize     — windowSize ≤ 0
      probeLossPctOutOfRange         — lossPct{Up,Down} outside [0, 100]
      probeLossThresholdsInverted    — lossPctUp ≥ lossPctDown
      probeNonPositiveRtt            — rttMs{Up,Down} ≤ 0
      probeRttThresholdsInverted     — rttMsUp ≥ rttMsDown
      probeNonPositiveHysteresis     — a hysteresis counter ≤ 0
      probeInvalidFamilyHealthPolicy — familyHealthPolicy not in
                                       `validFamilyHealthPolicies`

    Each recovery threshold must sit strictly below its failure
    threshold; otherwise a marginal WAN flaps on every Sample.
  */
  tryMake = requireAttrset "probeNotAnAttrset" formatErrors (
    input:
    let
      fields = mergeWithDefaults input;
      errors = collectErrors input fields;
    in
    if errors == [ ] then
      tryOk (
        fields
        // {
          targets = {
            v4 = (parseTargets fields.targets.v4).parsed;
            v6 = (parseTargets fields.targets.v6).parsed;
          };
        }
      )
    else
      tryErr (formatErrors errors)
  );

  /*
    Construct a probe value, failing evaluation on invalid input.

    `input`: an attrset with any subset of the probe fields; missing
    fields take `defaults`.

    Returns the probe value. Throws the aggregated `tryMake` error
    message when validation fails.
  */
  make =
    input:
    let
      result = tryMake input;
    in
    if result.success then result.value else throw result.error;

  /*
    Report which address families a probe covers, so `wan.make` can
    derive the families a WAN serves (docs/adr/0004-runtime-gateway-discovery.md).

    `probe`: a probe value.

    Returns `{ v4 = <bool>; v6 = <bool>; }`, true for each family with
    at least one target.
  */
  families = probe: {
    v4 = probe.targets.v4 != [ ];
    v6 = probe.targets.v6 != [ ];
  };

  /*
    Serialize a probe for the daemon-config JSON. Targets use libnet's
    canonical text form, so the rendered config is byte-stable.

    `probe`: a probe value.

    Returns the JSON-shaped attrset embedded by `wan.toJSONValue`.
  */
  toJSONValue = probe: {
    inherit (probe)
      familyHealthPolicy
      hysteresis
      intervalMs
      method
      thresholds
      timeoutMs
      windowSize
      ;
    targets = {
      v4 = map libnet.ip.toString probe.targets.v4;
      v6 = map libnet.ip.toString probe.targets.v6;
    };
  };
in
{
  inherit
    defaults
    families
    make
    toJSONValue
    tryMake
    validFamilyHealthPolicies
    validMethods
    ;
}
