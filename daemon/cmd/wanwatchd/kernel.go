package main

import (
	"context"
	"net"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/apply"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
)

// kernel is the daemon's seam to the kernel: the fwmark rules
// bootstrap installs, and the lookups and mutations behind Apply and
// the post-switch conntrack flush. netlinkKernel is the production
// adapter; tests substitute a recording fake, since the sandbox grants
// no CAP_NET_ADMIN.
type kernel interface {
	EnsureRule(ctx context.Context, r apply.FwmarkRule) error
	IfIndex(name string) (int, error)
	WriteRoute(ctx context.Context, r apply.DefaultRoute) error
	InterfaceAddrs(name string) ([]net.IP, error)
	FlushConntrack(ctx context.Context, family probe.Family, ip net.IP) (apply.FlushResult, error)
}

// netlinkKernel delegates to internal/apply and the net helpers.
type netlinkKernel struct{}

func (netlinkKernel) EnsureRule(ctx context.Context, r apply.FwmarkRule) error {
	return apply.EnsureRule(ctx, r)
}

func (netlinkKernel) IfIndex(name string) (int, error) {
	return interfaceIndex(name)
}

func (netlinkKernel) WriteRoute(ctx context.Context, r apply.DefaultRoute) error {
	return apply.WriteDefault(ctx, r)
}

func (netlinkKernel) InterfaceAddrs(name string) ([]net.IP, error) {
	return interfaceAddrs(name)
}

func (netlinkKernel) FlushConntrack(ctx context.Context, family probe.Family, ip net.IP) (apply.FlushResult, error) {
	return apply.FlushBySource(ctx, family, ip)
}
