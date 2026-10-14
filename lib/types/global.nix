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
      statePath = mkOption {
        type = types.nonEmptyStr;
        default = defaults.statePath;
        description = ''
          File the daemon atomically rewrites with its State on every
          Decision. The default lives in the service's
          RuntimeDirectory under `/run`.
        '';
      };
      hooksDir = mkOption {
        type = types.nonEmptyStr;
        default = defaults.hooksDir;
        description = ''
          Root of the hook-script tree. On every Decision the daemon
          runs the scripts in `<hooksDir>/{up,down,switch}.d/`
          (docs/specs/daemon-state.md).
        '';
      };
      metricsSocket = mkOption {
        type = types.nonEmptyStr;
        default = defaults.metricsSocket;
        description = ''
          Unix socket on which the daemon serves Prometheus metrics.
          The socket has mode 0660, so scrapers need membership in
          the daemon's group.
        '';
      };
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
          Deadline in milliseconds for each script under `hooksDir`.
          When it expires, the daemon sends SIGKILL to the hook's
          process group and reports a timeout.
        '';
      };
    };
  };
in
{
  inherit global globalLogLevel;
}
