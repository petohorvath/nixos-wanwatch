package main

import (
	"net"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
)

type gwKey struct {
	Iface  string
	Family rtnl.RouteFamily
}

// gatewayCache mirrors the kernel's "default route per (iface,
// family)" view of the main routing table. The daemon writes its
// own routes into per-group tables; the cache only tracks routes
// the kernel installed.
//
// A nil IP value is a scope-link default (PPP / WireGuard / GRE /
// tun): the (iface, family) pair has a route but no next-hop.
//
// Not synchronized: the cache is owned by the event-loop goroutine
// and only ever read or mutated from a RouteEvent / ProbeResult /
// LinkEvent handler, all of which run on that one goroutine.
type gatewayCache struct {
	entries map[gwKey]net.IP
}

func newGatewayCache() *gatewayCache {
	return &gatewayCache{entries: make(map[gwKey]net.IP)}
}

// set records the gateway for the (iface, family) pair. A nil IP
// is recorded as-is (scope-link route).
func (c *gatewayCache) set(iface string, fam rtnl.RouteFamily, gw net.IP) {
	c.entries[gwKey{Iface: iface, Family: fam}] = gw
}

// clear removes the entry for (iface, family) — used when the
// kernel signals RTM_DELROUTE. A subsequent get returns (nil,
// false).
func (c *gatewayCache) clear(iface string, fam rtnl.RouteFamily) {
	delete(c.entries, gwKey{Iface: iface, Family: fam})
}

// get returns (gateway, true) if the cache has an entry; (nil,
// false) otherwise. The gateway is `nil` for a scope-link entry.
func (c *gatewayCache) get(iface string, fam rtnl.RouteFamily) (net.IP, bool) {
	gw, ok := c.entries[gwKey{Iface: iface, Family: fam}]
	return gw, ok
}

// string returns the gateway as a canonical string
// (`"192.0.2.1"`, `"2001:db8::1"`) or `""` when the cache has no
// entry or holds a scope-link (nil) entry. The empty-string
// collapse matches the JSON state-file and hook env-var contracts.
func (c *gatewayCache) string(iface string, fam rtnl.RouteFamily) string {
	gw, ok := c.entries[gwKey{Iface: iface, Family: fam}]
	if !ok || gw == nil {
		return ""
	}
	return gw.String()
}
