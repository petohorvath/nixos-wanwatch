# CLAUDE.md

Guidance for Claude Code (and any AI-assisted contributor) when working
in this repository. Mirrors the discipline of `nix-libnet` /
`nix-nftzones`.

## What this is

`nixos-wanwatch` is multi-WAN monitoring and failover for NixOS,
delivered as three layers:

- **Pure-Nix library** (`lib/`) — typed values (`wan`, `probe`, `group`,
  `member`), validation, allocators (marks, tables), and pure selection
  logic. Takes `{ lib, libnet }` at import time; uses `nixpkgs.lib`
  freely. NixOS option types always available at `wanwatch.types`.
- **NixOS module** (`modules/`) — `services.wanwatch.*` declares wans
  and groups, renders the daemon config, emits the systemd unit.
- **Go daemon** (`daemon/`) — `wanwatchd` probes, decides, mutates
  kernel routing state via netlink, publishes state and metrics.

[`PLAN.md`](./PLAN.md) is the **authoritative** design document for v1.
Re-read it before any non-trivial change; update it (and add a commit
message that says so) when an intended change diverges from it.

## Commands

```sh
nix flake check                              # all checks (unit, integration, vm-on-linux)
nix build .#checks.x86_64-linux.unit         # unit tier only
nix build .#checks.x86_64-linux.integration  # integration tier
nix build .#checks.x86_64-linux.vm           # vm tier (Linux+KVM only)
nix fmt                                       # treefmt-nix: nixfmt + gofumpt + goimports
nix fmt -- --fail-on-change                   # CI gate: fail if anything would be reformatted
nix develop                                   # devshell with go, gopls, golangci-lint, etc.
```

Inside `daemon/`:

```sh
go test ./...                  # all Go tests
go test -cover ./internal/...  # coverage report
golangci-lint run              # lint
```

## Architecture

See [`PLAN.md`](./PLAN.md) §4 for the canonical diagram. Brief:

```
NixOS config → lib (validation, allocators) → module (renders JSON, emits unit)
                                                         ↓
                                                /etc/wanwatch/config.json
                                                         ↓
                                                   wanwatchd
                                                   ├── probe   (icmp + v6, sliding window)
                                                   ├── rtnl    (link / carrier events)
                                                   ├── selector (pure: Health → Selection)
                                                   ├── apply   (netlink: route, rule, conntrack)
                                                   ├── state   (atomic JSON + hooks)
                                                   └── metrics (Prometheus over Unix socket)
```

## Conventions

### Terminology

Strictly per [`docs/glossary.md`](./docs/glossary.md). Terms are
non-overlapping; reusing them loosely is a defect. Adding a term means
updating the glossary in the same commit.

Key terms: **WAN** (the interface + gateway(s)), **Probe** (the *config*
of how to test), **Sample** (one probe attempt), **Window** (sliding
samples), **Health** (derived status), **Hysteresis** (flap suppression),
**Group**, **Member**, **Strategy**, **Selection** (current chosen
member), **Decision** (a Selection *change*), **Apply** (kernel
mutation), **State** (the externalized snapshot), **Hook** (user
script).

### API skeleton (pure-Nix lib)

Every value type (`wan`, `probe`, `group`, `member`) implements
the same minimal skeleton:

```
make / tryMake / is<T> / toJSONValue / _type
```

Pure-function modules (`selector`, `marks`, `tables`, `config`,
`snippets`) get an explicitly different skeleton (`compute`,
`allocate`, `render`, …) — also documented.

A meta-test in `tests/unit/skeleton.nix` asserts every value type
exports the full skeleton. Failing it on a PR means "you added a type
and forgot a function."

### Testing

Three tiers, all reachable via `nix flake check`:

- **unit** — `lib.runTests` shape (`testFoo = { expr; expected; }`)
  for Nix; standard `_test.go` table-driven tests for Go.
- **integration** — Nix module-evaluation scenarios + rejections.
- **vm** — `pkgs.testers.nixosTest` end-to-end (Linux + KVM only).

Tests live with code: adding `lib/internal/wan.nix` and
`tests/unit/internal/wan.nix` is **one commit**. Per public function, required coverage: happy
path; every `throws` branch; every predicate (positive AND negative);
every boundary; round-trips; total-order properties for `compare`;
determinism for allocators.

Coverage gates per `PLAN.md` §9.2 — CI fails on regression.

### Commits

- One logical change per commit.
- Imperative subject ≤72 chars: `scope: summary`. Scope is one of
  `lib`, `internal`, `types`, `modules`, `daemon`, `tests`, `docs`,
  `ci`, `deps`, `flake`.
- Body explains *why* when not obvious from diff.
- `nix flake check` + `go test ./...` pass at HEAD after the commit.
- Tests live with the code they exercise — same commit.
- Never `--no-verify` / `--no-gpg-sign` unless explicitly requested
  and explained in the body.

### Bottom-up build order

