/*
  wanwatch.probe — Probe configuration value type.

  A Probe describes *how* a WAN is tested. It is the configuration,
  not the result — Samples and Windows live in the daemon. A Probe
  value carries:

    method             — probing protocol; v1 supports "icmp" only
    targets            — { v4, v6 }: per-family lists of libnet.ip
                         values; at least one family non-empty
    intervalMs         — milliseconds between probe cycles
    timeoutMs          — per-probe timeout
    windowSize         — number of samples in the sliding window
    thresholds         — loss / RTT thresholds in both directions
    hysteresis         — consecutive-cycle counters in both directions
    familyHealthPolicy — "all" | "any" — how per-family Health
                         combines into per-WAN Health (PLAN §5.4)

  Required field: `targets` with at least one of `v4`/`v6`
  populated. Everything else has a default; see `defaults` below.

  ===== make =====

  Input:  attrset of fields (any subset; missing fields take defaults)
  Output: probe value with each target parsed into a libnet.ip value
  Throws: aggregated error string if any field fails validation.

  ===== tryMake =====

  Same as `make` but returns the `tryResult` shape instead of
  throwing. Errors are aggregated nftzones-style — every violation
  in a single input attrset is reported in one error message rather
  than fail-on-first — so users see the whole problem set at once.

  Validation rules and error kinds:

    probeNoTargets               — both `targets.v4` and
                                   `targets.v6` empty
    probeInvalidTarget           — target string not a valid IP
    probeTargetFamilyMismatch    — v4 literal in `targets.v6`, or
                                   v6 literal in `targets.v4`
    probeInvalidMethod           — method ∉ {"icmp"}
    probeNonPositiveInterval     — intervalMs ≤ 0
    probeNonPositiveTimeout      — timeoutMs ≤ 0
    probeNonPositiveWindow       — windowSize ≤ 0
    probeLossPctOutOfRange       — lossPct{Up,Down} ∉ [0, 100]
    probeLossThresholdsInverted  — lossPctUp ≥ lossPctDown
    probeNonPositiveRTT          — rttMs{Up,Down} ≤ 0
    probeRTTThresholdsInverted   — rttMsUp ≥ rttMsDown
    probeNonPositiveHysteresis   — hysteresis counter ≤ 0
    probeInvalidFamilyPolicy     — familyHealthPolicy ∉ {"all", "any"}

  Rationale for inverted-threshold rules: the recovery threshold
  must be strictly below the failure threshold; otherwise a
  marginal probe oscillates between "healthy" and "unhealthy" on
  every sample. Pre-emptive rejection beats runtime flapping.

  ===== Accessors =====

  `method`, `targets`, `intervalMs`, `timeoutMs`, `windowSize`,
  `thresholds`, `hysteresis`, `familyHealthPolicy`, `families`
  (derived: `{ v4 = targets.v4 != []; v6 = targets.v6 != []; }`).

  ===== Serialization =====

  `toJSONValue` returns the canonical attrset form embedded in
  `lib/internal/config.nix`'s daemon-config render. Targets are
  stringified back to their canonical text form.
*/
{
  lib,
  libnet,
  internal,
}:
let
  inherit (internal.primitives)
    tryOk
    tryErr
    check
    partitionTry
    isPositiveInt
    ;
  formatErrors = internal.primitives.formatErrors "probe.make";

  # ===== Defaults =====

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

  # ===== Validation helpers =====

  isPct = x: builtins.isInt x && x >= 0 && x <= 100;

  # Closed-set enums. Exposed in the module return so `types/probe.nix`
  # can derive its enum types from the same lists — single source of
  # truth on the Nix side. Drift between internal and types would
  # otherwise let the type system accept a value the validator
  # rejects, or vice versa.
  validMethods = [ "icmp" ];
  validFamilyHealthPolicies = [
    "all"
    "any"
  ];

  # libnet.ip.tryParse speaks the standard tryResult shape; the
  # generic partitionTry handles the partition.
  parseTargets = partitionTry libnet.ip.tryParse;

  # ===== Field-level validators =====

  validateMethod =
    method:
    check "probeInvalidMethod" (builtins.elem method validMethods)
      "method must be one of ${builtins.toJSON validMethods}; got ${builtins.toJSON method}";

  # validateTargetBucket parses one per-family target list and
  # checks both parseability and family-match. An empty bucket is
  # fine here — the cross-bucket "at least one non-empty" check
  # lives in validateTargets.
  validateTargetBucket =
    fam: familyPredicate: xs:
    let
      parsed = parseTargets xs;
      parseErrors = builtins.map (lib.nameValuePair "probeInvalidTarget") parsed.errors;
      mismatches = builtins.concatMap (
        ip:
        if familyPredicate ip then
          [ ]
        else
          [
            (lib.nameValuePair "probeTargetFamilyMismatch" "${libnet.ip.toString ip} in targets.${fam} is not a ${fam} address")
          ]
      ) parsed.parsed;
    in
    parseErrors ++ mismatches;

  validateTargets =
    targets:
    if !(builtins.isAttrs targets) then
      check "probeInvalidTarget" false "targets must be { v4 = [...]; v6 = [...]; }"
    else
      let
        v4 = targets.v4 or [ ];
        v6 = targets.v6 or [ ];
        nonEmpty =
          if v4 == [ ] && v6 == [ ] then
            check "probeNoTargets" false "at least one of targets.v4 or targets.v6 must be non-empty"
          else
            [ ];
      in
      nonEmpty
      ++ validateTargetBucket "v4" libnet.ip.isIpv4 v4
      ++ validateTargetBucket "v6" libnet.ip.isIpv6 v6;

  validateInterval =
    interval:
    check "probeNonPositiveInterval" (isPositiveInt interval)
      "intervalMs must be a positive integer; got ${builtins.toJSON interval}";

  validateTimeout =
    timeout:
    check "probeNonPositiveTimeout" (isPositiveInt timeout)
      "timeoutMs must be a positive integer; got ${builtins.toJSON timeout}";

  validateWindowSize =
    n:
    check "probeNonPositiveWindow" (isPositiveInt n)
      "windowSize must be a positive integer; got ${builtins.toJSON n}";

  validateLossThresholds =
    t:
    let
      down = t.lossPctDown or null;
      up = t.lossPctUp or null;
      downValid = isPct down;
      upValid = isPct up;
    in
    check "probeLossPctOutOfRange" downValid
      "thresholds.lossPctDown must be an int in [0,100]; got ${builtins.toJSON down}"
    ++
      check "probeLossPctOutOfRange" upValid
        "thresholds.lossPctUp must be an int in [0,100]; got ${builtins.toJSON up}"
    ++
      check "probeLossThresholdsInverted" (!(downValid && upValid && up >= down))
        "thresholds.lossPctUp (${builtins.toJSON up}) must be strictly less than thresholds.lossPctDown (${builtins.toJSON down}); recovery threshold must sit below failure threshold to avoid flapping";

  validateRttThresholds =
    t:
    let
      down = t.rttMsDown or null;
      up = t.rttMsUp or null;
      downValid = isPositiveInt down;
      upValid = isPositiveInt up;
    in
    check "probeNonPositiveRTT" downValid
      "thresholds.rttMsDown must be a positive integer; got ${builtins.toJSON down}"
    ++
      check "probeNonPositiveRTT" upValid
        "thresholds.rttMsUp must be a positive integer; got ${builtins.toJSON up}"
    ++
      check "probeRTTThresholdsInverted" (!(downValid && upValid && up >= down))
        "thresholds.rttMsUp (${builtins.toJSON up}) must be strictly less than thresholds.rttMsDown (${builtins.toJSON down}); recovery threshold must sit below failure threshold to avoid flapping";

  validateHysteresis =
    h:
    check "probeNonPositiveHysteresis" (isPositiveInt (h.consecutiveDown or null))
      "hysteresis.consecutiveDown must be a positive integer; got ${
        builtins.toJSON (h.consecutiveDown or null)
      }"
    ++
      check "probeNonPositiveHysteresis" (isPositiveInt (h.consecutiveUp or null))
        "hysteresis.consecutiveUp must be a positive integer; got ${
          builtins.toJSON (h.consecutiveUp or null)
        }";

  validateFamilyHealthPolicy =
    policy:
    check "probeInvalidFamilyPolicy" (builtins.elem policy validFamilyHealthPolicies)
      "familyHealthPolicy must be one of ${builtins.toJSON validFamilyHealthPolicies}; got ${builtins.toJSON policy}";

  # ===== Aggregated validation + construction =====

  mergeWithDefaults =
    user:
    let
      t = user.targets or { };
    in
    {
      method = user.method or defaults.method;
      targets = {
        v4 = t.v4 or [ ];
        v6 = t.v6 or [ ];
      };
      intervalMs = user.intervalMs or defaults.intervalMs;
      timeoutMs = user.timeoutMs or defaults.timeoutMs;
      windowSize = user.windowSize or defaults.windowSize;
      thresholds = defaults.thresholds // (user.thresholds or { });
      hysteresis = defaults.hysteresis // (user.hysteresis or { });
      familyHealthPolicy = user.familyHealthPolicy or defaults.familyHealthPolicy;
    };

  collectErrors =
    cfg:
    validateMethod cfg.method
    ++ validateTargets cfg.targets
    ++ validateInterval cfg.intervalMs
    ++ validateTimeout cfg.timeoutMs
    ++ validateWindowSize cfg.windowSize
    ++ validateLossThresholds cfg.thresholds
    ++ validateRttThresholds cfg.thresholds
    ++ validateHysteresis cfg.hysteresis
    ++ validateFamilyHealthPolicy cfg.familyHealthPolicy;

  buildValue = cfg: parsedTargets: {
    inherit (cfg)
      method
      intervalMs
      timeoutMs
      windowSize
      thresholds
      hysteresis
      familyHealthPolicy
      ;
    targets = parsedTargets;
  };

  tryMake =
    user:
    let
      cfg = mergeWithDefaults user;
      errors = collectErrors cfg;
    in
    if errors == [ ] then
      tryOk (
        buildValue cfg {
          v4 = (parseTargets cfg.targets.v4).parsed;
          v6 = (parseTargets cfg.targets.v6).parsed;
        }
      )
    else
      tryErr (formatErrors errors);

  make =
    user:
    let
      r = tryMake user;
    in
    if r.success then r.value else builtins.throw r.error;

  # ===== Derived accessors =====

  # `families` returns an attrset {v4 = bool; v6 = bool;} reflecting
  # whether the probe's targets cover each family. Used by `wan.make`
  # to enforce the family-coupling invariant (PLAN §5.4).
  families = p: {
    v4 = p.targets.v4 != [ ];
    v6 = p.targets.v6 != [ ];
  };

  # ===== Serialization =====

  # The JSON-shape attrset embedded by `wan.toJSONValue` and by
  # `config.render`. Strings are libnet's canonical form so the
  # rendered config is byte-stable across builds.
  toJSONValue = p: {
    inherit (p)
      method
      intervalMs
      timeoutMs
      windowSize
      thresholds
      hysteresis
      familyHealthPolicy
      ;
    targets = {
      v4 = builtins.map libnet.ip.toString p.targets.v4;
      v6 = builtins.map libnet.ip.toString p.targets.v6;
    };
  };
in
{
  inherit
    make
    tryMake
    toJSONValue
    families
    ;
  # Exposed for tests / introspection / module-option defaults.
  inherit defaults;
  # Single-source enums: `types/probe.nix` derives `probeMethod` and
  # `probeFamilyHealthPolicy` from these so the option type and the
  # validator agree by construction.
  inherit validMethods validFamilyHealthPolicies;
}
