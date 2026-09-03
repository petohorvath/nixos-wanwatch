# AGENTS.md

Tool-neutral contributor instructions for people and automated agents working in this repository.

## Project state

`nixos-wanwatch` provides multi-WAN monitoring and single-active failover for NixOS. The `0.1.0` release completed the v1 design, while [`CHANGELOG.md`](./CHANGELOG.md) records unreleased changes, including current public-API breaks. [`TODO.md`](./TODO.md) is the deferred-work backlog. Do not describe completed source as a future build pass merely because [`PLAN.md`](./PLAN.md) retains the original milestone history.

The repository has three implementation layers:

- `lib/`: a pure-Nix library for validated WAN, Probe, Member, and Group values, option types, the selector mirror, and daemon-config rendering. Groups carry user-declared marks and routing-table IDs; automatic allocation has been removed.
- `modules/`: the main `services.wanwatch` NixOS module and the optional Telegraf integration.
- `daemon/`: the Linux-only Go `wanwatchd` process. It probes configured families, consumes link and route netlink events, computes Selections, applies routing state, and publishes State, Hooks, and Prometheus metrics.

`PLAN.md` is the authoritative v1 design record. Current source, contract documentation, `CHANGELOG.md`, and `TODO.md` capture post-release changes. Update the relevant design and contract documents whenever an approved change alters their claims; do not silently change architecture or a public contract in code.

## Repository layout

```text
flake.nix                 flake inputs, outputs, checks, hooks, and dev shells
flake.lock                pinned dependency graph
lib/
  default.nix             public library composition and version
  internal/               values, validation, selector, config renderer
  types/                  flattened NixOS option types
modules/
  wanwatch.nix            services.wanwatch module and systemd service
  telegraf.nix            optional metrics-scraping integration
daemon/
  cmd/wanwatchd/          process lifecycle and event loop
  internal/               apply, config, metrics, probe, rtnl, selector, state
  vendor/                 vendored Go dependencies; do not reformat manually
pkgs/wanwatchd.nix        daemon package
tests/
  unit/                   pure-Nix lib.runTests suite
  integration/            module-evaluation scenarios and rejection cases
  vm/                     NixOS VM scenarios
  ci/                     shell tests for CI helpers
docs/                     user, architecture, integration, and metrics guides
docs/specs/               daemon and failover contracts
```

## Development environment

The flake is the only repository entry point. It supports `x86_64-linux`, `aarch64-linux`, `x86_64-darwin`, and `aarch64-darwin`; the daemon package and Linux-specific checks are emitted only on Linux.

```sh
nix develop
nix build .#wanwatchd
```

Entering `nix develop` supplies Go, gopls, golangci-lint, statix, deadnix, and the treefmt wrapper. It also installs the repository's pre-commit and pre-push hooks. The Go module currently declares Go 1.25 and keeps external dependencies in `daemon/vendor/`.

For local work on sibling flakes, use absolute overrides rather than relative paths:

```sh
nix build --override-input libnet path:/absolute/path/to/nix-libnet \
  .#checks.x86_64-linux.unit
```

## Formatting and linting

`nix fmt` runs treefmt with `nixfmt`, `gofumpt`, and `goimports`. It excludes vendored Go code, lock files, Nix result links, and `.direnv`.

```sh
nix fmt
nix build .#checks.x86_64-linux.format
nix develop --command statix check .
nix develop --command deadnix --fail .
nix develop --command bash -c 'cd daemon && go vet ./...'
nix develop --command bash -c 'cd daemon && golangci-lint run ./...'
git diff --check
```

`.golangci.yml` is a golangci-lint v2 configuration. Formatting is intentionally enforced by treefmt rather than golangci-lint. Keep those failure classes separate.

## Tests and checks

Run the smallest checks that exercise the change first. The following examples use `x86_64-linux`; substitute the current flake system when necessary.

```sh
nix build .#checks.x86_64-linux.unit
nix build .#checks.x86_64-linux.integration
nix build .#checks.x86_64-linux.daemon
nix build .#checks.x86_64-linux.coverage
nix build .#checks.x86_64-linux.race
nix build .#checks.x86_64-linux.package
```

Inside `daemon/`, focused Go commands are also available:

```sh
go test ./...
go test -cover ./internal/...
go test -race -timeout 120s ./...
```

The check tiers are distinct:

- `unit`: pure-Nix library tests, including the value-type API skeleton.
- `integration`: NixOS module evaluation, rendered configuration, Telegraf wiring, and expected rejection cases.
- `daemon`: all Go tests in a hermetic vendored, network-disabled Nix build.
- `coverage`: per-package Go coverage floors defined in `flake.nix`.
- `race`: all Go tests with the race detector and cgo enabled.
- `package`: the production daemon derivation.
- `vm-<scenario>` and `vm-unstable-<scenario>`: each NixOS VM scenario against stable and unstable nixpkgs, respectively. These require Linux and KVM.

Run an individual VM scenario when the changed boundary requires it:

```sh
nix build .#checks.x86_64-linux.vm-smoke
nix build .#checks.x86_64-linux.vm-unstable-smoke
```

