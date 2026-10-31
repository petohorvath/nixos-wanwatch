# The daemon's Go tests, coverage floors, and race detector, run
# hermetically against the vendored modules and combined into one
# entry. The Go test files live beside the Go code in `daemon/`.
{ pkgs, ... }:
let
  runGoCheck = import ../helpers/go-check.nix { inherit pkgs; };
in
pkgs.linkFarm "wanwatch-daemon" {
  tests = runGoCheck {
    name = "wanwatch-daemon-tests";
    script = "go test -v ./...";
  };

  /*
    Per-package coverage floors (AGENTS.md). `cmd/wanwatchd` has the
    lowest floor: its netlink kernel adapter and the run() wiring of
    probers and subscribers need a real kernel, which only the VM tier
    exercises. Its coverage also varies between 72.8% and 73.6%: the
    shutdown tests reach eventLoop's cancelled-context returns only when
    select picks the queued event over ctx.Done, so its floor truncates
    the lowest measurement. Floors track measured coverage: raise them as
    coverage improves, and lower one only with a comment explaining the
    regression.
  */
  coverage = runGoCheck {
    name = "wanwatch-daemon-coverage";
    script = ''
      cat > coverage.thresholds <<'EOF'
      cmd/wanwatchd:72
      internal/apply:90
      internal/config:100
      internal/decision:100
      internal/health:100
      internal/metrics:88
      internal/probe:91
      internal/rtnl:91
      internal/selector:100
      internal/state:94
      EOF

      go test -cover ./cmd/... ./internal/... > coverage.out 2>&1 || {
          cat coverage.out
          echo "coverage: go test failed" >&2
          exit 1
      }
      cat coverage.out

      fail=0
      while IFS=: read -r pkg floor; do
          # Skip blank lines and heredoc indentation.
          pkg=$(echo "$pkg" | tr -d '[:space:]')
          floor=$(echo "$floor" | tr -d '[:space:]')
          [ -z "$pkg" ] && continue

          # Matches lines such as
          #   ok  <module>/<pkg>  0.012s  coverage: 88.6% of ...
          line=$(grep "/$pkg[[:space:]]" coverage.out || true)
          if [ -z "$line" ]; then
              echo "coverage: $pkg — no test output found" >&2
              fail=1
              continue
          fi
          pct=$(echo "$line" |
              sed -n 's/.*coverage: \([0-9.]*\)%.*/\1/p')
          if [ -z "$pct" ]; then
              echo "coverage: $pkg — could not parse line: $line" >&2
              fail=1
              continue
          fi
          # awk compares the fractional percentages.
          if awk -v p="$pct" -v f="$floor" \
              'BEGIN{ exit !(p+0 < f+0) }'; then
              printf 'coverage: %-22s %5s%% < floor %s%% — FAIL\n' \
                  "$pkg" "$pct" "$floor" >&2
              fail=1
          else
              printf 'coverage: %-22s %5s%% ≥ floor %s%% — ok\n' \
                  "$pkg" "$pct" "$floor"
          fi
      done < coverage.thresholds

      if [ "$fail" -ne 0 ]; then
          echo "coverage: one or more packages regressed below" \
              "their floor" >&2
          exit 1
      fi
    '';
  };

  race = runGoCheck {
    name = "wanwatch-daemon-race";
    cgo = true;
    script = "go test -race -timeout 120s ./...";
  };
}
