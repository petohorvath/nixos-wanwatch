package rtnl

import (
	"context"
	"errors"
	"fmt"

	"github.com/vishvananda/netlink"
)

// dumpAttempts bounds the reads of one netlink dump. The kernel marks
// a dump interrupted when its table changes mid-dump, as the daemon's
// own Group-table route writes do, and a fresh dump usually completes.
// Only sustained churn exhausts the attempts.
const dumpAttempts = 4

// retryDump calls dump again while it fails with
// netlink.ErrDumpInterrupted, up to dumpAttempts calls in all, and
// discards each interrupted call's partial result. It stops early
// with ctx's error once ctx is done. Any other error returns at once.
func retryDump[T any](ctx context.Context, dump func() (T, error)) (T, error) {
	var zero T
	for attempt := 1; ; attempt++ {
		v, err := dump()
		if !errors.Is(err, netlink.ErrDumpInterrupted) {
			return v, err
		}
		if attempt == dumpAttempts {
			return zero, fmt.Errorf("dump interrupted on all %d attempts: %w", dumpAttempts, err)
		}
		if err := ctx.Err(); err != nil {
			return zero, err
		}
	}
}
