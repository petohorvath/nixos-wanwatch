#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
subject="$repo_root/.github/scripts/retry-vulnix.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/bin" "$tmp/state"
derivers="$tmp/runtime-derivers"
printf '%s\n' '/nix/store/example.drv' > "$derivers"

cat > "$tmp/bin/vulnix" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 3 || "$1" != "--no-requisites" || "$2" != "--from-file" ]]; then
  printf 'unexpected arguments:' >&2
  printf ' %q' "$@" >&2
  printf '\n' >&2
  exit 97
fi

state_file="$FAKE_STATE_DIR/$FAKE_CASE"
attempt=0
if [[ -f "$state_file" ]]; then
  read -r attempt < "$state_file"
fi
attempt=$((attempt + 1))
printf '%s\n' "$attempt" > "$state_file"

case "$FAKE_CASE" in
  transient-then-success)
    if (( attempt < 3 )); then
      printf '%s\n' \
        "requests.exceptions.ConnectionError: HTTPSConnectionPool(host='nvd.nist.gov', port=443): Read timed out." >&2
      exit 1
    fi
    printf '%s\n' 'Found no advisories. Excellent!'
    ;;
  always-transient)
    printf '%s\n' 'urllib3.exceptions.ReadTimeoutError: NVD read timed out.' >&2
    exit 1
    ;;
  vulnerability)
    printf '%s\n' 'CVE-2099-0001: vulnerable-package-1.0' >&2
    exit 1
    ;;
  unexpected)
    printf '%s\n' 'database parse failed' >&2
    exit 42
    ;;
  *)
    printf 'unknown fake case: %s\n' "$FAKE_CASE" >&2
    exit 98
    ;;
esac
FAKE
chmod +x "$tmp/bin/vulnix"

failures=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  failures=$((failures + 1))
}

run_case() {
  local name="$1"
  local expected_status="$2"
  local expected_attempts="$3"
  local expected_text="$4"
  local forbidden_text="$5"
  local output status attempts

  rm -f "$tmp/state/$name"
  set +e
  output="$(
    PATH="$tmp/bin:$PATH" \
      FAKE_STATE_DIR="$tmp/state" \
      FAKE_CASE="$name" \
      VULNIX_MAX_ATTEMPTS=3 \
      VULNIX_RETRY_DELAY_SECONDS=0 \
      "$subject" "$derivers" 2>&1
  )"
  status=$?
  set -e

  attempts=0
  if [[ -f "$tmp/state/$name" ]]; then
    read -r attempts < "$tmp/state/$name"
  fi

  [[ "$status" -eq "$expected_status" ]] || \
    fail "$name: expected status $expected_status, got $status; output: $output"
  [[ "$attempts" -eq "$expected_attempts" ]] || \
    fail "$name: expected $expected_attempts attempts, got $attempts; output: $output"
  [[ "$output" == *"$expected_text"* ]] || \
    fail "$name: missing expected text '$expected_text'; output: $output"
  if [[ -n "$forbidden_text" && "$output" == *"$forbidden_text"* ]]; then
    fail "$name: unexpectedly contained '$forbidden_text'; output: $output"
  fi
}

run_case transient-then-success 0 3 \
  'Transient NVD download failure on attempt 2/3; retrying in 0s.' ''
run_case always-transient 1 3 \
  'vulnix failed after 3 transient attempts.' ''
run_case vulnerability 1 1 \
  'CVE-2099-0001: vulnerable-package-1.0' 'retrying in'
run_case unexpected 42 1 \
  'database parse failed' 'retrying in'

set +e
missing_output="$($subject "$tmp/missing-derivers" 2>&1)"
missing_status=$?
set -e
[[ "$missing_status" -ne 0 ]] || fail 'missing input: expected non-zero status'
[[ "$missing_output" == *'runtime derivation file is missing or empty:'* ]] || \
  fail "missing input: unexpected output: $missing_output"

if (( failures > 0 )); then
  printf '%s\n' "$failures retry test(s) failed" >&2
  exit 1
fi

printf '%s\n' 'vulnix retry tests passed'
