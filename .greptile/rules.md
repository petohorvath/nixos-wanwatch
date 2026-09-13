# Executable review environment

For T-Rex validation, run Go commands through `bash tooling/review-env.sh`
from the checkout. The wrapper supplies Go and GCC from `flake.lock`, checks
the module's Go version requirement, and uses vendored dependencies with
toolchain and module downloads disabled. The sandbox's preinstalled Go may
be too old for `daemon/go.mod`.

Prepare the environment with standard upstream Nix (NixOS/nix):

```sh
bash tooling/review-env.sh true
```

In a disposable Linux sandbox without Nix, use the opt-in bootstrap:

```sh
bash tooling/review-env.sh --install-nix true
```

Bootstrap requires Bash, CA certificates, curl, xz, tar, and sha256sum.
On bare Debian/Ubuntu, install missing prerequisites with
`apt-get update && apt-get install -y ca-certificates curl xz-utils` (use
`sudo -n` when needed). Creating `/nix` requires root, passwordless sudo, or
an administrator-prepared directory owned by the current user. Setup needs
network access to the official Nix releases, binary cache, and locked flake
inputs. Run setup while downloads are available; subsequent Go tests use
the installed toolchain and vendored modules.

After setup succeeds, use the wrapper for every focused check or custom
reproduction. It runs commands from the repository root. For Hook delivery:

```sh
bash tooling/review-env.sh bash -c \
  'cd daemon && go test -race -count=1 -timeout 120s ./internal/state ./cmd/wanwatchd'
```

For all Go tests, use `cd daemon && go test -race -count=1 -timeout 120s ./...`
as the wrapped command. For Nix library or module changes, select the `unit`
or `integration` checks from `flake.nix`. VM checks require `/dev/kvm` and
more resources than focused Go validation; report unavailable VM execution
separately from checks that ran.

Success requires the requested validation command to exit zero. The wrapper
preserves its exit code; exit 125 reports a detected environment blocker.
Include the exact command, toolchain version, and observed output in review
artifacts. If installation or downloads are denied, report that blocker and
the checks left unexecuted. Keep `go.mod`, `go.sum`, `vendor/`, and
`flake.lock` intact during setup. Cached Nix builds are prior evidence;
use fresh Go runs (`-count=1`) or `nix build --rebuild` when execution of a
reproduction is needed.
