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
// interface binding) is in icmp.go (added in Pass 2 per PLAN §10).
package probe

import "math"

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
// RTT, jitter — are read via the accessor methods.
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

// NewWindow returns a WindowStats with the given capacity. Panics
// when capacity <= 0 — wanwatch's config validation rejects
// non-positive window sizes upstream; reaching this path is a
// programmer error.
func NewWindow(capacity int) *WindowStats {
	if capacity <= 0 {
		panic("probe: NewWindow: capacity must be positive")
	}
	return &WindowStats{
		capacity: capacity,
		samples:  make([]Sample, capacity),
	}
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

// LossRatio returns the fraction of Samples in the window that were
// Lost, as a float in [0.0, 1.0]. Returns 0 when the window is empty.
func (w *WindowStats) LossRatio() float64 {
	n := w.Len()
	if n == 0 {
		return 0
	}
	var lost int
	w.forEach(func(s Sample) {
		if s.Lost {
			lost++
		}
	})
	return float64(lost) / float64(n)
}

// MeanRTT returns the arithmetic mean of RTTs across non-Lost Samples,
// in microseconds. Returns 0 when no non-Lost Samples are in the window.
func (w *WindowStats) MeanRTT() uint64 {
	var sum, n uint64
	w.forEach(func(s Sample) {
		if !s.Lost {
			sum += s.RTTMicros
			n++
		}
	})
	if n == 0 {
		return 0
	}
	return sum / n
}

// JitterMicros returns the population standard deviation of RTTs
// across non-Lost Samples, in microseconds. Returns 0 when fewer than
// two non-Lost Samples are in the window — stddev is degenerate
// below that threshold.
func (w *WindowStats) JitterMicros() uint64 {
	mean := w.MeanRTT()
	var sumSq float64
	var n uint64
	w.forEach(func(s Sample) {
		if !s.Lost {
			d := float64(s.RTTMicros) - float64(mean)
			sumSq += d * d
			n++
		}
	})
	if n < 2 {
		return 0
	}
	variance := sumSq / float64(n)
	return uint64(math.Sqrt(variance))
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
