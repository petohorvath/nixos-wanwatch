/*
  types/probe — NixOS option types for the Probe value.

  Exports (flattened into `wanwatch.types.<name>` by
  `lib/types/default.nix`):

    probeMethod              — enum [ "icmp" ]
    probeTarget              — libnet.types.ip (v4 or v6 IP string)
    probeFamilyHealthPolicy  — enum [ "all" "any" ]
    probeThresholds          — submodule { lossPctDown; lossPctUp;
                                            rttMsDown; rttMsUp; }
    probeHysteresis          — submodule { consecutiveDown;
                                            consecutiveUp; }
    probe                    — top-level submodule

  Defaults mirror `internal.probe.defaults` exactly so a config
  consisting of only `targets = { v4 = [ … ]; }` evaluates to the
  same shape the value-type's `make` would produce.

  Probe targets are stored as strings (libnet convention — option
  values stay as strings after merge; downstream code calls
  `libnet.ip.parse` when it needs structural access). The
  `internal.probe.make` validator parses these strings into libnet
  ip values at construction time.
*/
{
  lib,
  libnet,
  primitives,
  internal,
}:
let
  inherit (lib) types mkOption;

  # Single source of truth: enums and scalar defaults come from
  # `internal.probe`'s registries / defaults attrset, so the option
  # type and the validator agree by construction. Adding a method,
  # family-health policy, or changing a default no longer requires
  # touching two files in lockstep.
  probeMethod = types.enum internal.probe.validMethods;
  probeFamilyHealthPolicy = types.enum internal.probe.validFamilyHealthPolicies;
  inherit (internal.probe) defaults;

  probeTarget = libnet.types.ip;

  probeThresholds = types.submodule {
    options = {
      lossPctDown = mkOption {
        type = primitives.pctInt;
        default = defaults.thresholds.lossPctDown;
        description = ''
          Loss-percentage threshold above which a WAN flips to
          unhealthy. Compared against the rolling sliding-window
          loss ratio.
        '';
      };
      lossPctUp = mkOption {
        type = primitives.pctInt;
        default = defaults.thresholds.lossPctUp;
        description = ''
          Loss-percentage at or below which a WAN flips back to
          healthy. Must be strictly below `lossPctDown` — the
          value-type validator (`internal.probe.tryMake`) rejects
          equal or inverted thresholds because they oscillate at
          the boundary.
        '';
      };
      rttMsDown = mkOption {
        type = primitives.positiveInt;
        default = defaults.thresholds.rttMsDown;
        description = ''
          Mean RTT (milliseconds) above which a WAN flips to
          unhealthy.
        '';
      };
      rttMsUp = mkOption {
        type = primitives.positiveInt;
        default = defaults.thresholds.rttMsUp;
        description = ''
          Mean RTT (milliseconds) at or below which a WAN flips
          back to healthy. Must be strictly below `rttMsDown`.
        '';
      };
    };
  };

  probeHysteresis = types.submodule {
    options = {
      consecutiveDown = mkOption {
        type = primitives.positiveInt;
        default = defaults.hysteresis.consecutiveDown;
        description = ''
          Consecutive bad cycles required to mark a WAN unhealthy.
        '';
      };
      consecutiveUp = mkOption {
        type = primitives.positiveInt;
        default = defaults.hysteresis.consecutiveUp;
        description = ''
          Consecutive good cycles required to mark a WAN healthy
          again.
        '';
      };
    };
  };

  probe = types.submodule {
    options = {
      method = mkOption {
        type = probeMethod;
        default = defaults.method;
        description = ''
          Probing protocol. v1 supports `"icmp"` only; v2 will add
          TCP / HTTP / DNS once the daemon's probe layer grows.
        '';
      };
      targets = mkOption {
        type = types.submodule {
          options = {
            v4 = mkOption {
              type = types.listOf probeTarget;
              default = [ ];
              description = ''
                IPv4 probe targets. Each item must be a v4 IP literal —
                a v6 literal here is rejected by `internal.probe.make`
                with `probeTargetFamilyMismatch`.
              '';
            };
            v6 = mkOption {
              type = types.listOf probeTarget;
              default = [ ];
              description = ''
                IPv6 probe targets. Each item must be a v6 IP literal —
                a v4 literal here is rejected by `internal.probe.make`
                with `probeTargetFamilyMismatch`.
              '';
            };
          };
        };
        default = { };
        example = {
          v4 = [ "1.1.1.1" ];
          v6 = [ "2606:4700:4700::1111" ];
        };
        description = ''
          IP targets to probe, partitioned by family. At least one of
          `v4` / `v6` must be non-empty. Each family the surrounding
          WAN has a gateway for must have at least one matching
          target — see PLAN §5.4.
        '';
      };
      intervalMs = mkOption {
        type = primitives.positiveInt;
        default = defaults.intervalMs;
        description = ''
          Milliseconds between probe cycles. Multiple probes may be
          in flight simultaneously (dpinger-style) — `timeoutMs` is
          independent of `intervalMs`.
        '';
      };
      timeoutMs = mkOption {
        type = primitives.positiveInt;
        default = defaults.timeoutMs;
        description = ''
          Per-probe timeout in milliseconds. May exceed `intervalMs`.
        '';
      };
      windowSize = mkOption {
        type = primitives.positiveInt;
        default = defaults.windowSize;
        description = ''
          Number of samples in the sliding window used to compute
          loss / mean RTT / jitter.
        '';
      };
      thresholds = mkOption {
        type = probeThresholds;
        default = { };
        description = "Loss and RTT thresholds in both directions.";
      };
      hysteresis = mkOption {
        type = probeHysteresis;
        default = { };
        description = "Consecutive-cycle counters in both directions.";
      };
      familyHealthPolicy = mkOption {
        type = probeFamilyHealthPolicy;
        default = defaults.familyHealthPolicy;
        description = ''
          How per-family Health combines into per-WAN Health.
          `"all"` (default) — WAN healthy iff every configured
          family is healthy. `"any"` — WAN healthy if any
          configured family is healthy. See PLAN §5.4.
        '';
      };
    };
  };
in
{
  inherit
    probeMethod
    probeTarget
    probeFamilyHealthPolicy
    probeThresholds
    probeHysteresis
    probe
    ;
}
