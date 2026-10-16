# Adopt the devnix-labs test standard

The tests follow the test standard shared by the devnix-labs repositories, so each test kind has one directory and one entry point, and the tests run on standard runners. `tests/unit/` and `tests/integration/` hold nix-unit suites, `tests/build/` the tests that run a real program in a derivation, and `tests/vm/` the NixOS VM scenarios.

The unit and integration suites run on nix-unit's flake-parts module, which the `dev` partition imports. It publishes the suites as the `tests.systems.<system>` output of [ADR-0011](./0011-flake-parts-with-dev-partition.md)'s partition and adds one `nix-unit` check, which passes every flake input as `--override-input`, so a new input cannot be forgotten. The entry points take the inputs as arguments and import the library and NixOS modules from source, so unit suites evaluate no flake-parts. Build tests are plain derivations, and each VM scenario calls `pkgs.testers.runNixOSTest` itself. Before, a repository-specific check ran `nix-unit --flake` with an override for each input that it listed by hand, and an input missing from the list failed only inside the sandbox.

## Consequences

The check names change: `unit` and `integration` become `nix-unit`, `format` becomes `formatting`, `package` becomes `package-wanwatchd`, and the `daemon`, `coverage`, and `race` checks become the `tests`, `coverage`, and `race` links of `build-daemon`. `observation` becomes `build-observation` and, like the other build tests, exists only on Linux. golangci-lint runs in `lint`.

The flake gains the `nix-unit` input, whose own inputs follow `nixpkgs` or are dropped. The `nix-unit` check copies every flake input into a store inside its build directory, and evaluates all suites in one process.

The `flake-outputs` integration suite checks that the flake's `lib` and `nixosModules` match the source imports that the other suites test.
