package main

import (
	"net"
	"sync"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
)

// gwKey indexes the GatewayCache by interface name + IP family —
// the granularity at which the kernel reports default routes via
// rtnetlink and at which the daemon writes them via apply.WriteDefault.
type gwKey struct {
	Iface  string
	Family rtnl.RouteFamily
}

// GatewayCache mirrors the kernel's "default route per (iface,
// family)" view of the main routing table. The daemon writes its
// own routes into per-group tables; the cache only tracks routes
// the kernel installed (typically by systemd-networkd / dhcpcd /
// pppd / the user's DHCP client).
//
// The cache replaces the operator-typed `gateways.{v4,v6}`
// declaration — the daemon now learns the next-hop dynamically
// instead of requiring it in config.
//
// Empty IP value is intentional: scope-link defaults (PPP /
// WireGuard / GRE / tun) have no next-hop. The cache records that
// the family is "served" but holds nil — apply.WriteDefault will
// skip non-PtP-declared WANs in that case.
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

// String returns the gateway as a canonical string (`"192.0.2.1"`,
// `"2001:db8::1"`, or `""` if no entry / scope-link entry). Used
// to emit hook env vars (WANWATCH_GATEWAY_V4/V6_OLD/NEW).
func (c *GatewayCache) String(iface string, fam rtnl.RouteFamily) string {
	gw, ok := c.Get(iface, fam)
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
