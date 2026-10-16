# Adopt the shared project policy

CI calls the shared [nixos-project-policy](https://github.com/petohorvath/nixos-project-policy) workflow at `v0.6`, as the sibling projects do, instead of running the flake checks in its own jobs. On x86_64 and aarch64 Linux, the policy checks the inputs, the public outputs, the development shell, and the formatter. It builds every check with the locked nixpkgs and with the stable and unstable pins of the policy release. The `vm-*` checks run in the policy's VM test jobs, which skip them on a runner without KVM. `ci.yml` keeps only the jobs that need the network or run outside the Nix sandbox: Go module verification and the review-environment contract test, `tooling/review-env-test.sh`.

This replaces the CI half of [ADR-0012](./0012-single-nixpkgs-input.md). The flake still has one nixpkgs input, and the unstable run still overrides it, but the policy's unstable pin replaces the floating `nixos-unstable` revision and the weekly schedule. Policy patch releases move the pins, so a pin bump reaches this repository without a change here, and a failure on the unstable pin blocks a merge like any other required status. Before, only x86_64 ran the VM scenarios, with the locked nixpkgs and the floating revision.

The policy requires inputs from other `petohorvath` repositories to reference release tags. `libnet` and the `nftypes` input of `nftzones` reference their `v0.1.0` tags. `nixos-nftzones` has no release yet, so `nftzones` is pinned to its locked commit, which the policy accepts as temporary.

## Consequences

Branch protection on `main` requires the `Policy / Check (<system>)`, `Policy / Tests (locked|stable|unstable, <system>)`, and `Policy / VM tests (locked|stable|unstable, <system>)` statuses that the policy's `Plan` job lists, in place of the old CI job names.

A VM test job passes when its runner lacks KVM and skips every scenario; the old VM job failed instead. The job's report lists the skipped scenarios, so a skip is visible but not a failure.
