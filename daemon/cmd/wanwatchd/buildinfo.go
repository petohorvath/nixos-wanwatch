package main

import "runtime"

// version / revision are injected at link time via
// `-ldflags '-X main.version=… -X main.revision=…'` by the Nix
// package builder (packages/wanwatchd/package.nix). Defaults preserve
// developer-build identity so `wanwatch_build_info` still emits
// something useful when run from a checkout.
var (
	version  = "dev"
	revision = "unknown"
)

var goVersion = runtime.Version()
