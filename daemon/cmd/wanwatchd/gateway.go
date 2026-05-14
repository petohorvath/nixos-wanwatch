package main

import (
	"net"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
)

type gwKey struct {
	Iface  string
	Family rtnl.RouteFamily
}

// GatewayCache mirrors the kernel's "default route per (iface,
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
type GatewayCache struct {
	entries map[gwKey]net.IP
}

func NewGatewayCache() *GatewayCache {
	return &GatewayCache{entries: make(map[gwKey]net.IP)}
}

// Set records the gateway for the (iface, family) pair. A nil IP
// is recorded as-is (scope-link route).
func (c *GatewayCache) Set(iface string, fam rtnl.RouteFamily, gw net.IP) {
	c.entries[gwKey{Iface: iface, Family: fam}] = gw
}

// Clear removes the entry for (iface, family) — used when the
// kernel signals RTM_DELROUTE. A subsequent Get returns (nil,
// false).
func (c *GatewayCache) Clear(iface string, fam rtnl.RouteFamily) {
	delete(c.entries, gwKey{Iface: iface, Family: fam})
}

// Get returns (gateway, true) if the cache has an entry; (nil,
// false) otherwise. The gateway is `nil` for a scope-link entry.
func (c *GatewayCache) Get(iface string, fam rtnl.RouteFamily) (net.IP, bool) {
	gw, ok := c.entries[gwKey{Iface: iface, Family: fam}]
	return gw, ok
}

// String returns the gateway as a canonical string
// (`"192.0.2.1"`, `"2001:db8::1"`) or `""` when the cache has no
// entry or holds a scope-link (nil) entry. The empty-string
// collapse matches the JSON state-file and hook env-var contracts.
func (c *GatewayCache) String(iface string, fam rtnl.RouteFamily) string {
	gw, ok := c.entries[gwKey{Iface: iface, Family: fam}]
	if !ok || gw == nil {
		return ""
	}
	return gw.String()
}
