package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
)

// startLinkSubscriber opens an rtnetlink subscription filtered to the
// daemon's WAN interfaces and returns the LinkEvent channel. A
// subscriber that exits with anything other than context
// cancellation calls `cancel` with the cause, taking the daemon down
// for a systemd restart rather than running on blind.
//
// Prime runs synchronously before the subscriber goroutine spawns so
// each WAN's carrier/operstate is on the channel before the event
// loop's first iteration — see rtnl.LinkSubscriber.Prime for why a
// probe result racing the link dump would otherwise leave an up WAN
// unselected.
func startLinkSubscriber(ctx context.Context, cancel context.CancelCauseFunc, cfg *config.Config, logger *slog.Logger) (<-chan rtnl.LinkEvent, error) {
	watched := watchedInterfaces(cfg)
	s := &rtnl.LinkSubscriber{Interfaces: watched}
	events := make(chan rtnl.LinkEvent, 64)
	if err := s.Prime(ctx, events); err != nil {
		return nil, fmt.Errorf("rtnl link subscriber prime: %w", err)
	}
	go func() {
		err := s.Run(ctx, events)
		onSubsystemExit(cancel, logger, "link subscriber", err)
	}()
	logger.Info("rtnl link subscriber started", "interfaces", len(watched), "primed", len(events))
	return events, nil
}

// startRouteSubscriber opens an rtnetlink route subscription filtered
// to the daemon's WAN interfaces and returns the RouteEvent channel.
// The daemon uses these events to learn the current default-route
// gateway on each WAN's interface from the kernel's main RIB.
//
// Start establishes the live subscription before queuing the initial
// route snapshot synchronously. Buffered and subsequent notifications
// trigger current-route reads, so startup captures new defaults without
// exposing obsolete route history to Apply or State.
func startRouteSubscriber(ctx context.Context, cancel context.CancelCauseFunc, cfg *config.Config, logger *slog.Logger) (<-chan rtnl.RouteEvent, error) {
	watched := watchedInterfaces(cfg)
	s := &rtnl.RouteSubscriber{Interfaces: watched}
	events := make(chan rtnl.RouteEvent, 64)
	exited, err := s.Start(ctx, events)
	if err != nil {
		return nil, fmt.Errorf("rtnl route subscriber start: %w", err)
	}
	go func() {
		onSubsystemExit(cancel, logger, "route subscriber", <-exited)
	}()
	logger.Info("rtnl route subscriber started", "interfaces", len(watched), "queued", len(events))
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