`PLAN.md` §10 defines six passes (foundations → leaves → composites →
orchestration → surfaces → docs). When higher layers reveal that a
lower layer is inadequate, **refactor the lower layer in a dedicated
commit** with updated tests, rather than papering over.

### Modern Nix — flake first

- `flake.nix` is the only entry point. No `default.nix` at root.
- Outputs: `lib.<system>`, `nixosModules.{default,telegraf}`,
  `packages.<system>.{wanwatchd,default}`, `checks.<system>.*`,
  `formatter.<system>`, `devShells.<system>.default`.
- Inputs minimal, each with `.inputs.nixpkgs.follows = "nixpkgs"`.
- No `flake-utils` / `flake-parts`; hand-written `forAllSystems`.
- `flake.lock` committed; updated deliberately in the monthly audit.
- Per-system: `x86_64-linux`, `aarch64-linux`, `x86_64-darwin`,
  `aarch64-darwin`. Daemon Linux-only — gate via
  `lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux`.
- `nixpkgs.lib` is a standard dependency. `lib/` takes
  `{ lib, libnet }` at import time and uses `lib.*` freely. No
  "pure-Nix core" constraint — that pattern fits primitives
  libraries like libnet, not downstream consumers like wanwatch.

### Modern Go

- Go ≥ 1.24. `cmd/<bin>/` + `internal/<pkg>/` layout.
- `context.Context` propagation everywhere; cancellation honored.
- `log/slog` from stdlib for structured logs.
- Errors: `%w` wrapping, `errors.Is` / `errors.As`. Never
  `fmt.Errorf("%v", err)`.
- `any` over `interface{}`. Prefer concrete types over interfaces
  when there's a single implementation.
- Table-driven tests; `t.Helper()` in helpers; `t.Parallel()` where
  safe.
- No `init()` for side effects; no `panic()` outside `main`.

### Formatter

`nix fmt` runs `treefmt-nix` with three programs:

- `nixfmt` — `.nix` files (RFC 166).
- `gofumpt` — `.go` files (stricter `gofmt` superset).
- `goimports` — `.go` imports (group stdlib / external / internal).

CI gate: `nix fmt -- --fail-on-change`. Editor integration: configure
"format on save" with `nixfmt` / `gofumpt` directly.

### Lint

`golangci-lint` with curated checks (see `.golangci.yml`):
`errcheck`, `gosec`, `govet`, `revive`, `staticcheck`, `unused`,
`gocritic`, `gofumpt`, `unparam`, `errorlint`, `bodyclose`, `goconst`,
`prealloc`.

### Audits

Cadence in `PLAN.md` §11.10. Two that bear repeating:

- **Vulnerability scan**: weekly cron + every release.
  `.github/workflows/audit.yml` runs `govulncheck` + `vulnix`.
- **Glossary drift** + **convention drift**: re-read `CLAUDE.md` and
  `docs/glossary.md` against the actual code at every minor release.

## File layout (target — populated bottom-up per PLAN.md §10)

```
flake.nix                          # canonical entrypoint
lib/
  default.nix                      # composes internal + types
  internal/                        # operational code per concept
    default.nix                    # three-tier composition
    primitives.nix                 # generic helpers (hasTag, tryOk/tryErr, …)
    probe.nix · wan.nix
    group.nix · member.nix         # Pass 3
    selector.nix                   # Pass 4
    marks.nix · tables.nix         # Pass 3
    config.nix · snippets.nix      # Pass 4–5
  types/                           # NixOS option types per concept
    default.nix                    # flattens per-type files
    primitives.nix
    probe.nix · wan.nix
    # group.nix · member.nix       # Pass 3
modules/
  wanwatch.nix · telegraf.nix
daemon/
  go.mod · go.sum
  cmd/wanwatchd/main.go
  internal/
    config/ · probe/ · rtnl/ · selector/ · apply/ · state/ · metrics/
pkgs/
  wanwatchd.nix
tests/
  unit/    · integration/  · vm/
docs/
  glossary.md · architecture.md · wan-monitoring.md
  selector.md · nftzones-integration.md · metrics.md
  specs/
    failover.md · daemon-config.md · daemon-state.md
    probe-algorithm.md · prior-art.md
```

## Documentation map

| File | Audience |
|---|---|
| [`PLAN.md`](./PLAN.md) | Authoritative v1 design. Everyone. |
| [`docs/glossary.md`](./docs/glossary.md) | All contributors. Terminology is enforced. |
| `docs/wan-monitoring.md` | Newcomers. The model + quickstart. (Pass 6) |
| `docs/architecture.md` | Integrators / debuggers. Layers + data flow. (Pass 6) |
| `docs/selector.md` | Daemon implementers. Algorithm spec. (Pass 6) |
| `docs/nftzones-integration.md` | Users wiring firewall marks. (Pass 6) |
| `docs/metrics.md` | Observability. Prometheus catalog + Telegraf. (Pass 6) |
| `docs/specs/*` | Design rationale + frozen contracts. (Pass 6) |
