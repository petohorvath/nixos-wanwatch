/*
  wanwatch.withLib

  Opt-in entry point that injects `nixpkgs.lib` into the core library
  so NixOS option types under `wanwatch.types.*` become available.
  Without this call, wanwatch has no dependency on nixpkgs.

  Mirrors the libnet `withLib` pattern. Reach `types.nix` only
  through this entry point — calling `import ./types.nix { lib =
  <something>; }` directly bypasses the contract that says the rest
  of the library is consumable without `nixpkgs.lib`.

  Example:
    wanwatch.withLib pkgs.lib
    => wanwatch // { types = { wanName = <option-type>; ... }; }
*/
core: lib: core // (import ./types.nix { inherit lib; })
