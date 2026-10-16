/*
  Builds `testContext`, the single argument every nix-unit suite
  receives. It imports the wanwatch library and NixOS modules from
  source, with the dependencies that `flake.nix` passes them, so unit
  suites evaluate no flake-parts.

  `nixpkgs`: the nixpkgs flake.
  `libnet`: the nix-libnet flake.
  `flake-parts`: the flake-parts flake, which only the integration
  entry point passes; null otherwise.
  `system`: the host platform of the evaluated NixOS systems.

  Returns `{ fixtures; helpers; internal; lib; libnet; wanwatch; }`.
*/
{
  nixpkgs,
  libnet,
  flake-parts ? null,
  system,
}:
let
  inherit (nixpkgs) lib;

  libnetWithLib = libnet.lib.withLib lib;
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

  # The flake's outputs as consumers see them, evaluated from
  # `flake.nix` with the given inputs.
  flakeOutputs =
    if flake-parts == null then
      null
    else
      import ./helpers/evaluation-inputs.nix {
        path = ../.;
        inputs = { inherit flake-parts libnet nixpkgs; };
      };
in
{
  inherit lib wanwatch;
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
    // import ./helpers/nixos.nix { inherit nixosModules nixpkgs system; }
    // {
      inherit flakeOutputs;
    };
}
