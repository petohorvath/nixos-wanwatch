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
  consisting of only `targets = [ … ]` evaluates to the same shape
  the value-type's `make` would produce.

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

  # Single source of truth: enums come from `internal.probe`'s
  # registries so the option type and the validator agree by
  # construction. Without this, adding a method (or family-health
  # policy) would have to touch two files in lockstep.
  probeMethod = types.enum internal.probe.validMethods;
  probeFamilyHealthPolicy = types.enum internal.probe.validFamilyHealthPolicies;

  probeTarget = libnet.types.ip;

  probeThresholds = types.submodule {
    options = {
      lossPctDown = mkOption {
        type = primitives.pctInt;
        default = 30;
        description = ''
          Loss-percentage threshold above which a WAN flips to
          unhealthy. Compared against the rolling sliding-window
          loss ratio.
        '';
      };
      lossPctUp = mkOption {
        type = primitives.pctInt;
        default = 10;
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
        default = 500;
        description = ''
          Mean RTT (milliseconds) above which a WAN flips to
          unhealthy.
        '';
      };
      rttMsUp = mkOption {
        type = primitives.positiveInt;
        default = 250;
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
        default = 3;
        description = ''
          Consecutive bad cycles required to mark a WAN unhealthy.
        '';
      };
      consecutiveUp = mkOption {
        type = primitives.positiveInt;
        default = 5;
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
        default = "icmp";
        description = ''
          Probing protocol. v1 supports `"icmp"` only; v2 will add
          TCP / HTTP / DNS once the daemon's probe layer grows.
        '';
      };
      targets = mkOption {
        type = types.listOf probeTarget;
        example = [
          "1.1.1.1"
          "2606:4700:4700::1111"
        ];
        description = ''
          IP targets to probe. Family is detected from each address
          and dispatched to the matching probe socket (ICMP for v4,
          ICMPv6 for v6). At least one target is required, and each
          family the surrounding WAN has a gateway for must have at
          least one matching target — see PLAN §5.4.
        '';
      };
      intervalMs = mkOption {
        type = primitives.positiveInt;
        default = 500;
        description = ''
          Milliseconds between probe cycles. Multiple probes may be
          in flight simultaneously (dpinger-style) — `timeoutMs` is
          independent of `intervalMs`.
        '';
      };
      timeoutMs = mkOption {
        type = primitives.positiveInt;
        default = 1000;
        description = ''
          Per-probe timeout in milliseconds. May exceed `intervalMs`.
        '';
      };
      windowSize = mkOption {
        type = primitives.positiveInt;
        default = 10;
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
        default = "all";
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
