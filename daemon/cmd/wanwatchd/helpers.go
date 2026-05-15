package main

import (
	"fmt"
	"net"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
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

// interfaceAddrs reports the global-unicast IP addresses configured
// on `name` — the source addresses traffic egressing that interface
// is SNATted to. The post-switch conntrack flush uses these to find
// the vacated WAN's addresses.
func interfaceAddrs(name string) ([]net.IP, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, fmt.Errorf("InterfaceByName %q: %w", name, err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("interface %q addrs: %w", name, err)
	}
	return filterGlobalUnicast(addrs), nil
}

// filterGlobalUnicast narrows a net.Interface address list to the
// global-unicast IPs, dropping loopback, link-local, and multicast —
// conntrack entries are never pinned to those. Split from
// interfaceAddrs so the filtering is testable without a real
// interface.
func filterGlobalUnicast(addrs []net.Addr) []net.IP {
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ipnet.IP.IsGlobalUnicast() {
			out = append(out, ipnet.IP)
		}
	}
	return out
}

// ifaceFor resolves an Active to its WAN's interface name via the
// daemon's `wans` map. Returns "" when Active is absent or the WAN
// isn't known — hook env vars treat empty string as "no value".
func ifaceFor(wans map[string]*wanState, a selector.Active) string {
	if !a.Has {
		return ""
	}
	if w, ok := wans[a.Wan]; ok {
		return w.cfg.Interface
	}
	return ""
}

// probedFamiliesFor lists the families an Active WAN is probing as
// stringified labels ("v4", "v6"). Empty slice when Active is
// absent or the WAN isn't known. Order is map-iteration order —
// fine for env vars but tests that assert exact contents should
// sort first.
func probedFamiliesFor(wans map[string]*wanState, a selector.Active) []string {
	if !a.Has {
		return nil
	}
	w, ok := wans[a.Wan]
	if !ok {
		return nil
	}
	out := make([]string, 0, 2)
	for fam := range w.families {
		out = append(out, fam.String())
	}
	return out
}
