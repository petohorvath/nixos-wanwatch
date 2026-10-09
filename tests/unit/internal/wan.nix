/*
  Tests for `lib/internal/wan.nix`, exposed as `wanwatch.wan`.
  `skeleton.nix` covers the `make` / `tryMake` / `toJSONValue`
  contract.
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
  inherit (helpers) getErrorKinds;
  inherit (wanwatch) probe wan;

  wanFieldTests = helpers.fieldTests {
    inherit (wan) tryMake;
    input = minimal;
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

  # A WAN serves the Families its Probe Targets cover, so a WAN with
  # only v6 Targets is valid.
  testFamiliesFollowProbeTargets = {
    expr = map (input: wan.families (wan.make input)) [
      minimal
      (minimal // { probe.targets.v6 = [ "2606:4700:4700::1111" ]; })
      full
    ];
    expected = [
      {
        v4 = true;
        v6 = false;
      }
      {
        v4 = false;
        v6 = true;
      }
      {
        v4 = true;
        v6 = true;
      }
    ];
  };

  fields = {
    name = wanFieldTests "name" "wanInvalidName" cases.identifiers;
    interface = wanFieldTests "interface" "wanInvalidInterface" cases.interfaceNames;
    pointToPoint = wanFieldTests "pointToPoint" "wanInvalidPointToPoint" cases.booleans;
  };

  rejections = helpers.rejectionTests wan.tryMake {
    wanInvalidName.missingName = removeAttrs minimal [ "name" ];
    wanInvalidInterface.missingInterface = removeAttrs minimal [ "interface" ];
  };

  # The Probe's own error kinds follow the wrapping kind.
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
