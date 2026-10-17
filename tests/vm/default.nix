/*
  The VM entry point: end-to-end scenarios in NixOS VMs, covering what
  evaluation cannot: capabilities, hardening, netlink, and socket
  modes. Each scenario calls `runNixOSTest` and reaches the wanwatch
  modules through `inputs.self`.
*/
{ inputs, pkgs }:
let
  testArgs = { inherit inputs pkgs; };
in
{
  cold-start = import ./cold-start.nix testArgs;
  failover-dual-stack = import ./failover-dual-stack.nix testArgs;
  failover-probe-loss = import ./failover-probe-loss.nix testArgs;
  failover-probe-loss-v6 = import ./failover-probe-loss-v6.nix testArgs;
  failover-v4 = import ./failover-v4.nix testArgs;
  failover-v6 = import ./failover-v6.nix testArgs;
  family-health-policy = import ./family-health-policy.nix testArgs;
  gateway-discovery = import ./gateway-discovery.nix testArgs;
  gateway-discovery-v6 = import ./gateway-discovery-v6.nix testArgs;
  group-table-repair = import ./group-table-repair.nix testArgs;
  hooks = import ./hooks.nix testArgs;
  metrics = import ./metrics.nix testArgs;
  nftables-steering = import ./nftables-steering.nix testArgs;
  nftzones-integration = import ./nftzones-integration.nix testArgs;
  recovery = import ./recovery.nix testArgs;
  recovery-v6 = import ./recovery-v6.nix testArgs;
  smoke = import ./smoke.nix testArgs;
}
