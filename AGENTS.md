# AGENTS.md

Contributor instructions for people and automated agents working in this repository.

## Project

`nixos-wanwatch` provides multi-WAN monitoring and single-active failover for NixOS. Version `0.1.0` shipped the v1 design. [`CHANGELOG.md`](./CHANGELOG.md) records unreleased changes, including public-API breaks, and [`TODO.md`](./TODO.md) is the deferred-work backlog.

It has three layers, with dependencies pointing bottom-up only:

- `lib/`: a pure-Nix library of validated WAN, Probe, Member, and Group values, option types, the selector mirror, and daemon-config rendering. It never depends on the module or the daemon.
- `modules/`: the thin `services.wanwatch` NixOS module and the optional Telegraf integration.
- `daemon/`: the Linux-only Go `wanwatchd`. Its packages under `daemon/internal/` stay private, and `daemon/vendor/` is vendored code that is never edited or reformatted by hand.

The runtime path is: NixOS configuration → `lib` validation and rendering → `/etc/wanwatch/config.json` → `wanwatchd` consumes Probe and rtnetlink events → `decision` computes Selections with the pure selector → `apply` mutates routes, rules, and conntrack → State, Hooks, and metrics publish the result.

## Design records

[`docs/adr/`](./docs/adr/) records the architectural decisions, [`docs/specs/`](./docs/specs/) the public contracts, and [`GLOSSARY.md`](./GLOSSARY.md) the terminology. Changes to architecture, public contracts, compatibility, migration, security boundaries, rollback, or recovery need explicit approval; record an approved change in a new ADR or in the affected spec, in the same commit as the code.

Before changing a contract, read its sources and documentation together:

| Contract | Sources and documentation |
|---|---|
| Nix options and outputs | `lib/types/`, `modules/`, `README.md` |
| Daemon config JSON | `lib/internal/config.nix`, `daemon/internal/config/`, `docs/specs/daemon-config.md` |
| State and Hooks | `daemon/internal/state/`, `docs/specs/daemon-state.md` |
| Selection and failover | the Nix and Go selectors, `docs/selector.md`, `docs/specs/failover.md` |
| Metrics | `daemon/internal/metrics/`, `docs/metrics.md` |
| Firewall composition | `docs/nftzones-integration.md` |

Keep schema versions synchronized across producers, consumers, tests, and specs. Plaintext secrets stay out of source, Nix expressions, command lines, and the Nix store.

## Terminology

Use the [`GLOSSARY.md`](./GLOSSARY.md) terms exactly in code, comments, commit messages, error kinds, metric names, Hook variables, and docs. In particular, a Probe is configuration, a Sample is one attempt, Health is the derived WAN status, a Selection is the chosen Member, a Decision is a Selection change, and Apply is kernel mutation. Add or revise a glossary entry in the commit that introduces the concept.

## Development environment

The flake is the only entry point. `nix develop` provides Go, gopls, golangci-lint, statix, deadnix, and the treefmt wrapper, and installs the pre-commit and pre-push hooks. The daemon and Linux-specific checks exist only on Linux systems.

Override sibling flakes with absolute paths:

```sh
nix build --override-input libnet path:/absolute/path/to/nix-libnet \
  .#checks.x86_64-linux.unit
```

## Formatting and linting

`nix fmt` runs treefmt (`nixfmt`, `gofumpt`, `goimports`). golangci-lint (`.golangci.yml`, v2 format) checks lint only; formatting failures belong to treefmt.

```sh
nix fmt
nix develop --command statix check .
nix develop --command deadnix --fail .
nix develop --command bash -c 'cd daemon && go vet ./... && golangci-lint run ./...'
git diff --check
```

## Tests and checks

Run the smallest checks that exercise the change first. The examples use `x86_64-linux`.

| Check | Covers |
|---|---|
| `unit` | Pure-Nix library tests, including the value-type skeleton tests |
| `integration` | Module evaluation, rendered config, Telegraf wiring, and expected rejections |
| `daemon` | All Go tests in a hermetic, network-disabled build |
| `coverage` | Per-package Go coverage floors defined in `flake.nix` |
| `race` | All Go tests with the race detector |
| `package` | The production daemon derivation |
| `vm-<scenario>`, `vm-unstable-<scenario>` | NixOS VM scenarios on stable and unstable nixpkgs (Linux with KVM) |

The `unit` and `integration` checks run the flake's `tests` output with [nix-unit](https://github.com/nix-community/nix-unit) in the sandbox. For per-test results, run nix-unit directly; `nix-unit --flake .#tests.unit` narrows the run.

```sh
nix develop --command nix-unit --flake .#tests
nix build .#checks.x86_64-linux.unit
nix build .#checks.x86_64-linux.vm-smoke
cd daemon && go test -race -timeout 120s ./...
```

`nix flake check` on Linux runs every check, including the full VM matrix. It is resource-intensive; report it as passed only when it actually ran.

CI runs formatter drift, `tests/ci/` helper contracts, Go module verification, the unit, daemon, package, coverage, race, and integration checks on x86_64 and aarch64 Linux, and every VM scenario on x86_64 Linux against both nixpkgs channels. The pre-push hook runs golangci-lint when `daemon/**/*.go` changed and, on Linux, the unit, integration, race, and coverage checks, but no VM scenarios.

## Conventions

Every Nix value type (`wan`, `probe`, `group`, `member`) implements the `make`, `tryMake`, and `toJSONValue` skeleton that `tests/unit/skeleton.nix` asserts. Pure-function modules such as `selector` and `config` use purpose-specific APIs.

Nix tests are nix-unit attrsets of `testFoo = { expr; expected; }` (or `expectedError`), nested by module. Shared inputs and the valid and invalid cases of each value domain live in `tests/unit/fixtures.nix`; reuse a case table wherever an option type and a validator check the same domain, so the two layers stay in agreement.

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

| Audit | Cadence | Mechanism |
|---|---|---|
| Vulnerabilities | Weekly and on release tags | `.github/workflows/audit.yml` runs pinned `govulncheck` and `vulnix` |
| Primary nixpkgs | Monthly | Dependabot opens a review-only PR for the `nixpkgs` input; merge after reviewing the lock delta and CI |
| Other dependencies | Monthly | Manual review of `go.mod` and other `flake.lock` deltas |
| Public API, glossary, and convention drift | Each minor version | Manual review of `lib/default.nix`, daemon exports, `GLOSSARY.md`, and this file against the code |

## Documentation map

- [`README.md`](./README.md): overview, quickstart, and commands.
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

Hooks and signing stay on; `--no-verify` and `--no-gpg-sign` need explicit authorization for that commit and an explanation. Before handoff, inspect the final diff, list exactly which checks ran, distinguish focused checks from the full flake and VM matrix, and report every failure or skipped gate.
