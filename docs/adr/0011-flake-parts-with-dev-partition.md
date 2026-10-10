# Assemble the flake with flake-parts and a dev partition

The flake is assembled with flake-parts, following the shared flake-parts project layout that sibling projects such as `nixos-cross-config` use, rather than the hand-written `forAllSystems` of [ADR-0010](./0010-system-agnostic-lib-output.md). `flake.nix` declares only the systems, the library, the NixOS modules, and the packages. A `dev` partition, loaded from `dev/default.nix`, defines `checks`, `devShells`, `formatter`, and the nix-unit `tests` output, so the public outputs evaluate without the development wiring. The cost is one more input, `flake-parts`, whose `nixpkgs-lib` follows `nixpkgs`.

## Considered options

The development inputs (`treefmt-nix`, `nftzones`, `nixpkgs-unstable`) stay in the root `flake.nix` rather than moving to a separate `dev/flake.nix` through `partitions.dev.extraInputsFlake`. A separate development flake would keep them out of consumers' lock files, but it needs its own lock file, cannot `follows` the root `nixpkgs`, and would move the inputs out of reach of the Dependabot `nixpkgs` updates and the root lock review.
