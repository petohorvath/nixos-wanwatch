package main

import (
	"fmt"
	"net"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
)

// Small free-function helpers shared across the daemon pipeline.
// They don't depend on the `daemon` struct — keeping them out of
// daemon.go avoids burying short utilities under the bigger methods.

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
// configured WANs. Returns "" when Active is absent or the WAN isn't
// known — hook env vars treat empty string as "no value".
func ifaceFor(wans map[string]config.Wan, a selector.Active) string {
	if !a.Has {
		return ""
	}
	return wans[a.Wan].Interface
}

// probedFamiliesFor lists the families an Active WAN is probing as
// stringified labels, v4 first. Nil when Active is absent or the WAN
// isn't known.
func probedFamiliesFor(wans map[string]config.Wan, a selector.Active) []string {
	if !a.Has {
		return nil
	}
	w, ok := wans[a.Wan]
	if !ok {
		return nil
	}
	out := make([]string, 0, 2)
	for _, fam := range w.Probe.Targets.Families() {
		out = append(out, fam.String())
	}
	return out
}
