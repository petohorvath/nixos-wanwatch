/*
  Unit tests for `lib/internal/wan.nix` (exposed as `wanwatch.wan`).

  Coverage discipline per PLAN.md §9.1: every public function exercised
  on positive and negative inputs; every error kind triggered in
  isolation; at least one multi-violation case for aggregated reporting;
  the §5.1 API skeleton (`make` / `tryMake` / `toJSONValue`) exercised.

  Family-coupling invariant (PLAN §5.4) gets its own block of tests:
  each of the five error kinds in isolation plus positive cases for
  every topology (v4-only / v6-only / dual-stack).
*/
{ pkgs, libnet, ... }:
let
  wanwatch = import ../../../lib {
    inherit (pkgs) lib;
    inherit libnet;
  };
  inherit (wanwatch) wan;

  helpers = import ../helpers.nix { inherit pkgs; };
  inherit (helpers) evalThrows errorMatches;
  tryError = helpers.tryError wan;

  # Valid baselines for each topology.

  dualStackInput = {
    name = "primary";
    interface = "eth0";
    gateways = {
      v4 = "192.0.2.1";
      v6 = "2001:db8::1";
    };
    probe = {
      targets = [
        "1.1.1.1"
        "2606:4700:4700::1111"
      ];
    };
  };

  v4OnlyInput = {
    name = "primary";
    interface = "eth0";
    gateways.v4 = "192.0.2.1";
    probe.targets = [ "1.1.1.1" ];
  };

  v6OnlyInput = {
    name = "primary";
    interface = "eth0";
    gateways.v6 = "2001:db8::1";
    probe.targets = [ "2606:4700:4700::1111" ];
  };
