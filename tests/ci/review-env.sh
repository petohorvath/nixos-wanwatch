#!/usr/bin/env bash
# Exercise the real review wrapper and toolchain, including command failures.
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
wrapper="$repo_root/tooling/review-env.sh"
test_dir=$(mktemp -d -t wanwatch-review-test.XXXXXXXX)
trap 'rm -rf -- "$test_dir"' EXIT

# Starting below the repository root and passing arguments with spaces must
# still run the requested command against this checkout and its vendored code.
cd -- "$repo_root/daemon"
bash "$wrapper" bash -euo pipefail -c '
  [[ $PWD == "$1" ]]
  [[ $2 == "argument with spaces" && $3 == "" ]]
  [[ $(go env GOTOOLCHAIN) == local ]]
  [[ $(go env GOFLAGS) == -mod=vendor ]]
  [[ $(go env GOPROXY) == off && $(go env GOSUMDB) == off ]]
  [[ $(go env CGO_ENABLED) == 1 ]]
  cd daemon
  go test -race -count=1 -timeout 120s ./internal/state ./cmd/wanwatchd
' review-env-test "$repo_root" 'argument with spaces' ''

# A test failure must reach the reviewer unchanged, rather than being reported
# as either successful setup or an environment blocker.
status=0
bash "$wrapper" bash -c 'exit 42' >"$test_dir/exit.log" 2>&1 || status=$?
if [[ $status != 42 ]]; then
  cat "$test_dir/exit.log" >&2
  printf 'review command returned %s, expected 42\n' "$status" >&2
  exit 1
fi

status=0
bash "$wrapper" >"$test_dir/usage.log" 2>&1 || status=$?
if [[ $status != 2 ]]; then
  cat "$test_dir/usage.log" >&2
  printf 'missing command returned %s, expected 2\n' "$status" >&2
  exit 1
fi

printf 'Review environment contract passed.\n'
