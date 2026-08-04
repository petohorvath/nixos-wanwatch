#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 1 ]]; then
  printf 'usage: %s RUNTIME_DERIVERS\n' "$0" >&2
  exit 64
fi

runtime_derivers="$1"
if [[ ! -s "$runtime_derivers" ]]; then
  printf 'runtime derivation file is missing or empty: %s\n' "$runtime_derivers" >&2
  exit 66
fi

max_attempts="${VULNIX_MAX_ATTEMPTS:-3}"
retry_delay="${VULNIX_RETRY_DELAY_SECONDS:-15}"

if [[ ! "$max_attempts" =~ ^[1-9][0-9]*$ ]]; then
  printf 'VULNIX_MAX_ATTEMPTS must be a positive integer, got: %s\n' "$max_attempts" >&2
  exit 64
fi
if [[ ! "$retry_delay" =~ ^[0-9]+$ ]]; then
  printf 'VULNIX_RETRY_DELAY_SECONDS must be a non-negative integer, got: %s\n' "$retry_delay" >&2
  exit 64
fi

log_dir="$(mktemp -d)"
trap 'rm -rf "$log_dir"' EXIT

is_transient_nvd_failure() {
  local log_file="$1"

  LC_ALL=C grep -Eq \
    'requests\.exceptions\.(ConnectionError|ReadTimeout)|urllib3\.exceptions\.(ReadTimeoutError|ProtocolError|MaxRetryError|NewConnectionError)|TimeoutError: The read operation timed out|Temporary failure in name resolution|Connection (reset|timed out)|Remote end closed connection' \
    "$log_file"
}

for ((attempt = 1; attempt <= max_attempts; attempt++)); do
  log_file="$log_dir/attempt-$attempt.log"

  set +e
  vulnix --no-requisites --from-file "$runtime_derivers" 2>&1 | tee "$log_file"
  pipeline_status=("${PIPESTATUS[@]}")
  set -e

  scanner_status="${pipeline_status[0]}"
  tee_status="${pipeline_status[1]}"
  if (( tee_status != 0 )); then
    printf 'failed to preserve vulnix output (tee status %s)\n' "$tee_status" >&2
    exit "$tee_status"
  fi
  if (( scanner_status == 0 )); then
    exit 0
  fi

  # Vulnerability findings and parser/configuration failures must fail
  # immediately. Retry only recognizable transient network failures from
  # vulnix's NVD refresh path.
  if ! is_transient_nvd_failure "$log_file"; then
    exit "$scanner_status"
  fi

  if (( attempt == max_attempts )); then
    printf 'vulnix failed after %s transient attempts.\n' "$max_attempts" >&2
    exit "$scanner_status"
  fi

  printf 'Transient NVD download failure on attempt %s/%s; retrying in %ss.\n' \
    "$attempt" "$max_attempts" "$retry_delay" >&2
  sleep "$retry_delay"
done
