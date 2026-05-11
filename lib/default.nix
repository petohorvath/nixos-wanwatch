/*
  wanwatch — top-level library composition.

  Required inputs:
    lib    — `nixpkgs.lib`. Used throughout (`lib.types.*`,
             `lib.nameValuePair`, `lib.partition`, …). Treated as
             a standard dep, not an opt-in extension.
    libnet — `libnet.lib.withLib lib` — the libnet core plus
             libnet's option types. Used by validators in
             `lib/internal/wan.nix` and (in Pass 5)
             `lib/types/wan.nix`.

  Layout mirrors nftzones:
    lib/internal/<name>.nix — operational code (make, tryMake,
                              accessors, predicates, …)
    lib/types/<name>.nix    — NixOS option types

  Public surface:
    wanwatch.internal.<name> — operational modules namespaced
    wanwatch.types           — flattened option types (per-file
                               merged via `lib/types/default.nix`)
    wanwatch.probe / .wan    — convenience aliases to the
                               operational modules
    wanwatch.version         — current library version string
*/
{ lib, libnet }:
let
  internal = import ./internal { inherit lib libnet; };
  types = import ./types { inherit lib libnet internal; };
in
{
  inherit internal types;
  inherit (internal)
    probe
    member
    wan
    group
    marks
    ;
  version = "0.1.0-dev";
}