in
{
  # ===== Happy path — three topologies =====

  testMakeDualStackReturnsValue = {
    expr = builtins.isAttrs (wan.make dualStackInput);
    expected = true;
  };

  testMakeV4OnlyAccepted = {
    expr = (wan.make v4OnlyInput).gateways.v4 != null;
    expected = true;
  };

  testMakeV6OnlyAccepted = {
    expr = (wan.make v6OnlyInput).gateways.v6 != null;
    expected = true;
  };

  testMakeDualStackPreservesName = {
    expr = (wan.make dualStackInput).name;
    expected = "primary";
  };

  testMakeDualStackPreservesInterface = {
    expr = (wan.make dualStackInput).interface;
    expected = "eth0";
  };

  # ===== Accessors =====

  testGatewayV4Parsed = {
    expr = (wan.make dualStackInput).gateways.v4._type;
    expected = "ipv4";
  };

  testGatewayV6Parsed = {
    expr = (wan.make dualStackInput).gateways.v6._type;
    expected = "ipv6";
  };

  testGatewayV4NullWhenV6Only = {
    expr = (wan.make v6OnlyInput).gateways.v4;
    expected = null;
  };

  testGatewayV6NullWhenV4Only = {
    expr = (wan.make v4OnlyInput).gateways.v6;
    expected = null;
  };

  testFamiliesDualStack = {
    expr = wan.families (wan.make dualStackInput);
    expected = {
      v4 = true;
      v6 = true;
    };
  };

  testFamiliesV4Only = {
    expr = wan.families (wan.make v4OnlyInput);
    expected = {
      v4 = true;
      v6 = false;
    };
  };

  testFamiliesV6Only = {
    expr = wan.families (wan.make v6OnlyInput);
    expected = {
      v4 = false;
      v6 = true;
    };
  };

  testProbeAccessorReturnsProbeValue = {
    expr = builtins.isAttrs (wan.make dualStackInput).probe;
    expected = true;
  };

  testTargetsForwardedFromProbe = {
    expr = builtins.length (wan.make dualStackInput).probe.targets;
    expected = 2;
  };

  # ===== Error: wanInvalidName =====

  testRejectsMissingName = {
    expr = errorMatches "wanInvalidName" (tryError (removeAttrs dualStackInput [ "name" ]));
    expected = true;
  };

  testRejectsEmptyName = {
    expr = errorMatches "wanInvalidName" (tryError (dualStackInput // { name = ""; }));
    expected = true;
  };

  testRejectsNameStartingWithDigit = {
    expr = errorMatches "wanInvalidName" (tryError (dualStackInput // { name = "1primary"; }));
    expected = true;
  };

  testRejectsNameWithSpace = {
    expr = errorMatches "wanInvalidName" (tryError (dualStackInput // { name = "primary wan"; }));
    expected = true;
  };

  testAcceptsHyphenatedName = {
    expr = (wan.tryMake (dualStackInput // { name = "home-uplink-primary"; })).success;
    expected = true;
  };

  # ===== Error: wanInvalidInterface =====

  testRejectsEmptyInterface = {
    expr = errorMatches "wanInvalidInterface" (tryError (dualStackInput // { interface = ""; }));
    expected = true;
  };

  testRejectsLongInterface = {
    # IFNAMSIZ=16; on-wire length < 16 bytes (kernel parity).
    expr = errorMatches "wanInvalidInterface" (
      tryError (dualStackInput // { interface = "this-name-is-too-long"; })
    );
    expected = true;
  };

  testRejectsInterfaceWithSpace = {
    expr = errorMatches "wanInvalidInterface" (tryError (dualStackInput // { interface = "eth 0"; }));
    expected = true;
  };

  testRejectsInterfaceWithSlash = {
    expr = errorMatches "wanInvalidInterface" (tryError (dualStackInput // { interface = "eth/0"; }));
    expected = true;
  };

  # ===== Error: wanInvalidGatewayV4 =====

  testRejectsInvalidV4Gateway = {
    expr = errorMatches "wanInvalidGatewayV4" (
      tryError (
        dualStackInput
        // {
          gateways = dualStackInput.gateways // {
            v4 = "not.an.ip.addr";
          };
        }
      )
    );
    expected = true;
  };

  testRejectsV6AddressAsV4Gateway = {
    expr = errorMatches "wanInvalidGatewayV4" (
      tryError (
        dualStackInput
        // {
          gateways = dualStackInput.gateways // {
            v4 = "2001:db8::1";
          };
        }
      )
    );
    expected = true;
  };

  # ===== Error: wanInvalidGatewayV6 =====

  testRejectsInvalidV6Gateway = {
    expr = errorMatches "wanInvalidGatewayV6" (
      tryError (
        dualStackInput
        // {
          gateways = dualStackInput.gateways // {
            v6 = "not::an::ip";
          };
        }
      )
    );
    expected = true;
  };

  testRejectsV4AddressAsV6Gateway = {
    expr = errorMatches "wanInvalidGatewayV6" (
      tryError (
        dualStackInput
        // {
          gateways = dualStackInput.gateways // {
            v6 = "192.0.2.1";
          };
        }
      )
    );
    expected = true;
  };

  # ===== Error: wanInvalidProbe =====

  testForwardsProbeError = {
    expr = errorMatches "wanInvalidProbe" (tryError (dualStackInput // { probe.targets = [ ]; }));
    expected = true;
  };

  # ===== Error: wanNoGateways =====

  testRejectsBothGatewaysNull = {
    expr = errorMatches "wanNoGateways" (
      tryError (
        dualStackInput
        // {
          gateways = { };
          probe.targets = [ "1.1.1.1" ];
        }
      )
    );
    expected = true;
  };

  testRejectsBothGatewaysExplicitlyNull = {
    expr = errorMatches "wanNoGateways" (
      tryError (
        dualStackInput
        // {
          gateways = {
            v4 = null;
            v6 = null;
          };
          probe.targets = [ "1.1.1.1" ];
        }
      )
    );
    expected = true;
  };

  # ===== Family-coupling: wanV4GatewayNoTargets =====

  testRejectsV4GatewayWithoutV4Target = {
    expr = errorMatches "wanV4GatewayNoTargets" (
      tryError (dualStackInput // { probe.targets = [ "2606:4700:4700::1111" ]; })
    );
    expected = true;
  };

  # ===== Family-coupling: wanV6GatewayNoTargets =====

  testRejectsV6GatewayWithoutV6Target = {
    expr = errorMatches "wanV6GatewayNoTargets" (
      tryError (dualStackInput // { probe.targets = [ "1.1.1.1" ]; })
    );
    expected = true;
  };

  # ===== Family-coupling: wanV4TargetNoGateway =====

  testRejectsV4TargetWithoutV4Gateway = {
    expr = errorMatches "wanV4TargetNoGateway" (
      tryError (
        v6OnlyInput
        // {
          probe.targets = [
            "1.1.1.1"
            "2606:4700:4700::1111"
          ];
        }
      )
    );
    expected = true;
  };

  # ===== Family-coupling: wanV6TargetNoGateway =====

  testRejectsV6TargetWithoutV6Gateway = {
    expr = errorMatches "wanV6TargetNoGateway" (
      tryError (
        v4OnlyInput
        // {
          probe.targets = [
            "1.1.1.1"
            "2606:4700:4700::1111"
          ];
        }
      )
    );
    expected = true;
  };

  # ===== Aggregated multi-error =====

  testMultipleErrorsAggregated = {
    # Submit a config with multiple violations across categories.
    expr =
      let
        err = tryError {
          name = "1bad"; # wanInvalidName
          interface = "eth 0"; # wanInvalidInterface
          gateways.v4 = "not-an-ip"; # wanInvalidGatewayV4
          probe.targets = [ "1.1.1.1" ]; # otherwise valid probe
        };
        kinds = [
          "wanInvalidName"
          "wanInvalidInterface"
          "wanInvalidGatewayV4"
        ];
      in
      builtins.all (k: errorMatches k err) kinds;
    expected = true;
  };

  testFamilyCouplingSkippedWhenGatewayInvalid = {
    # When the v4 gateway fails to parse, the family-coupling check
    # is skipped — we can't reason about a malformed input. The
    # only error reported should be wanInvalidGatewayV4.
    expr =
      let
        err = tryError (
          dualStackInput
          // {
            gateways = {
              v4 = "not-an-ip";
              v6 = "2001:db8::1";
            };
            probe.targets = [ "2606:4700:4700::1111" ]; # no v4 target
          }
        );
      in
      errorMatches "wanInvalidGatewayV4" err && !(errorMatches "wanV4GatewayNoTargets" err);
    expected = true;
  };

  # ===== toJSONValue =====

  testToJSONValueIncludesName = {
    expr = (wan.toJSONValue (wan.make dualStackInput)).name;
    expected = "primary";
  };

  testToJSONValueStringifiesGatewayV4 = {
    expr = (wan.toJSONValue (wan.make dualStackInput)).gateways.v4;
    expected = "192.0.2.1";
  };

  testToJSONValueEmitsNullForMissingFamily = {
    expr = (wan.toJSONValue (wan.make v4OnlyInput)).gateways.v6;
    expected = null;
  };

  testToJSONValueEmbedsProbeAsNestedAttrset = {
    expr = builtins.isAttrs (wan.toJSONValue (wan.make dualStackInput)).probe;
    expected = true;
  };

  # ===== tryMake contract =====

  testTryMakeOkOnValid = {
    expr = (wan.tryMake dualStackInput).success;
    expected = true;
  };

  testTryMakeErrOnInvalid = {
    expr = (wan.tryMake { name = "bad"; }).success;
    expected = false;
  };

  testTryMakeErrorNullOnSuccess = {
    expr = (wan.tryMake dualStackInput).error;
    expected = null;
  };

  testTryMakeValueNullOnFailure = {
    expr = (wan.tryMake { name = "bad"; }).value;
    expected = null;
  };

  # ===== make throws =====

  testMakeThrowsOnInvalid = {
    expr = evalThrows (wan.make { name = "bad"; });
    expected = true;
  };
}
