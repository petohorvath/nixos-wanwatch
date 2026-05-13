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
  family flags with non-null gateways, e.g.
  `{ v4 = true; v6 = false; }`), `probe` (the embedded probe
  value), `targets` (forwarded through the probe's accessor).

  ===== Serialization =====

  `toJSONValue` is the canonical attrset form embedded in the
  daemon-config JSON. Nested submodules embed via their own
  `toJSONValue` rather than as nested JSON strings.
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
    parseOptional
    isValidName
    ;
  formatErrors = internal.primitives.formatErrors "wan.make";
  probe = internal.probe;

  parseV4 = parseOptional libnet.ipv4.tryParse;
  parseV6 = parseOptional libnet.ipv6.tryParse;

  # ===== Field-level validators =====

  validateName =
    name:
    check "wanInvalidName" (isValidName name)
      "name must match [a-zA-Z][a-zA-Z0-9-]*; got ${builtins.toJSON name}";

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
    check "wanInvalidInterface" r.success (if r.success then "" else r.error);

  validateGatewayResults =
    v4Result: v6Result:
    check "wanInvalidGatewayV4" v4Result.success (if v4Result.success then "" else v4Result.error)
    ++ check "wanInvalidGatewayV6" v6Result.success (if v6Result.success then "" else v6Result.error);

  validateProbeResult =
    probeResult:
    check "wanInvalidProbe" probeResult.success (if probeResult.success then "" else probeResult.error);

  validateNoGateways =
    gwV4: gwV6:
    check "wanNoGateways" (
      gwV4 != null || gwV6 != null
    ) "at least one of gateways.v4 / gateways.v6 must be set";

  # Family-coupling: cross-check gateways vs probe.targets. Called
  # only when gateways AND probe parsed cleanly — otherwise the
  # rules would report spurious follow-on errors over already-known-bad input.
  validateFamilyCoupling =
    gwV4: gwV6: probeValue:
    let
      fams = probe.families probeValue;
    in
    check "wanV4GatewayNoTargets" (
      gwV4 == null || fams.v4
    ) "gateways.v4 is set but probe.targets contains no IPv4 target"
    ++ check "wanV6GatewayNoTargets" (
      gwV6 == null || fams.v6
    ) "gateways.v6 is set but probe.targets contains no IPv6 target"
    ++ check "wanV4TargetNoGateway" (
      !fams.v4 || gwV4 != null
    ) "probe.targets contains an IPv4 target but gateways.v4 is null"
    ++ check "wanV6TargetNoGateway" (
      !fams.v6 || gwV6 != null
    ) "probe.targets contains an IPv6 target but gateways.v6 is null";

  # ===== Aggregated validation + construction =====
  #
  # Parses gateways and probe once, threads the results through both
  # error collection and value construction. The previous shape
  # parsed them twice — once in collectErrors, once in buildValue.

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
    cfg: v4Result: v6Result: probeResult:
    let
      gwV4 = if v4Result.success then v4Result.value else null;
      gwV6 = if v6Result.success then v6Result.value else null;

      structuralErrs =
        validateName cfg.name
        ++ validateInterface cfg.interface
        ++ validateGatewayResults v4Result v6Result
        ++ validateProbeResult probeResult;

      noGwErrs = validateNoGateways gwV4 gwV6;

      familyErrs =
        if
          v4Result.success && v6Result.success && probeResult.success && (gwV4 != null || gwV6 != null)
        then
          validateFamilyCoupling gwV4 gwV6 probeResult.value
        else
          [ ];
    in
    structuralErrs ++ noGwErrs ++ familyErrs;

  buildValue = cfg: v4Result: v6Result: probeResult: {
    name = cfg.name;
    interface = cfg.interface;
    gateways = {
      v4 = v4Result.value;
      v6 = v6Result.value;
    };
    probe = probeResult.value;
  };

  tryMake =
    user:
    let
      cfg = prepareInput user;
      v4Result = parseV4 cfg.gwInputV4;
      v6Result = parseV6 cfg.gwInputV6;
      probeResult = probe.tryMake cfg.probeInput;
      errors = collectErrors cfg v4Result v6Result probeResult;
    in
    if errors == [ ] then
      tryOk (buildValue cfg v4Result v6Result probeResult)
    else
      tryErr (formatErrors errors);

  make =
    user:
    let
      r = tryMake user;
    in
    if r.success then r.value else builtins.throw r.error;

  # ===== Derived accessors =====
  #
  # `families` reflects which gateway families the WAN has *declared*.
  # Distinct from `probe.families`, which reflects what's *probed*.
  # The family-coupling invariant guarantees these agree at construction.
  families = w: {
    v4 = w.gateways.v4 != null;
    v6 = w.gateways.v6 != null;
  };

  # ===== Serialization =====

  toJSONValue = w: {
    inherit (w) name interface;
    gateways = {
      v4 = if w.gateways.v4 == null then null else libnet.ipv4.toString w.gateways.v4;
      v6 = if w.gateways.v6 == null then null else libnet.ipv6.toString w.gateways.v6;
    };
    probe = probe.toJSONValue w.probe;
  };
in
{
  inherit
    make
    tryMake
    toJSONValue
    families
    ;
}
