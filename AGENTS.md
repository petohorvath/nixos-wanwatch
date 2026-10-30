# AGENTS.md

Contributor instructions for people and automated agents working in this repository.

## Project

`nixos-wanwatch` provides multi-WAN monitoring and single-active failover for NixOS; `main` implements the v1 design. [`CHANGELOG.md`](./CHANGELOG.md) records each release and, under Unreleased, the changes on `main` since the last one, with a migration note for every public-API break. [ADR-0018](./docs/adr/0018-0x-stability-promise.md) defines what a 0.x release may break, and [`TODO.md`](./TODO.md) is the deferred-work backlog.

It has three layers, with dependencies pointing bottom-up only:

- `lib/`: a pure-Nix library of validated WAN, Probe, Member, Group, and global-settings values, option types, the selector mirror, and daemon-config rendering. It never depends on the module or the daemon.
- `nixos/`: the thin `services.wanwatch` NixOS module and the optional Telegraf integration.
- `daemon/`: the Linux-only Go `wanwatchd`, packaged by `packages/wanwatchd/package.nix`. Its packages under `daemon/internal/` stay private, and `daemon/vendor/` is vendored code that is never edited or reformatted by hand.

The runtime path is: NixOS configuration → `lib` validation and rendering → `/etc/wanwatch/config.json` → `wanwatchd` consumes Probe and rtnetlink events → `decision` computes Selections with the pure selector → `apply` mutates routes, rules, and conntrack → State, Hooks, and metrics publish the result.

## Design records

[`docs/adr/`](./docs/adr/) records the architectural decisions, [`docs/specs/`](./docs/specs/) the public contracts, and [`GLOSSARY.md`](./GLOSSARY.md) the terminology. Changes to architecture, public contracts, compatibility, migration, security boundaries, rollback, or recovery need explicit approval; record an approved change in a new ADR or in the affected spec, in the same commit as the code.

Before changing a contract, read its sources and documentation together:

| Contract                | Sources and documentation                                                                                                                            |
| ----------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| Nix options and outputs | `lib/types/`, `nixos/`, `README.md`                                                                                                                  |
| Daemon config JSON      | `lib/internal/config.nix`, `daemon/internal/config/`, the golden `daemon/internal/config/testdata/config.golden.json`, `docs/specs/daemon-config.md` |
| State and Hooks         | `daemon/internal/state/`, `docs/specs/daemon-state.md`                                                                                               |
| Selection and failover  | the Nix and Go selectors, `docs/selector.md`, `docs/specs/failover.md`                                                                               |
| Metrics                 | `daemon/internal/metrics/`, `docs/metrics.md`                                                                                                        |
| Firewall composition    | `docs/nftzones-integration.md`                                                                                                                       |

Keep schema versions synchronized across producers, consumers, tests, and specs. Plaintext secrets stay out of source, Nix expressions, command lines, and the Nix store.

## Terminology

Use the [`GLOSSARY.md`](./GLOSSARY.md) terms exactly in code, comments, commit messages, error kinds, metric names, Hook variables, and docs. In particular, a Probe is configuration, a Sample is one attempt, Health is the derived WAN status, a Selection is the chosen Member, a Decision is a Selection change, and Apply is kernel mutation. Add or revise a glossary entry in the commit that introduces the concept.

## Development environment

The flake is the only entry point. `flake.nix` assembles the public outputs with flake-parts; the `dev` partition in `dev/` defines the checks, development shells, formatter, and the nix-unit `tests` output ([ADR-0011](./docs/adr/0011-flake-parts-with-dev-partition.md)). The tests follow the devnix-labs test standard ([ADR-0015](./docs/adr/0015-adopt-test-standard.md)): `dev/default.nix` runs the nix-unit suites through nix-unit's flake-parts module, `tests/default.nix` assembles the build and VM entries, and `dev/checks.nix` adds the lint and package entries. Formatting and linting follow the devnix-labs code quality standard ([ADR-0016](./docs/adr/0016-adopt-code-quality-standard.md)): treefmt-nix's flake-parts module, configured in `dev/treefmt.nix`, provides `nix fmt` and the `treefmt` entry. `nix develop` provides Go, gopls, golangci-lint, statix, and treefmt with every program that `dev/treefmt.nix` enables. The flake supports `x86_64-linux` and `aarch64-linux` only, because the daemon and the NixOS module are Linux-only.

