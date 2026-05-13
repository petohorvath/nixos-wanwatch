/*
  wanwatch.probe — Probe configuration value type.

  A Probe describes *how* a WAN is tested. It is the configuration,
  not the result — Samples and Windows live in the daemon. A Probe
  value carries:

    method             — probing protocol; v1 supports "icmp" only
    targets            — non-empty list of libnet.ip values
    intervalMs         — milliseconds between probe cycles
    timeoutMs          — per-probe timeout
    windowSize         — number of samples in the sliding window
    thresholds         — loss / RTT thresholds in both directions
    hysteresis         — consecutive-cycle counters in both directions
    familyHealthPolicy — "all" | "any" — how per-family Health
                         combines into per-WAN Health (PLAN §5.4)

  Required field: `targets` (at least one entry). Everything else
  has a default; see `defaults` below.

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

    probeNoTargets               — empty `targets`
    probeInvalidTarget           — target string not a valid IP
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
  (derived: set of family flags present in targets, e.g.
  `{ v4 = true; v6 = true; }`).

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
    tagError
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

  validateTargets =
    targets:
    if !(builtins.isList targets) then
      check "probeInvalidTarget" false "targets must be a list of IP strings"
    else if targets == [ ] then
      check "probeNoTargets" false "targets must be non-empty"
    else
      builtins.map (tagError "probeInvalidTarget") (parseTargets targets).errors;

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

  mergeWithDefaults = user: {
    method = user.method or defaults.method;
    targets = user.targets or [ ];
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
    method = cfg.method;
    targets = parsedTargets;
    intervalMs = cfg.intervalMs;
    timeoutMs = cfg.timeoutMs;
    windowSize = cfg.windowSize;
    thresholds = cfg.thresholds;
    hysteresis = cfg.hysteresis;
    familyHealthPolicy = cfg.familyHealthPolicy;
  };

  tryMake =
    user:
    let
      cfg = mergeWithDefaults user;
      errors = collectErrors cfg;
    in
    if errors == [ ] then
      tryOk (buildValue cfg (parseTargets cfg.targets).parsed)
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
    v4 = builtins.any libnet.ip.isIpv4 p.targets;
    v6 = builtins.any libnet.ip.isIpv6 p.targets;
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
    targets = builtins.map libnet.ip.toString p.targets;
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
