/*
  End-to-end scenarios in NixOS VMs, covering what evaluation cannot:
  capabilities, hardening, netlink, and socket modes. Each scenario
  receives the wanwatch module plus the extra modules and libraries it
  takes.

  `nftzones`: the nix-nftzones flake, for the nftzones-integration
  scenario.
  `nixosModules`: the flake's NixOS modules.
  `pkgs`: the package set the VMs build against.

  Returns an attrset of VM test derivations, one per scenario.
*/
{
  nftzones,
  nixosModules,
  pkgs,
}:
let
  scenario =
    name: extraArgs:
    import (./. + "/${name}.nix") (
      {
        inherit pkgs;
        nixosModule = nixosModules.default;
      }
      // extraArgs
    );
in
{
  smoke = scenario "smoke" { };
  failover-v4 = scenario "failover-v4" { };
  failover-v6 = scenario "failover-v6" { };
  failover-dual-stack = scenario "failover-dual-stack" { };
  failover-probe-loss = scenario "failover-probe-loss" { };
  failover-probe-loss-v6 = scenario "failover-probe-loss-v6" { };
  cold-start = scenario "cold-start" { };
  recovery = scenario "recovery" { };
  recovery-v6 = scenario "recovery-v6" { };
  hooks = scenario "hooks" { };
  metrics = scenario "metrics" {
    telegrafModule = nixosModules.telegraf;
  };
  family-health-policy = scenario "family-health-policy" { };
  gateway-discovery = scenario "gateway-discovery" { };
  gateway-discovery-v6 = scenario "gateway-discovery-v6" { };
  nftzones-integration = scenario "nftzones-integration" {
    nftzonesModule = nftzones.nixosModules.default;
    nftypes = nftzones.inputs.nftypes.lib;
  };
}
