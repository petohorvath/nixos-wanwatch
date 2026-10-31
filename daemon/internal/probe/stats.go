// Package probe implements ICMP / ICMPv6 probing and per-WAN
// statistics for wanwatch.
//
// stats.go is the pure-math half of the package: a sliding-window
// aggregator over Sample values. No I/O, no goroutines, no
// time.Now — every input is explicit so the tests are deterministic
// and the implementation is reusable from contexts that drive the
// window manually (e.g. replaying recorded probe traces).
//
// The ICMP transport layer (sockets, identifier allocation,
// interface binding) is in icmp.go.
package probe

import (
	"fmt"
	"math"
)

// Sample is one probe attempt's outcome.
//
// Encoded as either:
//   - {RTTMicros: positive value, Lost: false}, for a successful
//     echo with a measured round-trip time, or
//   - {RTTMicros: 0,              Lost: true},  for a timeout or
//     transport error.
//
// Keeping the result and the loss flag in one struct lets callers
// push a fixed number of Samples per cycle without an Optional<T>
// indirection.
type Sample struct {
	// RTTMicros is the round-trip time in microseconds. Ignored
	// when Lost is true.
	RTTMicros uint64

	// Lost is true when the probe timed out or otherwise failed
	// to elicit a reply.
	Lost bool
}

// WindowStats is a sliding-window aggregator over Samples. The caller
// pushes one Sample per probe cycle via Push; older samples are
// discarded once Capacity is reached. Metrics — loss ratio, mean
// RTT, jitter — are read via the accessor methods, each of which
// reports whether the window holds data for it.
//
// The zero value is unusable; construct via NewWindow. WindowStats
// is not safe for concurrent use; wrap externally if multiple
// goroutines need access.
type WindowStats struct {
	capacity int
	samples  []Sample
	head     int  // index of the next slot to write
	filled   bool // true once samples has wrapped at least once
}

// NewWindow returns a WindowStats with the given capacity. Returns
// an error when capacity <= 0 — wanwatch's config validation
// rejects non-positive window sizes upstream, but defending here
// keeps tests and ad-hoc callers honest without resorting to a
// package-level panic.
func NewWindow(capacity int) (*WindowStats, error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("probe: NewWindow: capacity must be positive (got %d)", capacity)
	}
	return &WindowStats{
		capacity: capacity,
		samples:  make([]Sample, capacity),
	}, nil
}

// Push records a Sample, discarding the oldest if the window is full.
func (w *WindowStats) Push(s Sample) {
	w.samples[w.head] = s
	w.head = (w.head + 1) % w.capacity
	if w.head == 0 {
		w.filled = true
	}
}

// Capacity returns the configured window size.
func (w *WindowStats) Capacity() int { return w.capacity }

// Len returns the current number of samples in the window — either
// the count pushed so far (when not yet filled) or Capacity.
func (w *WindowStats) Len() int {
	if w.filled {
		return w.capacity
	}
	return w.head
}

// Filled reports whether the window has wrapped at least once —
// equivalently, whether Len() == Capacity(). The cold-start
// handoff in daemon/cmd/wanwatchd defers hysteresis seeding until
// every per-target window reports Filled, so a partial-window
// verdict (e.g. the first sample landed Lost because the route
// hadn't converged yet) doesn't flap the WAN's effective Health
// once probes catch up.
func (w *WindowStats) Filled() bool { return w.filled }

// LossCounts returns the exact loss of the Window: the number of Lost
// Samples and the number of Samples it holds. A total of 0 means no
// data, an empty Window.
func (w *WindowStats) LossCounts() (lost, total int) {
	w.forEach(func(s Sample) {
		if s.Lost {
			lost++
		}
	})
	return lost, w.Len()
}

// MeanRTT returns the arithmetic mean of RTTs across non-Lost Samples,
// in microseconds. It reports false, no data, when no non-Lost Sample
// is in the window, so a dead link never reads as a perfect RTT of 0.
func (w *WindowStats) MeanRTT() (uint64, bool) {
	var sum, n uint64
	w.forEach(func(s Sample) {
		if !s.Lost {
			sum += s.RTTMicros
			n++
		}
	})
	if n == 0 {
		return 0, false
	}
	return sum / n, true
}

// JitterMicros returns the population standard deviation of RTTs
// across non-Lost Samples, in microseconds. It reports false, no data,
// when no non-Lost Sample is in the window. A single non-Lost Sample
// has a jitter of 0, so no data always means no successful Sample.
func (w *WindowStats) JitterMicros() (uint64, bool) {
	mean, ok := w.MeanRTT()
	if !ok {
		return 0, false
	}
	var sumSq float64
	var n uint64
	w.forEach(func(s Sample) {
		if !s.Lost {
			d := float64(s.RTTMicros) - float64(mean)
			sumSq += d * d
			n++
		}
	})
	variance := sumSq / float64(n)
	return uint64(math.Sqrt(variance)), true
}

// forEach iterates every Sample in the window in oldest-to-newest
// order. Internal helper that keeps ring-buffer indexing in one
// place; all metric accessors go through it.
func (w *WindowStats) forEach(fn func(Sample)) {
	if !w.filled {
		for i := 0; i < w.head; i++ {
			fn(w.samples[i])
		}
		return
	}
	for i := 0; i < w.capacity; i++ {
		idx := (w.head + i) % w.capacity
		fn(w.samples[idx])
	}
}
