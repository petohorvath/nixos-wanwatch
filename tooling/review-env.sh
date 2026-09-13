#!/usr/bin/env bash
# Run review commands with the locked Go toolchain and vendored dependencies.
# --install-nix is opt-in and intended for disposable Linux review sandboxes.
set -euo pipefail

blocked() {
  printf 'Review environment blocked: %s\n' "$*" >&2
  exit 125
}

install_nix=false
if [[ ${1:-} == --install-nix ]]; then
  install_nix=true
  shift
fi
if [[ $# == 0 ]]; then
  printf 'Usage: bash tooling/review-env.sh [--install-nix] COMMAND [ARG...]\n' >&2
  exit 2
fi
case "$(uname -s).$(uname -m)" in
  Linux.x86_64|Linux.aarch64) ;;
  *) blocked 'daemon review requires x86_64-linux or aarch64-linux' ;;
esac

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd -- "$repo_root"

# Non-login shells often miss an existing Nix installation.
if ! command -v nix >/dev/null 2>&1; then
  export PATH="$HOME/.nix-profile/bin:/nix/var/nix/profiles/default/bin:$PATH"
fi
export NIX_CONFIG="${NIX_CONFIG:-}"$'\nextra-experimental-features = nix-command flakes'

# A root-only, daemonless container builds as its invoking user. These
# settings apply only to this process tree inside the disposable sandbox.
if [[ $EUID == 0 && ! -S /nix/var/nix/daemon-socket/socket ]]; then
  export NIX_CONFIG="$NIX_CONFIG"$'\nbuild-users-group =\nsandbox = false'
fi

if ! command -v nix >/dev/null 2>&1; then
  [[ $install_nix == true ]] || blocked 'Nix is missing; in a disposable sandbox, rerun with --install-nix'
  for tool in curl xz tar sha256sum; do
    command -v "$tool" >/dev/null 2>&1 || blocked "install prerequisite: $tool (see .greptile/rules.md)"
  done
  if [[ ! -e /nix ]]; then
    if [[ $EUID == 0 ]]; then
      mkdir -m 0755 /nix
    elif command -v sudo >/dev/null 2>&1 && sudo -n true; then
      sudo -n install -d -m 0755 -o "$(id -u)" -g "$(id -g)" /nix
    else
      blocked 'an administrator must create /nix owned by the current user'
    fi
  fi
  [[ -w /nix ]] || blocked '/nix exists but is not writable; reuse its Nix installation or ask its administrator'

  # Reuse nix-nftypes' checksum-pinned official upstream Nix bootstrap.
  # The installer also verifies its platform-specific binary archive.
  installer_dir=$(mktemp -d -t wanwatch-review-nix.XXXXXXXX)
  trap 'rm -f -- "$installer_dir/install"; rmdir -- "$installer_dir"' EXIT
  curl --proto '=https' --tlsv1.2 -fsSL \
    https://releases.nixos.org/nix/nix-2.34.6/install \
    -o "$installer_dir/install" || blocked 'could not download the upstream Nix installer'
  printf '%s  %s\n' \
    bf6d12da4aeaae38ab509dc736df648597bcdee051046ea76d3d53d5b18fa54a \
    "$installer_dir/install" | sha256sum --check --status || blocked 'upstream Nix installer checksum mismatch'
  sh "$installer_dir/install" --no-daemon --yes --no-channel-add --no-modify-profile \
    || blocked 'upstream Nix installation failed'
fi

command -v nix >/dev/null 2>&1 || blocked 'the installer did not provide a Nix executable'
nix --version
# shellcheck disable=SC2016 # Expand variables inside the locked review shell.
nix develop --no-write-lock-file .#review --command bash -euo pipefail -c '
  go version
  # Reading the module drives the Go version gate without fetching modules.
  # A stale sandbox Go must fail here, before we claim setup succeeded.
  (cd daemon && go list -m)
  if ! gcc -dumpversion || [[ $(go env CGO_ENABLED) != 1 ]]; then
    echo "Review environment blocked: race tests require GCC and CGO_ENABLED=1." >&2
    exit 125
  fi
  echo "Review environment ready: locked Go, GCC, and vendored modules."
  exec "$@"
' review-env "$@"
