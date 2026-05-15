/*
  wanwatch.wan — WAN value type.

  A WAN is an egress interface plus a Probe describing how to test
  it. The atomic monitored unit — Groups compose WANs as Members,
  but the WAN itself is independent of any Group.

  Fields (required unless marked optional):

    name          — identifier, `[a-zA-Z][a-zA-Z0-9-]*`. Typically
                    derived from the NixOS module's `wans.<name>`
                    attribute key.
    interface     — Linux interface name; validated via libnet's
                    kernel-`dev_valid_name` parity check.
    pointToPoint  — optional bool, default false. When true the
                    daemon installs scope-link default routes
                    (PPP / WireGuard / GRE / tun); when false the
                    daemon discovers the gateway via netlink from
                    the main routing table at runtime.
    probe         — attrset passed to `probe.make` (this module
                    owns the construction; users don't pre-build).

  The families the WAN handles are derived from `probe.targets`:
  `targets.v4` non-empty means the WAN serves v4, `targets.v6`
  non-empty means it serves v6. There is no separate family
  declaration.

  ===== make =====

  Input:  attrset of fields (see above)
  Output: wan value with a constructed probe value embedded.
  Throws: aggregated error string if validation fails.

  ===== tryMake =====

  Same as `make` but returns the `tryResult` shape instead of
  throwing.

  Error kinds:

    wanInvalidName            — name missing / not a valid identifier
    wanInvalidInterface       — interface name fails kernel parity check
    wanInvalidPointToPoint    — pointToPoint set but not a bool
    wanInvalidProbe           — embedded probe.make rejected the config

  ===== Accessors =====

  `families` (derived from `probe.targets` — same as
  `probe.families` of the embedded probe).

  ===== Serialization =====

  `toJSONValue` is the canonical attrset form embedded in the
  daemon-config JSON.
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
    isValidName
    ;
  formatErrors = internal.primitives.formatErrors "wan.make";
  probe = internal.probe;

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

  validatePointToPoint =
    ptp:
    check "wanInvalidPointToPoint" (builtins.isBool ptp)
      "pointToPoint must be a bool; got ${builtins.typeOf ptp}";

  validateProbeResult =
    probeResult:
    check "wanInvalidProbe" probeResult.success (if probeResult.success then "" else probeResult.error);

  # ===== Aggregated validation + construction =====

  prepareInput = user: {
    name = user.name or null;
    interface = user.interface or null;
    pointToPoint = user.pointToPoint or false;
    probeInput = user.probe or { };
  };

  collectErrors =
    cfg: probeResult:
    validateName cfg.name
    ++ validateInterface cfg.interface
    ++ validatePointToPoint cfg.pointToPoint
    ++ validateProbeResult probeResult;

  buildValue = cfg: probeResult: {
    name = cfg.name;
    interface = cfg.interface;
    pointToPoint = cfg.pointToPoint;
    probe = probeResult.value;
  };

  tryMake =
    user:
    let
      cfg = prepareInput user;
      probeResult = probe.tryMake cfg.probeInput;
      errors = collectErrors cfg probeResult;
    in
    if errors == [ ] then tryOk (buildValue cfg probeResult) else tryErr (formatErrors errors);

  make =
    user:
    let
      r = tryMake user;
    in
    if r.success then r.value else builtins.throw r.error;

  # ===== Derived accessors =====
  #
  # `families` is `probe.families` of the embedded probe — the
  # WAN serves whatever families its probe targets cover.
  families = w: probe.families w.probe;

  # ===== Serialization =====

  toJSONValue = w: {
    inherit (w) name interface pointToPoint;
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