On Linux, `nix flake check` includes every stable and unstable VM scenario as well as the non-VM checks. It is the complete local contract but is resource-intensive; do not imply it passed when only focused checks ran. On Darwin, Linux-only package, daemon, integration, race, coverage, and VM checks are absent.

Current CI runs:

- formatter drift and the `tests/ci/` helper contracts;
- Go module verification and tidy checks;
- unit, daemon/package/coverage/race, and integration jobs on x86_64 and aarch64 Linux;
- every VM scenario on x86_64 Linux against both stable and unstable nixpkgs.

The pre-push hook runs golangci-lint; on Linux it also runs unit, integration, race, and coverage checks. It does not run the VM matrix. The separate audit workflow runs `govulncheck` and `vulnix` weekly and for release tags.

## Architecture and change boundaries

The runtime path is:

```text
NixOS configuration
  -> lib validation and daemon-config rendering
  -> modules/wanwatch.nix writes /etc/wanwatch/config.json
  -> wanwatchd consumes Probe and rtnetlink events
  -> selector computes a Selection
  -> apply mutates route/rule/conntrack state
  -> State, Hooks, and metrics expose the result
```

Keep dependency direction bottom-up. Do not make the Nix library depend on the module or daemon. Keep `modules/` thin and keep the Go packages under `daemon/internal/` private.

Before changing a public or operational contract, read the corresponding sources and documentation:

- Nix option or output surface: `lib/types/`, `modules/`, `README.md`.
- Daemon config JSON: `lib/internal/config.nix`, `daemon/internal/config/`, `docs/specs/daemon-config.md`.
- State or Hook contract: `daemon/internal/state/`, `docs/specs/daemon-state.md`.
- Selection or failover semantics: both Nix and Go selectors, `docs/selector.md`, `docs/specs/failover.md`.
- Metrics: `daemon/internal/metrics/`, `docs/metrics.md`.
- Firewall composition: `docs/nftzones-integration.md`.

Changes to architecture, public contracts, compatibility, migration, security boundaries, rollback, or recovery require explicit approval. Keep schema versions synchronized across producers, consumers, tests, and specs. Never place plaintext secrets in source, Nix expressions, command lines, or the Nix store.

## Terminology and implementation conventions

Use the definitions in [`docs/glossary.md`](./docs/glossary.md) exactly. In particular, a Probe is configuration, a Sample is one attempt, Health is the derived WAN status, a Selection is the current chosen Member, a Decision is a Selection change, and Apply is kernel mutation. Add or revise glossary entries with the change that introduces the concept.

Every Nix value type (`wan`, `probe`, `group`, `member`) follows the common `make`, `tryMake`, and `toJSONValue` skeleton. Preserve the meta-test in `tests/unit/skeleton.nix`. Pure-function modules such as `selector` and `config` use purpose-specific APIs instead.

Use current Nix APIs and explicit dependencies. Avoid `with` at file scope, removed `lib.types.uniq`, and `lib.types.either` where `lib.types.oneOf` is appropriate. NixOS options need a type, description, and a default or example.

For Go, propagate `context.Context`, honor cancellation, use `log/slog`, wrap errors with `%w`, and inspect them with `errors.Is` or `errors.As`. Prefer concrete types when only one implementation exists. Use table-driven tests, `t.Helper()` in helpers, `testing.TB` in shared helpers, and `t.Parallel()` only when safe. Avoid side-effecting `init` functions and panic outside `main`.

Tests belong with the behavior they protect. Exercise happy paths, rejection paths, boundary values, deterministic behavior, and serialization round trips where applicable. Do not weaken coverage floors, lint rules, security hardening, or CI gates merely to obtain a pass.

## Documentation map

- [`README.md`](./README.md): user overview, quickstart, and top-level commands.
- [`PLAN.md`](./PLAN.md): authoritative v1 design, historical build plan, and conventions.
- [`CHANGELOG.md`](./CHANGELOG.md): released and unreleased behavior changes and migrations.
- [`TODO.md`](./TODO.md): deferred work and known cleanup.
- [`docs/glossary.md`](./docs/glossary.md): authoritative terminology.
- [`docs/wan-monitoring.md`](./docs/wan-monitoring.md): conceptual introduction.
- [`docs/architecture.md`](./docs/architecture.md): layers and data flow.
- [`docs/selector.md`](./docs/selector.md): Strategy and Hysteresis behavior.
- [`docs/nftzones-integration.md`](./docs/nftzones-integration.md): firewall integration.
- [`docs/metrics.md`](./docs/metrics.md): Prometheus catalog and Telegraf usage.
- [`docs/specs/`](./docs/specs/): daemon-config, daemon-state, failover, probe-algorithm, and prior-art documents.

## Contributions

Keep each commit to one logical change. Use an imperative subject of at most 72 characters in the form `scope: summary`; accepted scopes are `lib`, `internal`, `types`, `modules`, `daemon`, `tests`, `docs`, `ci`, `deps`, and `flake`. Explain why in the body when the diff is not self-explanatory. Keep tests and documentation in the same commit as the behavior they cover.

Do not bypass hooks or signing with `--no-verify` or `--no-gpg-sign` unless explicitly authorized for that commit and explained. Before handoff, inspect the final diff, record exactly which checks ran, distinguish focused checks from the full flake/VM matrix, and report every failure or skipped gate.
