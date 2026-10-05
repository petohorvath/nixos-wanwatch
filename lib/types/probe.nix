/*
  Option types for Probe configuration, exported through
  `wanwatch.types`:

    probeMethod             — enum of `internal.probe.validMethods`
    probeTarget             — IPv4 or IPv6 address string
    probeFamilyHealthPolicy — enum of
                              `internal.probe.validFamilyHealthPolicies`
    probeThresholds         — submodule of loss and RTT thresholds
    probeHysteresis         — submodule of consecutive-cycle counters
    probe                   — the complete Probe submodule

  Enums and defaults come from `internal.probe`, so a config with only
  `targets` evaluates to what `probe.make` would build. Targets stay
  strings here; `probe.make` parses them into libnet IP values.
*/
{
  lib,
  libnet,
  primitives,
  internal,
}:
let
  inherit (internal.probe) defaults;
  inherit (lib) mkOption types;

  probeMethod = types.enum internal.probe.validMethods;
  probeFamilyHealthPolicy = types.enum internal.probe.validFamilyHealthPolicies;
  probeTarget = libnet.types.ip;

  probeThresholds = types.submodule {
    options = {
      lossPctDown = mkOption {
        type = primitives.pctInt;
        default = defaults.thresholds.lossPctDown;
        description = ''
          Loss percentage above which a WAN becomes unhealthy, compared
          with the loss ratio over the sliding window.
        '';
      };
      lossPctUp = mkOption {
        type = primitives.pctInt;
        default = defaults.thresholds.lossPctUp;
        description = ''
          Loss percentage at or below which a WAN becomes healthy
          again. Must be strictly below `lossPctDown`; equal or
          inverted thresholds would flap at the boundary.
        '';
      };
      rttMsDown = mkOption {
        type = primitives.positiveInt;
        default = defaults.thresholds.rttMsDown;
        description = ''
          Mean RTT in milliseconds above which a WAN becomes unhealthy.
        '';
      };
      rttMsUp = mkOption {
        type = primitives.positiveInt;
        default = defaults.thresholds.rttMsUp;
        description = ''
          Mean RTT in milliseconds at or below which a WAN becomes
          healthy again. Must be strictly below `rttMsDown`.
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
          Consecutive good cycles required to mark a WAN healthy again.
        '';
      };
    };
  };

  mkTargetFamilyOption =
    family: otherFamily:
    mkOption {
      type = types.listOf probeTarget;
      default = [ ];
      description = ''
        ${family} probe targets. Each must be an ${family} address;
        an ${otherFamily} address fails with
        `probeTargetFamilyMismatch`.
      '';
    };

  probe = types.submodule {
    options = {
      method = mkOption {
        type = probeMethod;
        default = defaults.method;
        description = "Probing protocol. Only `\"icmp\"` is supported.";
      };
      targets = mkOption {
        type = types.submodule {
          options = {
            v4 = mkTargetFamilyOption "IPv4" "IPv6";
            v6 = mkTargetFamilyOption "IPv6" "IPv4";
          };
        };
        default = { };
        example = {
          v4 = [ "1.1.1.1" ];
          v6 = [ "2606:4700:4700::1111" ];
        };
        description = ''
          Addresses to probe, by family. At least one of `v4` and `v6`
          must be non-empty; the WAN serves the families listed here
          (PLAN §5.4).
        '';
      };
      intervalMs = mkOption {
        type = primitives.positiveInt;
        default = defaults.intervalMs;
        description = ''
          Milliseconds between probe cycles. Probes may overlap, so
          `timeoutMs` is independent of `intervalMs`.
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
          Number of Samples in the sliding window used to compute loss,
          mean RTT, and jitter.
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
          How per-family Health combines into WAN Health. With `"all"`
          (the default), the WAN is healthy when every configured family
          is healthy; with `"any"`, when at least one is (PLAN §5.4).
        '';
      };
    };
  };
in
{
  inherit
    probe
    probeFamilyHealthPolicy
    probeHysteresis
    probeMethod
    probeTarget
    probeThresholds
    ;
}
