/*
  Go alone, pinned for review sandboxes. mkShell supplies gcc for race
  tests; Go and its modules come from the lock file and daemon/vendor,
  not from a preinstalled SDK or the module proxy.
*/
{ go, mkShell }:
mkShell {
  packages = [ go ];
  GOTOOLCHAIN = "local";
  GOENV = "off";
  GOFLAGS = "-mod=vendor";
  GOPROXY = "off";
  GOSUMDB = "off";
  CGO_ENABLED = "1";
}