Override sibling flakes with absolute paths:

```sh
nix build --override-input nix-libnet path:/absolute/path/to/nix-libnet \
  .#checks.x86_64-linux.nix-unit
```

## Formatting and linting

`nix fmt` runs treefmt with the programs in `dev/treefmt.nix`: nixfmt and deadnix for Nix, gofumpt and goimports for Go, shfmt and shellcheck for shell, ruff-format and ruff-check for Python, prettier for Markdown, YAML, and JSON, and actionlint for the workflows. deadnix and ruff-check apply their fixes, so review the diff before committing. A finding that a tool cannot fix fails `nix fmt` and the `treefmt` entry. Prettier runs with its defaults, so an editor that formats on save matches `nix fmt`. treefmt skips `daemon/vendor/` and the `*.golden.json` files, whose bytes come from their generators.

statix and golangci-lint (`.golangci.yml`, v2 format) run in the `lint` entry instead, because treefmt runs only `statix fix`, which skips the lints it cannot fix, and passes golangci-lint single files instead of packages. golangci-lint checks lint only; formatting failures belong to treefmt. A shell file without a shebang starts with a `# shellcheck shell=bash` directive, as `.envrc` does.

```sh
nix fmt
nix build .#checks.x86_64-linux.treefmt
nix build .#checks.x86_64-linux.lint
nix develop --command statix check .
nix develop --command bash -c 'cd daemon && go vet ./... && golangci-lint run ./...'
git diff --check
```

## Tests and checks

Run the smallest checks that exercise the change first. The examples use `x86_64-linux`.

