/*
  Unit-test runner. Wraps `lib.runTests` in a derivation so failures
  surface as a failed `nix flake check`.
*/
{ pkgs }:
let
  inherit (pkgs) lib;

  formatValue = lib.generators.toPretty { multiline = true; };

  formatFailure = failure: ''
    ✗ ${failure.name}
        expected: ${formatValue failure.expected}
        actual:   ${formatValue failure.result}
  '';
in
{
  /*
    Run a `lib.runTests`-shaped attrset of tests inside a derivation.

    `tests`: attrset of `testFoo = { expr; expected; }` entries; names
    without the `test` prefix are ignored, as in `lib.runTests`.

    Returns a derivation that builds an empty `$out` when every test
    passes, and otherwise prints each failure and exits 1. The
    `failures` and `total` passthru attributes expose the raw results.
  */
  runTests =
    tests:
    let
      failures = lib.runTests tests;
      total = lib.pipe tests [
        builtins.attrNames
        (builtins.filter (lib.hasPrefix "test"))
        builtins.length
      ];
      failed = builtins.length failures;
      report = lib.concatMapStringsSep "\n" formatFailure failures;
    in
    pkgs.runCommand "wanwatch-unit-tests"
      {
        passthru = {
          inherit failures total;
        };
      }
      (
        if failed == 0 then
          ''
            echo "all ${toString total} unit test(s) passed"
            touch $out
          ''
        else
          ''
            echo "${toString failed} of ${toString total} unit test(s) failed:"
            cat <<'EOF'
            ${report}
            EOF
            exit 1
          ''
      );
}
