/*
  Option types for global settings, exported through `wanwatch.types`:

    globalLogLevel — enum of `internal.global.validLogLevels`
    global         — the complete global-settings submodule

  Defaults come from `internal.global`, so `global = { }` evaluates to
  what `global.make { }` builds.
*/
{
  lib,
  primitives,
  internal,
}:
let
  inherit (internal.global) defaults;
  inherit (lib) mkOption types;

  globalLogLevel = types.enum internal.global.validLogLevels;

  global = types.submodule {
    options = {
      logLevel = mkOption {
        type = globalLogLevel;
        default = defaults.logLevel;
        description = ''
          Minimum slog level emitted by the daemon. The `-log-level`
          flag overrides this at runtime.
        '';
      };
      hookTimeoutMs = mkOption {
        type = primitives.positiveInt;
        default = defaults.hookTimeoutMs;
        description = ''
          Deadline in milliseconds for each Hook. When it expires,
          the daemon sends SIGKILL to the Hook's process group and
          reports a timeout.
        '';
      };
    };
  };
in
{
  inherit global globalLogLevel;
}
