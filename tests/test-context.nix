/*
  Builds `testContext`, the single argument every nix-unit suite
  receives. It imports the wanwatch library and NixOS modules from
  source, with the dependencies that `flake.nix` passes them, so unit
  suites evaluate no flake-parts.

  `nixpkgs`: the nixpkgs flake.
  `nix-libnet`: the nix-libnet flake.
  `self`: the flake itself, which only the integration entry point
  passes, for the suite that compares its outputs with the source;
  null otherwise.
  `system`: the host platform of the NixOS systems that the
  integration suites evaluate; null otherwise.

  Returns `{ fixtures; helpers; internal; lib; libnet; self; wanwatch; }`.
*/
{
  nixpkgs,
  nix-libnet,
  self ? null,
  system ? null,
}:
let
  inherit (nixpkgs) lib;

  libnetWithLib = nix-libnet.lib.withLib lib;
  wanwatch = import ../lib {
    inherit lib;
    libnet = libnetWithLib;
  };

  # Applies the static arguments as the flake's `importApply` does.
  importModule =
    modulePath: lib.setDefaultModuleLocation modulePath (import modulePath { inherit wanwatch; });
  nixosModules = {
    default = importModule ../nixos/module.nix;
    telegraf = importModule ../nixos/telegraf.nix;
  };
in
{
  inherit lib self wanwatch;
  libnet = libnetWithLib;
  # The modules below the public API, such as `primitives`.
  internal = import ../lib/internal {
    inherit lib;
    libnet = libnetWithLib;
  };
  fixtures = import ./fixtures/values.nix // {
    nixos = import ./fixtures/nixos.nix;
  };
  helpers =
    import ./helpers/value-types.nix { inherit lib; }
    // import ./helpers/nixos.nix { inherit nixosModules nixpkgs system; };
}
