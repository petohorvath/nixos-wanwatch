# `runGoCheck`, shared by the `daemon` build test and the `lint` entry
# in `dev/checks.nix`. `pkgs` provides Go and gcc.
{ pkgs }:
let
  /*
    A sandboxed Go command run over `daemon/`. The vendored modules and
    disabled proxy make any network access fail. Go refuses a go.mod
    directly in the build's temporary root, so the script copies the
    source into a subdirectory first.

    `name`: the derivation name.
    `cgo`: whether to enable cgo and add gcc, as `-race` requires.
    `script`: shell commands run in the source copy.

    Returns a derivation that builds when `script` succeeds.
  */
  runGoCheck =
    {
      name,
      cgo ? false,
      script,
    }:
    pkgs.runCommand name
      {
        src = ../../daemon;
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
runGoCheck
