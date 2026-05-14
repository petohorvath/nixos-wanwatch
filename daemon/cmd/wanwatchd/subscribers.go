package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
)

// startLinkSubscriber opens an rtnetlink subscription filtered to the
// daemon's WAN interfaces and returns the LinkEvent channel.
func startLinkSubscriber(ctx context.Context, cfg *config.Config, logger *slog.Logger) <-chan rtnl.LinkEvent {
	watched := watchedInterfaces(cfg)
	s := &rtnl.LinkSubscriber{Interfaces: watched}
	events := make(chan rtnl.LinkEvent, 64)
	go func() {
		err := s.Run(ctx, events)
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("rtnl subscriber exited", "err", err)
		}
	}()
	logger.Info("rtnl subscriber started", "interfaces", len(watched))
	return events
}

// startRouteSubscriber opens an rtnetlink route subscription filtered
// to the daemon's WAN interfaces and returns the RouteEvent channel.
// The daemon uses these events to learn the current default-route
// gateway on each WAN's interface from the kernel's main RIB.
//
// Prime runs synchronously before the subscriber goroutine spawns so
// that any default routes already present in the kernel (the common
// case — systemd-networkd has typically finished by the time
// wanwatchd starts) are visible to the event loop on its very first
// iteration. Without this, a link-event arriving before the
// subscriber dumps would drive an applyRoutes call that finds an
// empty cache and skips the route write.
func startRouteSubscriber(ctx context.Context, cfg *config.Config, logger *slog.Logger) (<-chan rtnl.RouteEvent, error) {
	watched := watchedInterfaces(cfg)
	s := &rtnl.RouteSubscriber{Interfaces: watched}
	events := make(chan rtnl.RouteEvent, 64)
	if err := s.Prime(ctx, events); err != nil {
		return nil, fmt.Errorf("rtnl route subscriber prime: %w", err)
	}
	go func() {
		err := s.Run(ctx, events)
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("rtnl route subscriber exited", "err", err)
		}
	}()
	logger.Info("rtnl route subscriber started", "interfaces", len(watched), "primed", len(events))
	return events, nil
}

// watchedInterfaces is the set of interface names the daemon
// subscribes to — both link and route channels filter through this.
func watchedInterfaces(cfg *config.Config) map[string]struct{} {
	out := make(map[string]struct{}, len(cfg.Wans))
	for _, wan := range cfg.Wans {
		out[wan.Interface] = struct{}{}
	}
	return out
}
