/*
  Tests for `lib/internal/wan.nix`, exposed as `wanwatch.wan`.
  `skeleton.nix` covers the `make` / `tryMake` / `toJSONValue`
  contract. A WAN's families derive from its probe targets.
*/
{
  fixtures,
  helpers,
  wanwatch,
  ...
}:
let
  inherit (fixtures) cases;
  inherit (fixtures.inputs.wan) full minimal;
  inherit (helpers) fieldTests getErrorKinds;
  inherit (wanwatch) probe wan;

  wanField =
    field: kind:
    fieldTests {
      inherit (wan) tryMake;
      inherit kind;
      input = minimal;
      path = [ field ];
    };
in
{
  testPointToPointDefaultsToFalse = {
    expr = (wan.make minimal).pointToPoint;
    expected = false;
  };

  testEmbedsProbeValue = {
    expr = (wan.make full).probe;
    expected = probe.make full.probe;
  };

  # The daemon discovers Gateways at runtime through netlink, so the
  # rendered config carries none.
  testSerializesWithoutGateways = {
    expr = builtins.attrNames (wan.toJSONValue (wan.make full));
    expected = [
      "interface"
      "name"
      "pointToPoint"
      "probe"
    ];
  };

  families = {
    testV4Only = {
      expr = wan.families (wan.make minimal);
      expected = {
        v4 = true;
        v6 = false;
      };
    };

    testV6Only = {
      expr = wan.families (wan.make (minimal // { probe.targets.v6 = [ "2606:4700:4700::1111" ]; }));
      expected = {
        v4 = false;
        v6 = true;
      };
    };

    testDualStack = {
      expr = wan.families (wan.make full);
      expected = {
        v4 = true;
        v6 = true;
      };
    };
  };

  fields = {
    name = wanField "name" "wanInvalidName" cases.identifiers;
    interface = wanField "interface" "wanInvalidInterface" cases.interfaceNames;
    pointToPoint = wanField "pointToPoint" "wanInvalidPointToPoint" cases.booleans;
  };

  rejections = helpers.rejectionTests wan.tryMake {
    wanInvalidName.missingName = removeAttrs minimal [ "name" ];
    wanInvalidInterface.missingInterface = removeAttrs minimal [ "interface" ];
  };

  # The probe's own report follows the wrapping kind.
  testForwardsProbeErrors = {
    expr = getErrorKinds (wan.tryMake (minimal // { probe.targets = { }; }));
    expected = [
      "wanInvalidProbe"
      "probeNoTargets"
    ];
  };

  testReportsEveryViolation = {
    expr = getErrorKinds (
      wan.tryMake {
        name = "1bad";
        interface = "eth 0";
        pointToPoint = "yes";
        probe.targets.v4 = [ "1.1.1.1" ];
      }
    );
    expected = [
      "wanInvalidName"
      "wanInvalidInterface"
      "wanInvalidPointToPoint"
    ];
  };
}
