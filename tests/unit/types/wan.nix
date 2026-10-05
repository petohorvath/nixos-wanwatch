/*
  Unit tests for `lib/types/wan.nix`: per-field validation and the
  submodule's defaults. `tests/unit/internal/wan.nix` covers
  cross-field validation in `wan.make` / `wan.tryMake`.
*/
{ helpers, wanwatch, ... }:
let
  inherit (helpers) evalType evalTypeFails;
  inherit (wanwatch) types;

  # `name` is read-only and defaults to the attribute key; `evalType`
  # declares the option as `value`, so the submodule sees "value".
  baseConfig = {
    interface = "eth0";
    probe.targets.v4 = [ "1.1.1.1" ];
  };
in
{
  # ===== leaf types =====

  testWanNameAcceptsIdentifier = {
    expr = evalType types.wanName "primary";
    expected = "primary";
  };

  testWanNameRejectsLeadingDigit = {
    expr = evalTypeFails types.wanName "1bad";
    expected = true;
  };

  testWanInterfaceAcceptsEth0 = {
    expr = evalType types.wanInterface "eth0";
    expected = "eth0";
  };

  testWanInterfaceRejectsTooLong = {
    expr = evalTypeFails types.wanInterface "this-name-is-too-long";
    expected = true;
  };

  testWanInterfaceRejectsSpace = {
    expr = evalTypeFails types.wanInterface "eth 0";
    expected = true;
  };

  # ===== wan — top-level submodule =====

  testWanMinimalShape = {
    # types/probe.nix covers the probe defaults exhaustively; this
    # checks the outer fields and that the probe was filled in.
    expr =
      let
        wan = evalType types.wan baseConfig;
      in
      {
        inherit (wan) interface name pointToPoint;
        probeMethod = wan.probe.method;
        probeTargets = wan.probe.targets;
      };
    expected = {
      name = "value"; # derived from `options.value` in `evalType`
      interface = "eth0";
      pointToPoint = false;
      probeMethod = "icmp";
      probeTargets = {
        v4 = [ "1.1.1.1" ];
        v6 = [ ];
      };
    };
  };

  testWanPointToPointAcceptsTrue = {
    expr = (evalType types.wan (baseConfig // { pointToPoint = true; })).pointToPoint;
    expected = true;
  };

  testWanPointToPointDefaultsFalse = {
    expr = (evalType types.wan baseConfig).pointToPoint;
    expected = false;
  };

  testWanRejectsBadInterface = {
    expr = evalTypeFails types.wan (baseConfig // { interface = "eth 0"; });
    expected = true;
  };

  testWanRejectsNonBoolPointToPoint = {
    expr = evalTypeFails types.wan (baseConfig // { pointToPoint = "yes"; });
    expected = true;
  };

  testWanRejectsBadProbe = {
    expr = evalTypeFails types.wan (
      baseConfig
      // {
        probe = {
          targets.v4 = [ "1.1.1.1" ];
          method = "tcp";
        };
      }
    );
    expected = true;
  };
}
