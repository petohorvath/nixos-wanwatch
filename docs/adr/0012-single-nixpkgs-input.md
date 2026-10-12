# One nixpkgs input, with unstable as a CI override

The flake has one nixpkgs input, `nixpkgs`, pinned to the stable `nixos-26.05` branch. The `nixpkgs-unstable` input is removed, together with the `vm-unstable-*` checks it built. CI tests against unstable by building the same `vm-*` checks with `--override-input nixpkgs github:NixOS/nixpkgs/<rev>`, where `<rev>` is the current `nixos-unstable` revision that the job resolves at the start of each run. A second input cost a lock node in every consumer's `flake.lock`, a second set of check names, and a CI guard to keep the `vm-` prefix from matching `vm-unstable-`. It also needed lock updates by hand, because Dependabot updates only `nixpkgs`. [ADR-0011](./0011-flake-parts-with-dev-partition.md) still keeps the other development inputs in the root flake.

## Considered options

- **Pinned unstable revision in `ci.yml`.** This makes runs repeatable, but someone has to update the revision by hand, which is the work this change removes. The override floats instead. Upstream drift can therefore fail the unstable VM job without a change in this repository. On pull requests, the job is `continue-on-error` and must not be a required check, so drift cannot block a merge. On `main` and on a weekly schedule, a failure turns the run red.

## Consequences

The weekly vulnerability audit workflow (`audit.yml`) and its `audit` development shell are removed for now. The audit shell took `govulncheck` and `vulnix` from `nixpkgs-unstable`, and choosing a new scanner source is deferred to the audit's restoration; see `TODO.md`.
