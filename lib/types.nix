/*
  wanwatch.types

  NixOS option-type integration. Produces module-option types
  (wan-name, gateway, target, ...) plus `.mk` coercers that validate
  and return the original value. Requires `nixpkgs.lib`; reach this
  module only through `wanwatch.withLib pkgs.lib`.

  Pass 1 boundary: empty stub so the `withLib` contract is fixed from
  day one. Real types land in Pass 5 (PLAN.md §10), at which point
  this file grows to flatten per-module type exports under a single
  `types` namespace — same pattern as `nix-nftzones/lib/default.nix`.
*/
{ lib }:
{
  types = { };
}