| Check               | Covers                                                                                                                                                                                                                    |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `treefmt`           | Every program in `dev/treefmt.nix`: fails when a tool would change a file or reports a finding it cannot fix                                                                                                              |
| `lint`              | statix over the repository, and the `.golangci.yml` linters over the daemon                                                                                                                                               |
| `nix-unit`          | The nix-unit suites: `unit` (pure-Nix library tests, including the value-type skeleton tests) and `integration` (NixOS module evaluation, rendered config, Telegraf wiring, expected rejections, and the flake's outputs) |
| `build-daemon`      | All Go tests in a hermetic, network-disabled build (`tests`), the per-package coverage floors defined in `tests/build/daemon.nix` (`coverage`), and all Go tests with the race detector (`race`)                          |
| `build-observation` | The VM observation helpers' contract tests, without a VM                                                                                                                                                                  |
| `package-wanwatchd` | The production daemon derivation                                                                                                                                                                                          |
| `vm-<scenario>`     | NixOS VM scenarios (Linux with KVM)                                                                                                                                                                                       |

The flake has one nixpkgs input, pinned to the stable branch. To run a check against nixos-unstable, override that input; `--override-input` leaves `flake.lock` unchanged ([ADR-0012](./docs/adr/0012-single-nixpkgs-input.md)).

Tests live under `tests/`, one directory per kind: `unit/` and `integration/` hold [nix-unit](https://github.com/nix-community/nix-unit) suites, `build/` the tests that run a real program in a derivation, and `vm/` the NixOS VM scenarios. Each kind's `default.nix` lists its tests by hand, so a new test file runs only once it is listed there. Unit suites mirror the source path they test (`lib/internal/config.nix` → `tests/unit/internal/config.nix`); every suite receives the `testContext` that `tests/test-context.nix` builds from source. Shared helpers live in `tests/helpers/` and shared data in `tests/fixtures/`.

The `nix-unit` check runs the flake's `tests.systems.<system>` output in the sandbox, with every flake input passed as `--override-input`. For per-test results, run nix-unit on the output directly; a longer attribute path narrows the run to one suite, `--attr <test>` narrows it to one test of that suite, and `--override-input nixpkgs <flake-ref>` evaluates against another nixpkgs. nix-unit treats the selected attribute as a suite, so a test's full attribute path does not select it.

```sh
nix develop --command nix-unit --flake .#tests.systems.x86_64-linux.unit
nix develop --command nix-unit --flake .#tests.systems.x86_64-linux.integration.nixos-module
nix develop --command nix-unit --flake .#tests.systems.x86_64-linux.unit.internal.config \
  --attr testSchemaVersion
nix build .#checks.x86_64-linux.nix-unit
nix build .#checks.x86_64-linux.vm-smoke
nix build --override-input nixpkgs github:NixOS/nixpkgs/nixos-unstable \
  .#checks.x86_64-linux.vm-smoke
cd daemon && go test -race -timeout 120s ./...
```

`nix flake check` on Linux runs every check, including the full VM matrix. It is resource-intensive; report it as passed only when it actually ran.

CI calls the [shared project policy](https://github.com/petohorvath/nixos-project-policy/blob/v0.6/POLICY.md) from [`.github/workflows/check.yml`](./.github/workflows/check.yml) ([ADR-0013](./docs/adr/0013-adopt-project-policy.md)). On x86_64 and aarch64 Linux, the policy checks the inputs, public outputs, development shell, and formatter, and builds every check with the locked nixpkgs and with the policy's stable and unstable pins. The `vm-*` checks run in the policy's VM test jobs, which skip them on a runner without KVM. [`.github/workflows/ci.yml`](./.github/workflows/ci.yml) adds Go module verification and the review-environment contract test `tooling/review-env-test.sh`, which need the network or run outside the Nix sandbox. To run the policy locally from a clean checkout, use a Nix whose `nix derivation show` reports format version 4, such as Nix 2.34:

```sh
nix run github:petohorvath/nixos-project-policy/v0.6 -- check .
nix run github:petohorvath/nixos-project-policy/v0.6 -- test . --nixpkgs locked
nix run github:petohorvath/nixos-project-policy/v0.6 -- test . --nixpkgs stable
nix run github:petohorvath/nixos-project-policy/v0.6 -- test . --nixpkgs unstable
```

`test` skips the `vm-*` checks; add `--vm` to build only them.

## Conventions

Every Nix value type (`wan`, `probe`, `group`, `member`, `global`) implements the `make`, `tryMake`, and `toJSONValue` skeleton that `tests/unit/skeleton.nix` asserts. Pure-function modules such as `selector` and `config` use purpose-specific APIs.

Nix tests are nix-unit attrsets of `testFoo = { expr; expected; }` (or `expectedError`), nested by module. Shared inputs and the valid and invalid cases of each value domain live in `tests/fixtures/values.nix`; reuse a case table wherever an option type and a validator check the same domain, so the two layers stay in agreement.

Nix code uses current APIs and explicit dependencies: `inherit (x) y` rather than file-scope `with`, `lib.types.oneOf` rather than `lib.types.either`, and never the removed `lib.types.uniq`. Every NixOS option has a type, a description, and a default or example.

Go code propagates `context.Context` and honors cancellation, logs with `log/slog`, wraps errors with `%w`, and inspects them with `errors.Is` or `errors.As`. It prefers concrete types when only one implementation exists, keeps `init` free of side effects, and panics only in `main`.

Tests live with the behavior they protect. Go tests are table-driven, with `t.Helper()` in helpers, `testing.TB` in shared helpers, and `t.Parallel()` only when safe. Each public Nix function has tests for:

1. the happy path;
2. every `throws` branch;
3. both outcomes of every predicate;
4. every boundary (empty, single, minimum, maximum);
5. the serialization round trip (`make` → `toJSONValue` → `make`);
6. determinism.

Coverage floors, lint rules, security hardening, and CI gates stay at their current strength; fix the code to pass them.

## Audit cadence

| Audit                                      | Cadence            | Mechanism                                                                                                  |
| ------------------------------------------ | ------------------ | ---------------------------------------------------------------------------------------------------------- |
| Vulnerabilities                            | None (paused)      | No workflow runs `govulncheck` or `vulnix`; [`TODO.md`](./TODO.md) tracks restoring the audit              |
| Primary nixpkgs                            | Monthly            | Manual `nix flake update nixpkgs` on the pinned stable branch; merge after reviewing the lock delta and CI |
| Other dependencies                         | Monthly            | Manual review of `go.mod` and other `flake.lock` deltas                                                    |
| Public API, glossary, and convention drift | Each minor version | Manual review of `lib/default.nix`, daemon exports, `GLOSSARY.md`, and this file against the code          |

## Documentation map

- [`README.md`](./README.md): overview, installation, usage, and limitations.
- [`GLOSSARY.md`](./GLOSSARY.md): terminology.
- [`docs/adr/`](./docs/adr/): architectural decisions.
- [`docs/wan-monitoring.md`](./docs/wan-monitoring.md): conceptual introduction.
- [`docs/architecture.md`](./docs/architecture.md): layers, data flow, Families, and Gateways.
- [`docs/selector.md`](./docs/selector.md): Strategy and Hysteresis.
- [`docs/nftzones-integration.md`](./docs/nftzones-integration.md): firewall integration.
- [`docs/metrics.md`](./docs/metrics.md): Prometheus catalog and Telegraf.
- [`docs/specs/`](./docs/specs/): daemon-config, daemon-state, failover, probe-algorithm, and prior art.

## Agent skills

### Issue tracker

Issues live in GitHub Issues for `petohorvath/nixos-wanwatch`, managed with `gh`. See `docs/agents/issue-tracker.md`.

### Triage labels

Default vocabulary: `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: root `GLOSSARY.md` plus `docs/adr/`. See `docs/agents/domain.md`.

## Contributions

Keep each commit to one logical change, with its tests and documentation. Use an imperative subject of at most 72 characters in the form `scope: Summary`; scopes are `lib`, `internal`, `types`, `modules`, `daemon`, `tests`, `docs`, `ci`, `deps`, and `flake`. Explain why in the body when the diff does not show it.

Before handoff, inspect the final diff, list exactly which checks ran, distinguish focused checks from the full flake and VM matrix, and report every failure or skipped gate.

## Releasing

A release is a pull request that prepares the version, then a tag and a GitHub release on its merge commit. Under [ADR-0018](./docs/adr/0018-0x-stability-promise.md), a 0.x release that breaks the `services.wanwatch` options or `lib` bumps the minor version.

1. Bump the version in both places: `version` in `lib/default.nix`, which the flake passes to the package, and the fallback `version` default in `packages/wanwatchd/package.nix`. Update the expected version in `tests/unit/lib.nix`.
2. In `CHANGELOG.md`, rename `## [Unreleased]` to `## [X.Y.Z] - YYYY-MM-DD`, add a new empty `## [Unreleased]` above it, and update the link definitions at the end of the file. The date is the day of the tag; correct it before the merge if the release slips.
3. Pin the README install snippet's `url` to `github:petohorvath/nixos-wanwatch/vX.Y.Z`.
4. After the pull request merges, tag its merge commit and push the tag:

   ```sh
   git tag -a vX.Y.Z -m vX.Y.Z <merge-commit>
   git push origin vX.Y.Z
   ```

5. Publish the GitHub release, with the version's `CHANGELOG.md` section as its notes:

   ```sh
   gh release create vX.Y.Z --title vX.Y.Z --notes-file <notes-file>
   ```
