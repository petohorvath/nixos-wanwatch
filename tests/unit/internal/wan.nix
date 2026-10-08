/*
  Unit tests for `lib/internal/wan.nix`, exposed as `wanwatch.wan`.
  Per AGENTS.md, each public function is exercised on positive and
  negative inputs, each error kind is triggered alone and in an
  aggregated case, and the §5.1 API skeleton is covered. A WAN's
  families derive from its probe targets.
*/
{ helpers, wanwatch, ... }:
let
  inherit (helpers) errorMatches evalThrows;
  inherit (wanwatch) wan;

  tryError = helpers.tryError wan;

  # Valid baselines; probe.targets alone determines the families.
  dualStackInput = {
    name = "primary";
    interface = "eth0";
    probe.targets = {
      v4 = [ "1.1.1.1" ];
      v6 = [ "2606:4700:4700::1111" ];
    };
  };

  v4OnlyInput = {
    name = "primary";
    interface = "eth0";
    probe.targets.v4 = [ "1.1.1.1" ];
  };

  v6OnlyInput = {
    name = "primary";
    interface = "eth0";
    probe.targets.v6 = [ "2606:4700:4700::1111" ];
  };

  ptpInput = {
    name = "vpn";
    interface = "wg0";
    pointToPoint = true;
    probe.targets.v4 = [ "1.1.1.1" ];
  };
in
{
  # ===== Happy path — three topologies =====

  testMakeDualStackReturnsValue = {
    expr = builtins.isAttrs (wan.make dualStackInput);
    expected = true;
  };

  testMakeV4OnlyAccepted = {
    expr = (wan.tryMake v4OnlyInput).success;
    expected = true;
  };

  testMakeV6OnlyAccepted = {
    expr = (wan.tryMake v6OnlyInput).success;
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

  # ===== pointToPoint field =====

  testPointToPointDefaultsToFalse = {
    expr = (wan.make dualStackInput).pointToPoint;
    expected = false;
  };

  testPointToPointAcceptsTrue = {
    expr = (wan.make ptpInput).pointToPoint;
    expected = true;
  };

  # ===== families accessor — derived from probe.targets =====

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
    expr =
      let
        inherit ((wan.make dualStackInput).probe) targets;
      in
      builtins.length targets.v4 + builtins.length targets.v6;
    expected = 2;
  };

  # ===== Error: wanInvalidName =====

  testWanRejectsMissingName = {
    expr = errorMatches "wanInvalidName" (tryError (removeAttrs dualStackInput [ "name" ]));
    expected = true;
  };

  testWanRejectsEmptyName = {
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

  # ===== Error: wanInvalidPointToPoint =====

  testRejectsNonBoolPointToPoint = {
    expr = errorMatches "wanInvalidPointToPoint" (
      tryError (dualStackInput // { pointToPoint = "yes"; })
    );
    expected = true;
  };

  testRejectsNullPointToPoint = {
    expr = errorMatches "wanInvalidPointToPoint" (
      tryError (dualStackInput // { pointToPoint = null; })
    );
    expected = true;
  };

  # ===== Error: wanInvalidProbe (forwarded from probe.tryMake) =====

  testForwardsProbeError = {
    expr = errorMatches "wanInvalidProbe" (tryError (dualStackInput // { probe.targets = { }; }));
    expected = true;
  };

  # ===== Aggregated multi-error =====

  testWanMultipleErrorsAggregated = {
    # Violations across several fields are all reported.
    expr =
      let
        error = tryError {
          name = "1bad"; # wanInvalidName
          interface = "eth 0"; # wanInvalidInterface
          pointToPoint = "yes"; # wanInvalidPointToPoint
          probe.targets.v4 = [ "1.1.1.1" ]; # otherwise valid probe
        };
        kinds = [
          "wanInvalidName"
          "wanInvalidInterface"
          "wanInvalidPointToPoint"
        ];
      in
      builtins.all (kind: errorMatches kind error) kinds;
    expected = true;
  };

  # ===== toJSONValue =====

  testToJSONValueIncludesName = {
    expr = (wan.toJSONValue (wan.make dualStackInput)).name;
    expected = "primary";
  };

  testToJSONValueIncludesInterface = {
    expr = (wan.toJSONValue (wan.make dualStackInput)).interface;
    expected = "eth0";
  };

  testToJSONValueIncludesPointToPointFalse = {
    expr = (wan.toJSONValue (wan.make dualStackInput)).pointToPoint;
    expected = false;
  };

  testToJSONValueIncludesPointToPointTrue = {
    expr = (wan.toJSONValue (wan.make ptpInput)).pointToPoint;
    expected = true;
  };

  testToJSONValueEmbedsProbeAsNestedAttrset = {
    expr = builtins.isAttrs (wan.toJSONValue (wan.make dualStackInput)).probe;
    expected = true;
  };

  testToJSONValueOmitsGatewaysField = {
    # The daemon discovers gateways at runtime via netlink, so the
    # rendered config carries none.
    expr = (wan.toJSONValue (wan.make dualStackInput)) ? gateways;
    expected = false;
  };

  # ===== tryMake contract =====

  testWanTryMakeOkOnValid = {
    expr = (wan.tryMake dualStackInput).success;
    expected = true;
  };

  testWanTryMakeErrOnInvalid = {
    expr = (wan.tryMake { name = "bad"; }).success;
    expected = false;
  };

  testWanTryMakeErrorNullOnSuccess = {
    expr = (wan.tryMake dualStackInput).error;
    expected = null;
  };

  testWanTryMakeValueNullOnFailure = {
    expr = (wan.tryMake { name = "bad"; }).value;
    expected = null;
  };

  # ===== make throws =====

  testWanMakeThrowsOnInvalid = {
    expr = evalThrows (wan.make { name = "bad"; });
    expected = true;
  };

  # ===== Round-trip =====

  testWanRoundTrip = {
    # AGENTS.md (5): re-emitting the JSON shape after a second
    # `make` must be byte-identical to the first, nested probe
    # included.
    expr =
      let
        firstJSON = wan.toJSONValue (wan.make dualStackInput);
        secondJSON = wan.toJSONValue (wan.make firstJSON);
      in
      firstJSON == secondJSON;
    expected = true;
  };
}
