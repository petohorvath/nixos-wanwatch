/*
  Global settings value type, exposed as `wanwatch.global`. Global
  settings configure the daemon process rather than one WAN or Group.
  A global value carries:

    logLevel      — minimum level the daemon logs, from `validLogLevels`
    hookTimeoutMs — deadline for each Hook in milliseconds

  Every field has a default in `defaults`, so `make { }` is valid. The
  daemon's paths are not settings; `config.paths` fixes them.
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

  defaults = {
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
    ++ validateLogLevel fields.logLevel
    ++ validateHookTimeout fields.hookTimeoutMs;

  /*
    Validate global settings without throwing, reporting every
    violation in one message. The checks cover everything the daemon
    rejects in these settings, so a bad setting fails evaluation
    instead of the service start. A path field is unknown, because
    `config.paths` fixes the paths.

    `input`: an attrset with any subset of the global fields; missing
    fields take `defaults`.

    Returns a `tryResult` whose value is the global value. Error kinds:

      globalUnknownField           — a field outside the global fields
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

    Returns the JSON-shaped attrset that `config.render` embeds, adding
    `config.paths`.
  */
  toJSONValue = global: {
    inherit (global) hookTimeoutMs logLevel;
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
