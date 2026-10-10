# The daemon's Go tests, coverage floors, race detector, and `go vet`,
# run hermetically against the vendored modules.
{ pkgs }:
let
  /*
    A sandboxed `go test` run over `daemon/`. The vendored modules and
    disabled proxy make any network access fail. Go refuses a go.mod
    directly in the build's temporary root, so the script copies the
    source into a subdirectory first.

    `name`: the derivation name.
    `cgo`: whether to enable cgo and add gcc, as `-race` requires.
    `script`: shell commands run in the source copy.

    Returns a derivation that builds when `script` succeeds.
  */
  runGoTests =
    {
      name,
      cgo ? false,
      script,
    }:
    pkgs.runCommand name
      {
        src = ../daemon;
        nativeBuildInputs = [ pkgs.go ] ++ pkgs.lib.optional cgo pkgs.gcc;
        GOFLAGS = "-mod=vendor";
        GOPROXY = "off";
        GOSUMDB = "off";
        # Only `-race` needs cgo; netns, the one cgo dependency, is
        # unreachable from wanwatch.
        CGO_ENABLED = if cgo then "1" else "0";
      }
      ''
        export HOME=$TMPDIR
        export GOCACHE=$TMPDIR/gocache
        mkdir -p source
        cp -r $src/* source/
        chmod -R u+w source
        cd source
        ${script}
        touch $out
      '';
in
{
  daemon = runGoTests {
    name = "wanwatch-daemon-tests";
    script = "go test -v ./...";
  };

  /*
    Per-package coverage floors (AGENTS.md); `cmd/wanwatchd` is
    exempt because the VM tier exercises it. Floors track measured
    coverage: raise them as coverage improves, and lower one only
    with a comment explaining the regression.
  */
  coverage = runGoTests {
    name = "wanwatch-daemon-coverage";
    script = ''
      cat > coverage.thresholds <<'EOF'
      internal/apply:90
      internal/config:100
      internal/decision:100
      internal/metrics:88
      internal/probe:86
      internal/rtnl:91
      internal/selector:100
      internal/state:94
      EOF

      go test -cover ./internal/... > coverage.out 2>&1 || {
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

  vet = runGoTests {
    name = "wanwatch-daemon-vet";
    script = "go vet ./...";
  };

  race = runGoTests {
    name = "wanwatch-daemon-race";
    cgo = true;
    script = "go test -race -timeout 120s ./...";
  };
}
