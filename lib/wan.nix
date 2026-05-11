/*
  wanwatch.wan — WAN value type.

  A WAN is an egress interface with one or two gateways and a
  Probe describing how to test it. It is the atomic monitored unit —
  Groups compose WANs as Members, but the WAN itself is independent
  of any Group.

  Fields (required unless marked optional):

    name       — identifier, `[a-zA-Z][a-zA-Z0-9-]*`. Typically
                 derived from the NixOS module's `wans.<name>` attr
                 key; supplied explicitly when called directly.
    interface  — Linux interface name; validated via libnet's
                 kernel-`dev_valid_name` parity check.
    gateways   — `{ v4 = ipv4 | null; v6 = ipv6 | null; }`. At
                 least one of v4/v6 must be set.
    probe      — attrset passed to `probe.make` (this module owns
                 the construction; users don't pre-construct).

  ===== make =====

  Input:  attrset of fields (see above)
  Output: tagged wan value with parsed gateways and a constructed
          probe value embedded.
  Throws: aggregated error string if validation fails.

  ===== tryMake =====

  Same as `make` but returns the `tryResult` shape instead of
  throwing. Aggregates errors from name, interface, gateways, the
  embedded probe, AND the family-coupling invariant (PLAN §5.4)
  in a single error message.

  Error kinds:

    wanInvalidName             — name missing / not a valid identifier
    wanInvalidInterface        — interface name fails kernel parity check
    wanInvalidGatewayV4        — gateways.v4 is set but doesn't parse as IPv4
    wanInvalidGatewayV6        — gateways.v6 is set but doesn't parse as IPv6
    wanInvalidProbe            — embedded probe.make rejected the config
                                 (the probe's own error kinds are propagated)
    wanNoGateways              — both gateways.v4 and gateways.v6 are null
    wanV4GatewayNoTargets      — v4 gateway set; no v4 IP in probe.targets
    wanV6GatewayNoTargets      — v6 gateway set; no v6 IP in probe.targets
    wanV4TargetNoGateway       — v4 target in probe; no v4 gateway
    wanV6TargetNoGateway       — v6 target in probe; no v6 gateway

  Validation order: structural errors first (name, interface,
  gateway parsing, probe construction). Family-coupling rules are
  only evaluated when both `gateways` and `probe` parsed cleanly —
  evaluating them against partially-broken data would generate
  spurious errors.

  ===== Accessors =====

  `name`, `interface`, `gatewayV4` (libnet ipv4 value or null),
  `gatewayV6` (libnet ipv6 value or null), `families` (set of
  family strings with non-null gateways, e.g.
  `{ v4 = true; v6 = false; }`), `probe` (the embedded probe
  value), `targets` (forwarded from the probe).

  ===== Equality, ordering, toJSON =====

  Same skeleton as probe: `eq` is structural; `compare` derives
  from canonical JSON; `lt`/`le`/`gt`/`ge`/`min`/`max` derive from
  `compare`; `toJSON` produces a daemon-consumable string.
*/
{
  libnet,
  internal,
  probe,
}:
let
  inherit (internal.types) tryOk tryErr;

  # ===== Validation helpers =====

  nameValuePair = name: value: { inherit name value; };

  isValidName = s: builtins.isString s && builtins.match "[a-zA-Z][a-zA-Z0-9-]*" s != null;

  validateName =
    name:
    if isValidName name then
      [ ]
    else
      [
        (nameValuePair "wanInvalidName" "name must match [a-zA-Z][a-zA-Z0-9-]*; got ${builtins.toJSON name}")
      ];

  # Interface name validation delegates to libnet — kernel parity.
  validateInterface =
    interface:
    let
      r =
        if builtins.isString interface then
          libnet.interface.tryParseName interface
        else
          {
            success = false;
            error = "interface must be a string; got ${builtins.typeOf interface}";
          };
    in
    if r.success then [ ] else [ (nameValuePair "wanInvalidInterface" r.error) ];

  parseGatewayV4 =
    addr:
    if addr == null then
      {
        ok = true;
        value = null;
      }
    else
      let
        r = libnet.ipv4.tryParse addr;
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

  parseGatewayV6 =
    addr:
    if addr == null then
      {
        ok = true;
        value = null;
      }
    else
      let
        r = libnet.ipv6.tryParse addr;
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

  validateGatewayParseErrors =
    v4Result: v6Result:
    (if v4Result.ok then [ ] else [ (nameValuePair "wanInvalidGatewayV4" v4Result.error) ])
    ++ (if v6Result.ok then [ ] else [ (nameValuePair "wanInvalidGatewayV6" v6Result.error) ]);

  validateNoGateways =
    gwV4: gwV6:
    if gwV4 == null && gwV6 == null then
      [ (nameValuePair "wanNoGateways" "at least one of gateways.v4 / gateways.v6 must be set") ]
    else
      [ ];

  # Family-coupling: cross-check gateways vs probe.targets. Called
  # only when both gateways and probe parsed cleanly.
  validateFamilyCoupling =
    gwV4: gwV6: probeValue:
    let
      fams = probe.families probeValue;
      hasV4Target = fams.v4;
      hasV6Target = fams.v6;
    in
    (
      if gwV4 != null && !hasV4Target then
        [
          (nameValuePair "wanV4GatewayNoTargets" "gateways.v4 is set but probe.targets contains no IPv4 target")
        ]
      else
        [ ]
    )
    ++ (
      if gwV6 != null && !hasV6Target then
        [
          (nameValuePair "wanV6GatewayNoTargets" "gateways.v6 is set but probe.targets contains no IPv6 target")
        ]
      else
        [ ]
    )
    ++ (
      if hasV4Target && gwV4 == null then
        [
          (nameValuePair "wanV4TargetNoGateway" "probe.targets contains an IPv4 target but gateways.v4 is null")
        ]
      else
        [ ]
    )
    ++ (
      if hasV6Target && gwV6 == null then
        [
          (nameValuePair "wanV6TargetNoGateway" "probe.targets contains an IPv6 target but gateways.v6 is null")
        ]
      else
        [ ]
    );

  # ===== Aggregated validation + construction =====

  prepareInput =
    user:
    let
      gw = user.gateways or { };
    in
    {
      name = user.name or null;
      interface = user.interface or null;
      gwInputV4 = gw.v4 or null;
      gwInputV6 = gw.v6 or null;
      probeInput = user.probe or { };
    };

  collectErrors =
    cfg:
    let
      v4Result = parseGatewayV4 cfg.gwInputV4;
      v6Result = parseGatewayV6 cfg.gwInputV6;
      probeResult = probe.tryMake cfg.probeInput;
      gatewayParseErrs = validateGatewayParseErrors v4Result v6Result;
      probeErr =
        if probeResult.success then
          [ ]
        else
          [
            (nameValuePair "wanInvalidProbe" probeResult.error)
          ];

      structuralErrs =
        validateName cfg.name ++ validateInterface cfg.interface ++ gatewayParseErrs ++ probeErr;

      gwV4 = if v4Result.ok then v4Result.value else null;
      gwV6 = if v6Result.ok then v6Result.value else null;

      noGwErrs = validateNoGateways gwV4 gwV6;

      # Family-coupling: only meaningful when gateways parsed AND probe parsed.
      familyErrs =
        if v4Result.ok && v6Result.ok && probeResult.success && (gwV4 != null || gwV6 != null) then
          validateFamilyCoupling gwV4 gwV6 probeResult.value
        else
          [ ];
    in
    structuralErrs ++ noGwErrs ++ familyErrs;

  formatErrors =
    errors:
    "wan.make: " + builtins.concatStringsSep "; " (builtins.map (e: "[${e.name}] ${e.value}") errors);

  buildValue =
    cfg:
    let
      v4 = (parseGatewayV4 cfg.gwInputV4).value;
      v6 = (parseGatewayV6 cfg.gwInputV6).value;
      probeValue = (probe.tryMake cfg.probeInput).value;
    in
    {
      _type = "wan";
      name = cfg.name;
      interface = cfg.interface;
      gateways = {
        inherit v4 v6;
      };
      probe = probeValue;
    };

  tryMake =
    user:
    let
      cfg = prepareInput user;
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

  isWan = internal.types.isWan;

  # ===== Accessors =====

  name = w: w.name;
  interface = w: w.interface;
  gatewayV4 = w: w.gateways.v4;
  gatewayV6 = w: w.gateways.v6;
  probeOf = w: w.probe;
  targets = w: w.probe.targets;

  # `families` reflects which gateway families the WAN has *declared*.
  # Distinct from `probe.families`, which reflects what's *probed*.
  # The family-coupling invariant guarantees these agree at construction.
  families = w: {
    v4 = w.gateways.v4 != null;
    v6 = w.gateways.v6 != null;
  };

  # ===== Serialization =====

  toJSONValue = w: {
    _type = "wan";
    inherit (w) name interface;
    gateways = {
      v4 = if w.gateways.v4 == null then null else libnet.ipv4.toString w.gateways.v4;
      v6 = if w.gateways.v6 == null then null else libnet.ipv6.toString w.gateways.v6;
    };
    # Embed the probe as a nested attrset (not a nested JSON string).
    # `probe.toJSONValue` exposes the pre-serialization shape exactly
    # for this case.
    probe = probe.toJSONValue w.probe;
  };

  toJSON = w: builtins.toJSON (toJSONValue w);

  # ===== Equality and ordering =====

  eq = a: b: a == b;

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
    isWan
    toJSON
    ;
  inherit
    name
    interface
    gatewayV4
    gatewayV6
    families
    ;
  # Renamed to avoid colliding with the `probe` module argument.
  probe = probeOf;
  inherit targets;
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
}
