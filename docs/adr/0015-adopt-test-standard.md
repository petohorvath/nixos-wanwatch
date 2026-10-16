# Adopt the devnix-labs test standard

The tests follow the test standard shared by the devnix-labs repositories, so each test kind has one directory, one entry point, one runner, and fixed entry names in `checks`. `tests/unit/` and `tests/integration/` hold nix-unit suites, `tests/build/` the tests that run a real program in a derivation, and `tests/vm/` the NixOS VM scenarios. The runners in `tests/runners/` and `tests/helpers/evaluation-inputs.nix` are copies of the standard's shared files and are not edited here.

This replaces the nix-unit `tests` output of [ADR-0011](./0011-flake-parts-with-dev-partition.md). The nix-unit entry points take the flake inputs they evaluate as arguments, defaulting to the locked inputs, and import the library and NixOS modules from source, so `nix-unit tests/unit` runs without the flake. The `unit` and `integration` checks pass the inputs as store paths, which reaches every input that `--override-input` replaces. Before, the checks ran `nix-unit --flake` with an override for each input, and an input missing from the overrides failed only inside the sandbox.

## Consequences

The check names change: `format` becomes `formatting`, `package` becomes `package-wanwatchd`, and the `daemon`, `coverage`, and `race` checks become the `tests`, `coverage`, and `race` links of `build-daemon`. `observation` becomes `build-observation` and, like the other build tests, exists only on Linux. golangci-lint runs in `lint`.

The `flake-outputs` integration suite checks that the flake's `lib` and `nixosModules` match the source imports that the other suites test.
