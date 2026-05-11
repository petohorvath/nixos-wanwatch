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
  Output: tagged probe value `{ _type = "probe"; ... }` with each
          target parsed into a libnet.ip value
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
  (derived: set of family strings present in targets, e.g.
  `{ v4 = true; v6 = true; }`).

  ===== Equality and ordering =====

  `eq` is structural attrset equality. `compare` is a total order
  derived from the canonical JSON form — there is no natural
  ordering on Probes, so we pick one that's stable, deterministic,
  and round-trippable. `lt`/`le`/`gt`/`ge`/`min`/`max` derive from
  `compare`.

  ===== toJSON =====

  Returns a JSON *string* (per PLAN §5.1 contract) suitable for the
  daemon-config file. Targets are stringified back to their canonical
  text form. Keys are sorted alphabetically by `builtins.toJSON`,
  which makes the output content-addressable.
*/
{ libnet, internal }:
let
  inherit (internal.types) tryOk tryErr;

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

  isPositiveInt = x: builtins.isInt x && x > 0;
  isPct = x: builtins.isInt x && x >= 0 && x <= 100;

  validMethods = [ "icmp" ];
  validFamilyHealthPolicies = [
    "all"
    "any"
  ];

  elemOf = list: x: builtins.elem x list;

  nameValuePair = name: value: { inherit name value; };

  # Parse one user-supplied target string into a libnet.ip value.
  # Returns { ok = true; value; } | { ok = false; error; }.
  parseTarget =
    s:
    let
      r = libnet.ip.tryParse s;
    in
    if r.success then
      {
        ok = true;
        value = r.value;
      }
    else
      {
        ok = false;
        error = r.error;
      };

  # Parse every target string; produces { parsed = [<ip values>]; errors = [string]; }.
  parseTargets =
    targets:
    let
      results = builtins.map parseTarget targets;
      parsed = builtins.map (r: r.value) (builtins.filter (r: r.ok) results);
      errors = builtins.map (r: r.error) (builtins.filter (r: !r.ok) results);
    in
    {
      inherit parsed errors;
    };

  # ===== Field-level validators =====

  validateMethod =
    method:
    if elemOf validMethods method then
      [ ]
    else
      [
        (nameValuePair "probeInvalidMethod" "method must be one of ${builtins.toJSON validMethods}; got ${builtins.toJSON method}")
      ];

  validateTargets =
    targets:
    if !(builtins.isList targets) then
      [ (nameValuePair "probeInvalidTarget" "targets must be a list of IP strings") ]
    else if targets == [ ] then
      [ (nameValuePair "probeNoTargets" "targets must be non-empty") ]
    else
      let
        r = parseTargets targets;
      in
      builtins.map (e: nameValuePair "probeInvalidTarget" e) r.errors;

  validateInterval =
    interval:
    if isPositiveInt interval then
      [ ]
    else
      [
        (nameValuePair "probeNonPositiveInterval" "intervalMs must be a positive integer; got ${builtins.toJSON interval}")
      ];

  validateTimeout =
    timeout:
    if isPositiveInt timeout then
      [ ]
    else
      [
        (nameValuePair "probeNonPositiveTimeout" "timeoutMs must be a positive integer; got ${builtins.toJSON timeout}")
      ];

  validateWindowSize =
    n:
    if isPositiveInt n then
      [ ]
    else
      [
        (nameValuePair "probeNonPositiveWindow" "windowSize must be a positive integer; got ${builtins.toJSON n}")
      ];

  validateLossThresholds =
    t:
    let
      downErr =
        if isPct (t.lossPctDown or null) then
          [ ]
        else
          [
            (nameValuePair "probeLossPctOutOfRange" "thresholds.lossPctDown must be an int in [0,100]; got ${
              builtins.toJSON (t.lossPctDown or null)
            }")
          ];
      upErr =
        if isPct (t.lossPctUp or null) then
          [ ]
        else
          [
            (nameValuePair "probeLossPctOutOfRange" "thresholds.lossPctUp must be an int in [0,100]; got ${
              builtins.toJSON (t.lossPctUp or null)
            }")
          ];
      orderErr =
        if isPct (t.lossPctDown or null) && isPct (t.lossPctUp or null) && t.lossPctUp >= t.lossPctDown then
          [
            (nameValuePair "probeLossThresholdsInverted" "thresholds.lossPctUp (${builtins.toJSON t.lossPctUp}) must be strictly less than thresholds.lossPctDown (${builtins.toJSON t.lossPctDown}); recovery threshold must sit below failure threshold to avoid flapping")
          ]
        else
          [ ];
    in
    downErr ++ upErr ++ orderErr;

  validateRttThresholds =
    t:
    let
      downErr =
        if isPositiveInt (t.rttMsDown or null) then
          [ ]
        else
          [
            (nameValuePair "probeNonPositiveRTT" "thresholds.rttMsDown must be a positive integer; got ${
              builtins.toJSON (t.rttMsDown or null)
            }")
          ];
      upErr =
        if isPositiveInt (t.rttMsUp or null) then
          [ ]
        else
          [
            (nameValuePair "probeNonPositiveRTT" "thresholds.rttMsUp must be a positive integer; got ${
              builtins.toJSON (t.rttMsUp or null)
            }")
          ];
      orderErr =
        if
          isPositiveInt (t.rttMsDown or null) && isPositiveInt (t.rttMsUp or null) && t.rttMsUp >= t.rttMsDown
        then
          [
            (nameValuePair "probeRTTThresholdsInverted" "thresholds.rttMsUp (${builtins.toJSON t.rttMsUp}) must be strictly less than thresholds.rttMsDown (${builtins.toJSON t.rttMsDown}); recovery threshold must sit below failure threshold to avoid flapping")
          ]
        else
          [ ];
    in
    downErr ++ upErr ++ orderErr;

  validateHysteresis =
    h:
    let
      down =
        if isPositiveInt (h.consecutiveDown or null) then
          [ ]
        else
          [
            (nameValuePair "probeNonPositiveHysteresis" "hysteresis.consecutiveDown must be a positive integer; got ${
              builtins.toJSON (h.consecutiveDown or null)
            }")
          ];
      up =
        if isPositiveInt (h.consecutiveUp or null) then
          [ ]
        else
          [
            (nameValuePair "probeNonPositiveHysteresis" "hysteresis.consecutiveUp must be a positive integer; got ${
              builtins.toJSON (h.consecutiveUp or null)
            }")
          ];
    in
    down ++ up;

  validateFamilyHealthPolicy =
    policy:
    if elemOf validFamilyHealthPolicies policy then
      [ ]
    else
      [
        (nameValuePair "probeInvalidFamilyPolicy" "familyHealthPolicy must be one of ${builtins.toJSON validFamilyHealthPolicies}; got ${builtins.toJSON policy}")
      ];

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

  formatErrors =
    errors:
    "probe.make: " + builtins.concatStringsSep "; " (builtins.map (e: "[${e.name}] ${e.value}") errors);

  buildValue =
    cfg:
    let
      parsedTargets = (parseTargets cfg.targets).parsed;
    in
    {
      _type = "probe";
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
    if errors == [ ] then tryOk (buildValue cfg) else tryErr (formatErrors errors);

  make =
    user:
    let
      r = tryMake user;
    in
    if r.success then r.value else builtins.throw r.error;

  # ===== Predicates =====

  isProbe = internal.types.isProbe;

  # ===== Accessors =====

  method = p: p.method;
  targets = p: p.targets;
  intervalMs = p: p.intervalMs;
  timeoutMs = p: p.timeoutMs;
  windowSize = p: p.windowSize;
  thresholds = p: p.thresholds;
  hysteresis = p: p.hysteresis;
  familyHealthPolicy = p: p.familyHealthPolicy;

  # `families` returns an attrset {v4 = bool; v6 = bool;} reflecting
  # whether the probe's targets cover each family. Used by `wan.make`
  # to enforce the family-coupling invariant (PLAN §5.4).
  families =
    p:
    let
      hasV4 = builtins.any (t: t._type == "ipv4") p.targets;
      hasV6 = builtins.any (t: t._type == "ipv6") p.targets;
    in
    {
      v4 = hasV4;
      v6 = hasV6;
    };

  # ===== Serialization =====

  toJSONValue = p: {
    inherit (p)
      _type
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

  toJSON = p: builtins.toJSON (toJSONValue p);

  # Exposed for callers that need the JSON-shape attrset before
  # serialization — e.g. `wan.toJSON` embeds the probe as a nested
  # object rather than a nested JSON string.

  # ===== Equality and ordering =====

  eq = a: b: a == b;

  # Total order derived from canonical JSON. Stable, deterministic,
  # round-trippable. There is no natural ordering on Probes.
  compare =
    a: b:
    let
      aj = toJSON a;
      bj = toJSON b;
    in
    if aj < bj then
      -1
    else if aj > bj then
      1
    else
      0;

  lt = a: b: compare a b == -1;
  le = a: b: compare a b <= 0;
  gt = a: b: compare a b == 1;
  ge = a: b: compare a b >= 0;
  min = a: b: if le a b then a else b;
  max = a: b: if ge a b then a else b;
in
{
  inherit
    make
    tryMake
    isProbe
    toJSON
    toJSONValue
    ;
  inherit
    method
    targets
    intervalMs
    timeoutMs
    windowSize
    thresholds
    hysteresis
    familyHealthPolicy
    families
    ;
  inherit
    eq
    compare
    lt
    le
    gt
    ge
    min
    max
    ;
  # Exposed for tests / introspection / module-option defaults.
  inherit defaults;
}
