/*
  Unit tests for `lib/types/wan.nix`.

  Cross-field invariants from PLAN §5.4 (at-least-one-gateway,
  family-coupling) are tested in `tests/unit/internal/wan.nix`
  against `wan.make` / `wan.tryMake`. Here we only test what the
  type system itself can enforce — per-field validation and the
  submodule's structural defaults.
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
    gateways.v4 = "192.0.2.1";
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

  # ===== wanGateways — submodule =====

  testWanGatewaysDefaultsBothNull = {
    expr = evalType types.wanGateways { };
    expected = {
      v4 = null;
      v6 = null;
    };
  };

  testWanGatewaysAcceptsV4Only = {
    expr = evalType types.wanGateways { v4 = "192.0.2.1"; };
    expected = {
      v4 = "192.0.2.1";
      v6 = null;
    };
  };

  testWanGatewaysAcceptsV6Only = {
    expr = evalType types.wanGateways { v6 = "2001:db8::1"; };
    expected = {
      v4 = null;
      v6 = "2001:db8::1";
    };
  };

  testWanGatewaysAcceptsBoth = {
    expr = evalType types.wanGateways {
      v4 = "192.0.2.1";
      v6 = "2001:db8::1";
    };
    expected = {
      v4 = "192.0.2.1";
      v6 = "2001:db8::1";
    };
  };

  testWanGatewaysRejectsBadV4 = {
    expr = evalTypeFails types.wanGateways { v4 = "not-an-ip"; };
    expected = true;
  };

  testWanGatewaysRejectsV6AsV4 = {
    expr = evalTypeFails types.wanGateways { v4 = "2001:db8::1"; };
    expected = true;
  };

  testWanGatewaysRejectsBadV6 = {
    expr = evalTypeFails types.wanGateways { v6 = "not-an-ip"; };
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
        inherit (w) name interface;
        gateways = w.gateways;
        probeMethod = w.probe.method;
        probeTargets = w.probe.targets;
      };
    expected = {
      name = "value"; # derived from `options.value` in `evalType`
      interface = "eth0";
      gateways = {
        v4 = "192.0.2.1";
        v6 = null;
      };
      probeMethod = "icmp";
      probeTargets = [ "1.1.1.1" ];
    };
  };

  testWanRejectsBadInterface = {
    expr = evalTypeFails types.wan (baseConfig // { interface = "eth 0"; });
    expected = true;
  };

  testWanRejectsBadGateway = {
    expr = evalTypeFails types.wan (
      baseConfig
      // {
        gateways.v4 = "not-an-ip";
      }
    );
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
