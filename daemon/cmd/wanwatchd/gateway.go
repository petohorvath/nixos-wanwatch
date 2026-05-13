package main

import (
	"net"
	"sync"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
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
// Safe for concurrent use.
type GatewayCache struct {
	mu      sync.RWMutex
	entries map[gwKey]net.IP
}

func NewGatewayCache() *GatewayCache {
	return &GatewayCache{entries: make(map[gwKey]net.IP)}
}

// Set records the gateway for the (iface, family) pair. A nil IP
// is recorded as-is (scope-link route).
func (c *GatewayCache) Set(iface string, fam rtnl.RouteFamily, gw net.IP) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[gwKey{Iface: iface, Family: fam}] = gw
}

// Clear removes the entry for (iface, family) — used when the
// kernel signals RTM_DELROUTE. A subsequent Get returns (nil,
// false).
func (c *GatewayCache) Clear(iface string, fam rtnl.RouteFamily) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, gwKey{Iface: iface, Family: fam})
}

// Get returns (gateway, true) if the cache has an entry; (nil,
// false) otherwise. The gateway is `nil` for a scope-link entry.
func (c *GatewayCache) Get(iface string, fam rtnl.RouteFamily) (net.IP, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	gw, ok := c.entries[gwKey{Iface: iface, Family: fam}]
	return gw, ok
}

// Snapshot returns a copy of the cache taken under a single
// RLock. Callers that need many entries in one pass (state.json
// publication, hook env-var assembly) should snapshot once
// rather than calling Get repeatedly.
type Snapshot map[gwKey]net.IP

func (c *GatewayCache) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(Snapshot, len(c.entries))
	for k, v := range c.entries {
		out[k] = v
	}
	return out
}

// String returns the gateway as a canonical string
// (`"192.0.2.1"`, `"2001:db8::1"`) or `""` when the cache has no
// entry or holds a scope-link (nil) entry. The empty-string
// collapse matches the JSON state-file and hook env-var contracts.
func (s Snapshot) String(iface string, fam rtnl.RouteFamily) string {
	gw, ok := s[gwKey{Iface: iface, Family: fam}]
	if !ok || gw == nil {
		return ""
	}
	return gw.String()
}

// probeFamilyToRoute converts the daemon's internal probe.Family
// enum to the rtnl.RouteFamily values the cache uses. Both are
// "the family of an IPv4/IPv6 address" but they live in separate
// packages to avoid an import cycle.
func probeFamilyToRoute(f probe.Family) rtnl.RouteFamily {
	if f == probe.FamilyV6 {
		return rtnl.RouteFamilyV6
	}
	return rtnl.RouteFamilyV4
}
