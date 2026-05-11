/*
  wanwatch.withLib

  Opt-in entry point that injects `nixpkgs.lib` into the core
  library so NixOS option types under `wanwatch.types.*` become
  available. Without this call, wanwatch has no dependency on
  `nixpkgs.lib` (libnet's pure-Nix core remains a dep regardless —
  see lib/default.nix).

  Mirrors the libnet `withLib` pattern with one extra currying
  step: takes the wanwatch core, *then* libnet's pure-Nix core,
  *then* nixpkgs.lib. lib/default.nix pre-applies the first two so
  the user-facing call is simply `wanwatch.withLib pkgs.lib`.

  Internally, this delegates libnet-types construction to libnet
  by invoking `libnet.withLib lib` and passing the full version
  (core + types) down to `lib/types.nix`. The wanwatch types
  module can therefore reference `libnet.types.ipv4` etc.
  directly.

  Example:
    wanwatch.withLib pkgs.lib
    => wanwatch // { types = { wanName = <option-type>; ... }; }
*/
core: libnet: lib:
core
// (import ./types.nix {
  inherit lib;
  libnet = libnet.withLib lib;
})
