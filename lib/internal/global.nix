/*
  Global settings value type, exposed as `wanwatch.global`. Global
  settings configure the daemon process rather than one WAN or Group.
  A global value carries:

    statePath     — file the daemon atomically rewrites with its State
    hooksDir      — root of the Hook tree, `<hooksDir>/{up,down,switch}.d/`
    metricsSocket — Unix socket that serves Prometheus metrics
    logLevel      — minimum level the daemon logs, from `validLogLevels`
    hookTimeoutMs — deadline for each Hook in milliseconds

  Every field has a default in `defaults`, so `make { }` is valid.
*/
{ internal }:
let
  inherit (internal.primitives)
    check
    checkUnknownFields
    isPositiveInt
    tryErr
    tryOk
    ;

  formatErrors = internal.primitives.formatErrors "global.make";

  # The default statePath and metricsSocket live in /run/wanwatch, the
  # RuntimeDirectory that `nixos/module.nix` creates.
  defaults = {
    statePath = "/run/wanwatch/state.json";
    hooksDir = "/etc/wanwatch/hooks";
    metricsSocket = "/run/wanwatch/metrics.sock";
    logLevel = "info";
    hookTimeoutMs = 5000;
  };

  # `types/global.nix` derives its enum option type from this list. The
  # daemon accepts the same levels.
  validLogLevels = [
    "debug"
    "info"
    "warn"
    "error"
  ];

  knownFields = builtins.attrNames defaults;

  # Matches `lib.types.nonEmptyStr`, the paths' option type: at least
  # one character other than a space, tab, or newline.
  isNonEmptyString = value: builtins.isString value && builtins.match "[ \t\n]*" value == null;

  validatePath =
    kind: field: value:
    check kind (isNonEmptyString value)
      "${field} must be a non-empty string; got ${builtins.toJSON value}";

  validateLogLevel =
    logLevel:
    check "globalInvalidLogLevel" (builtins.elem logLevel validLogLevels)
      "logLevel must be one of ${builtins.toJSON validLogLevels}; got ${builtins.toJSON logLevel}";

  validateHookTimeout =
    hookTimeoutMs:
    check "globalNonPositiveHookTimeout" (isPositiveInt hookTimeoutMs)
      "hookTimeoutMs must be a positive integer; got ${builtins.toJSON hookTimeoutMs}";

  mergeWithDefaults = input: builtins.mapAttrs (field: default: input.${field} or default) defaults;

  collectErrors =
    input: fields:
    checkUnknownFields "globalUnknownField" "global" knownFields input
    ++ validatePath "globalInvalidStatePath" "statePath" fields.statePath
    ++ validatePath "globalInvalidHooksDir" "hooksDir" fields.hooksDir
    ++ validatePath "globalInvalidMetricsSocket" "metricsSocket" fields.metricsSocket
    ++ validateLogLevel fields.logLevel
    ++ validateHookTimeout fields.hookTimeoutMs;

  /*
    Validate global settings without throwing, reporting every
    violation in one message. The checks cover everything the daemon
    rejects in `global`, so a bad setting fails evaluation instead of
    the service start. They are stricter on paths, which must contain
    a character other than whitespace, as `lib.types.nonEmptyStr`
    requires.

    `input`: an attrset with any subset of the global fields; missing
    fields take `defaults`.

    Returns a `tryResult` whose value is the global value. Error kinds:

      globalUnknownField           — a field outside the global fields
      globalInvalidStatePath       — statePath is not a non-empty string
      globalInvalidHooksDir        — hooksDir is not a non-empty string
      globalInvalidMetricsSocket   — metricsSocket is not a non-empty
                                     string
      globalInvalidLogLevel        — logLevel not in `validLogLevels`
      globalNonPositiveHookTimeout — hookTimeoutMs ≤ 0
  */
  tryMake =
    input:
    let
      fields = mergeWithDefaults input;
      errors = collectErrors input fields;
    in
    if errors == [ ] then tryOk fields else tryErr (formatErrors errors);

  /*
    Construct a global value, failing evaluation on invalid input.

    `input`: an attrset with any subset of the global fields; missing
    fields take `defaults`.

    Returns the global value. Throws the aggregated `tryMake` error
    message when validation fails.
  */
  make =
    input:
    let
      result = tryMake input;
    in
    if result.success then result.value else throw result.error;

  /*
    Serialize global settings for the daemon-config JSON.

    `global`: a global value.

    Returns the JSON-shaped attrset embedded by `config.render`.
  */
  toJSONValue = global: {
    inherit (global)
      hookTimeoutMs
      hooksDir
      logLevel
      metricsSocket
      statePath
      ;
  };
in
{
  inherit
    defaults
    make
    toJSONValue
    tryMake
    validLogLevels
    ;
}
