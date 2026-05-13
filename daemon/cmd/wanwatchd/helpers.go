package main

import (
	"fmt"
	"net"
)

// Small free-function helpers shared across the daemon pipeline.
// They don't depend on the `daemon` struct — keeping them out of
// daemon.go avoids burying short utilities under the bigger methods.

// boolToFloat is the gauge-friendly bool encoding: 1.0 for true,
// 0.0 for false. Used everywhere a Prometheus gauge stores a binary
// signal (`wanwatch_wan_healthy`, `wanwatch_wan_carrier`, etc.).
func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// interfaceIndex wraps net.InterfaceByName to report ifindex with
// an error message that names the offending interface — apply
// callers carry the result into route writes, where the kernel-side
// error otherwise just says "ENODEV".
func interfaceIndex(name string) (int, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return 0, fmt.Errorf("InterfaceByName %q: %w", name, err)
	}
	return iface.Index, nil
}

// ifaceFor resolves a WAN name to its interface name via the
// daemon's `wans` map. Returns "" when name is nil or the WAN
// isn't known — hook env vars treat empty string as "no value".
func ifaceFor(wans map[string]*wanState, name *string) string {
	if name == nil {
		return ""
	}
	if w, ok := wans[*name]; ok {
		return w.cfg.Interface
	}
	return ""
}

// probedFamiliesFor lists the families a WAN is probing as
// stringified labels ("v4", "v6"). Empty slice when name is nil or
// the WAN isn't known. Order is map-iteration order — fine for env
// vars but tests that assert exact contents should sort first.
func probedFamiliesFor(wans map[string]*wanState, name *string) []string {
	if name == nil {
		return nil
	}
	w, ok := wans[*name]
	if !ok {
		return nil
	}
	out := make([]string, 0, 2)
	for fam := range w.families {
		out = append(out, fam.String())
	}
	return out
}

// strPtr collapses a `*string` to its value or "" — convenient for
// places like log fields and hook env vars where nil and "" are
// indistinguishable to the consumer.
func strPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
