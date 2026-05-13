/*
  Unit tests for `lib/types/wan.nix`.

  Cross-field validation (probe forwarding, error aggregation) is
  tested in `tests/unit/internal/wan.nix` against `wan.make` /
  `wan.tryMake`. Here we only test what the type system itself
  enforces — per-field validation and the submodule's structural
  defaults.
*/
{ pkgs, libnet, ... }:
let
  wanwatch = import ../../../lib {
    inherit (pkgs) lib;
    inherit libnet;
  };
  inherit (wanwatch) types;

  helpers = import ../helpers.nix { inherit pkgs; };
  inherit (helpers) evalType evalTypeFails;

  # A minimal valid wan input. `name` is intentionally NOT supplied
  # — it's a `readOnly` field with `default = name` (from the attr
  # key). `evalType` wraps the value under `options.value`, so the
  # submodule sees `name = "value"`.
  baseConfig = {
    interface = "eth0";
    probe.targets = [ "1.1.1.1" ];
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
    # The `probe` submodule fills in its own defaults; we check the
    # outer fields and that probe was at least filled in (its
    # exhaustive defaults are tested in types/probe.nix).
    expr =
      let
        w = evalType types.wan baseConfig;
      in
      {
        inherit (w) name interface pointToPoint;
        probeMethod = w.probe.method;
        probeTargets = w.probe.targets;
      };
    expected = {
      name = "value"; # derived from `options.value` in `evalType`
      interface = "eth0";
      pointToPoint = false;
      probeMethod = "icmp";
      probeTargets = [ "1.1.1.1" ];
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
          targets = [ "1.1.1.1" ];
          method = "tcp";
        };
      }
    );
    expected = true;
  };
}
