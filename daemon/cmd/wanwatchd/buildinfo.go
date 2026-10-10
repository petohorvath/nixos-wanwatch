package main

import "runtime"

// version / commit are injected at link time via
// `-ldflags '-X main.version=… -X main.commit=…'` by the Nix
// package builder (packages/wanwatchd/package.nix). Defaults preserve
// developer-build identity so `wanwatchd_build_info` still emits
// something useful when run from a checkout.
var (
	version = "dev"
	commit  = "unknown"
)

var goVersion = runtime.Version()
