/*
  WAN value type, exposed as `wanwatch.wan`. A WAN is an egress
  interface plus the Probe that tests it; Groups reference WANs as
  Members, but a WAN does not depend on any Group. A WAN value
  carries:

    name         — wanwatch identifier, usually the `wans.<name>` key
    interface    — Linux interface name, checked against the kernel's
                   `dev_valid_name` rules by libnet
    pointToPoint — when true, the daemon installs scope-link default
                   routes (PPP, WireGuard, GRE, tun); when false, it
                   discovers the gateway from the main routing table
    probe        — the probe value built from the `probe` input

  The WAN serves the families its probe targets cover; there is no
  separate family declaration.
*/
{
  libnet,
  internal,
}:
let
  inherit (internal) probe;
  inherit (internal.primitives)
    check
    isValidName
    tryErr
    tryOk
    ;

  formatErrors = internal.primitives.formatErrors "wan.make";

  validateName =
    name:
    check "wanInvalidName" (isValidName name)
      "name must match [a-zA-Z][a-zA-Z0-9-]*; got ${builtins.toJSON name}";

  validateInterface =
    interface:
    let
      result =
        if builtins.isString interface then
          libnet.interfaceName.tryParse interface
        else
          {
            success = false;
            error = "interface must be a string; got ${builtins.typeOf interface}";
          };
    in
    check "wanInvalidInterface" result.success (if result.success then "" else result.error);

  validatePointToPoint =
    pointToPoint:
    check "wanInvalidPointToPoint" (builtins.isBool pointToPoint)
      "pointToPoint must be a bool; got ${builtins.typeOf pointToPoint}";

  validateProbeResult =
    probeResult:
    check "wanInvalidProbe" probeResult.success (if probeResult.success then "" else probeResult.error);

  prepareInput = input: {
    name = input.name or null;
    interface = input.interface or null;
    pointToPoint = input.pointToPoint or false;
    probeInput = input.probe or { };
  };

  collectErrors =
    fields: probeResult:
    validateName fields.name
    ++ validateInterface fields.interface
    ++ validatePointToPoint fields.pointToPoint
    ++ validateProbeResult probeResult;

  /*
    Validate WAN input without throwing, reporting every violation,
    including the embedded probe's, in one message.

    `input`: an attrset with `name`, `interface`, `probe` (the input
    for `probe.tryMake`), and optional `pointToPoint` (default false).

    Returns a `tryResult` whose value is the WAN value. Error kinds:

      wanInvalidName         — name missing or not a valid identifier
      wanInvalidInterface    — interface fails the kernel name check
      wanInvalidPointToPoint — pointToPoint is not a bool
      wanInvalidProbe        — `probe.tryMake` rejected the probe input
  */
  tryMake =
    input:
    let
      fields = prepareInput input;
      probeResult = probe.tryMake fields.probeInput;
      errors = collectErrors fields probeResult;
    in
    if errors == [ ] then
      tryOk {
        inherit (fields) interface name pointToPoint;
        probe = probeResult.value;
      }
    else
      tryErr (formatErrors errors);

  /*
    Construct a WAN value, failing evaluation on invalid input.

    `input`: the attrset accepted by `tryMake`.

    Returns the WAN value with its probe value embedded. Throws the
    aggregated `tryMake` error message when validation fails.
  */
  make =
    input:
    let
      result = tryMake input;
    in
    if result.success then result.value else throw result.error;

  /*
    Report which address families a WAN serves; these are the families
    its probe targets cover.

    `wan`: a WAN value.

    Returns `{ v4 = <bool>; v6 = <bool>; }`.
  */
  families = wan: probe.families wan.probe;

  /*
    Serialize a WAN for the daemon-config JSON.

    `wan`: a WAN value.

    Returns the JSON-shaped attrset embedded by `config.render`.
  */
  toJSONValue = wan: {
    inherit (wan) interface name pointToPoint;
    probe = probe.toJSONValue wan.probe;
  };
in
{
  inherit
    families
    make
    toJSONValue
    tryMake
    ;
}
