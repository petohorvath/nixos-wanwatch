/*
  wanwatch — top-level library composition.

  Required inputs:
    lib    — `nixpkgs.lib`. Used throughout (`lib.types.*`,
             `lib.nameValuePair`, `lib.partition`, …). Treated as
             a standard dep, not an opt-in extension.
    libnet — `libnet.lib.withLib lib` — the libnet core plus
             libnet's option types. Used by validators in
             `lib/wan.nix` and `lib/types.nix`.

  Public surface:
    wanwatch.internal     — internal helpers (tag primitives,
                            validators, ordering, …)
    wanwatch.probe        — Probe value type
    wanwatch.wan          — WAN value type
    wanwatch.types        — NixOS option types (`lib.types.*`)
    wanwatch.version      — current library version string

  See PLAN.md §5.1 for the full target API; modules land
  bottom-up per PLAN.md §10.
*/
{ lib, libnet }:
let
  internal = {
    types = import ./internal/types.nix { inherit lib; };
  };

  probe = import ./probe.nix { inherit lib libnet internal; };
  wan = import ./wan.nix {
    inherit
      lib
      libnet
      internal
      probe
      ;
  };
  types = import ./types.nix { inherit lib libnet; };
in
{
  inherit
    internal
    probe
    wan
    types
    ;
  version = "0.1.0-dev";
}
