package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/apply"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
)

// trace is the ordered record of effects shared by the fake kernel and
// the Hook recorder.
type trace struct {
	mu    sync.Mutex
	lines []string
	taken int
}

func (t *trace) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
}

// take returns the lines added since the previous take.
func (t *trace) take() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := slices.Clone(t.lines[t.taken:])
	t.taken = len(t.lines)
	return out
}

// failedMark suffixes the trace line of a failed attempt.
const failedMark = " ✗"

// kernelOp names one kernel operation of the daemon's kernel seam.
type kernelOp string

const (
	opRule    kernelOp = "rule"
	opIfIndex kernelOp = "ifindex"
	opRoute   kernelOp = "route"
	opAddrs   kernelOp = "addrs"
	opFlush   kernelOp = "flush"
)

// errScripted is the error every scripted failure wraps.
var errScripted = errors.New("fake kernel: scripted failure")

// kernelFailure scripts one operation to fail. An empty iface matches
// every interface; a zero fam matches every Family.
type kernelFailure struct {
	op    kernelOp
	iface string
	fam   probe.Family
}

// fakeKernel records mutations in the trace and fails the operations
// scripted with fail. Like internal/apply, it checks ctx on entry to a
// mutation and fails it once ctx is cancelled. Lookups that succeed
// are not traced. Each
// interface name gets a stable ifindex that maps back to the name, and
// each routing table maps back to its Group, so route lines read
// `route <group> <family> <iface> via <gateway>`.
type fakeKernel struct {
	trace  *trace
	groups map[int]string // table → Group name

	mu       sync.Mutex
	ifindex  map[string]int
	ifname   map[int]string
	addrs    map[string][]net.IP
	failures []kernelFailure
	holds    []kernelHold
}

// kernelHold pauses one lookup until release is closed.
type kernelHold struct {
	op      kernelOp
	iface   string
	release <-chan struct{}
}

func newFakeKernel(tr *trace, cfg *config.Config) *fakeKernel {
	k := &fakeKernel{
		trace:   tr,
		groups:  make(map[int]string, len(cfg.Groups)),
		ifindex: make(map[string]int),
		ifname:  make(map[int]string),
		addrs:   make(map[string][]net.IP),
	}
	for name, g := range cfg.Groups {
		k.groups[g.Table] = name
	}
	return k
}

// fail makes op on iface fail until healed: for every Family when fams
// is empty, otherwise for each one given. An empty iface matches every
// interface; rules and flushes carry no interface of their own, so a
// flush matches the interface owning its source address.
func (k *fakeKernel) fail(op kernelOp, iface string, fams ...probe.Family) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, f := range failuresFor(op, iface, fams) {
		if !slices.Contains(k.failures, f) {
			k.failures = append(k.failures, f)
		}
	}
}

// heal removes failures scripted by fail with the same arguments.
func (k *fakeKernel) heal(op kernelOp, iface string, fams ...probe.Family) {
	k.mu.Lock()
	defer k.mu.Unlock()
	drop := failuresFor(op, iface, fams)
	k.failures = slices.DeleteFunc(k.failures, func(f kernelFailure) bool {
		return slices.Contains(drop, f)
	})
}

func failuresFor(op kernelOp, iface string, fams []probe.Family) []kernelFailure {
	if len(fams) == 0 {
		return []kernelFailure{{op: op, iface: iface}}
	}
	out := make([]kernelFailure, 0, len(fams))
	for _, fam := range fams {
		out = append(out, kernelFailure{op: op, iface: iface, fam: fam})
	}
	return out
}

// setAddrs sets the global-unicast addresses InterfaceAddrs reports
// for iface.
func (k *fakeKernel) setAddrs(iface string, ips ...string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	parsed := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		parsed = append(parsed, net.ParseIP(ip))
	}
	k.addrs[iface] = parsed
}

// hold makes the next op lookup on iface return only once release is
// closed, after its outcome is traced: the event loop stays inside
// that handler, as in a kernel call still in progress.
func (k *fakeKernel) hold(op kernelOp, iface string, release <-chan struct{}) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.holds = append(k.holds, kernelHold{op: op, iface: iface, release: release})
}

// pause blocks until the release of the first hold matching op and
// iface, and consumes that hold. Callers do not hold k.mu, so the test
// can script failures meanwhile.
func (k *fakeKernel) pause(op kernelOp, iface string) {
	k.mu.Lock()
	i := slices.IndexFunc(k.holds, func(h kernelHold) bool { return h.op == op && h.iface == iface })
	if i < 0 {
		k.mu.Unlock()
		return
	}
	release := k.holds[i].release
	k.holds = slices.Delete(k.holds, i, i+1)
	k.mu.Unlock()
	<-release
}

// scripted reports whether op on iface and fam is scripted to fail.
// Callers hold k.mu.
func (k *fakeKernel) scripted(op kernelOp, iface string, fam probe.Family) bool {
	return slices.ContainsFunc(k.failures, func(f kernelFailure) bool {
		return f.op == op && (f.iface == "" || f.iface == iface) && (f.fam == 0 || f.fam == fam)
	})
}

// mutate records a mutation's line, marked when ctx is cancelled or
// the mutation is scripted to fail, and returns the failure. Callers
// hold k.mu.
func (k *fakeKernel) mutate(ctx context.Context, op kernelOp, iface string, fam probe.Family, line string) error {
	if err := ctx.Err(); err != nil {
		k.trace.add(line + failedMark)
		return fmt.Errorf("%s %s: %w", op, iface, err)
	}
	if k.scripted(op, iface, fam) {
		k.trace.add(line + failedMark)
		return fmt.Errorf("%s %s: %w", op, iface, errScripted)
	}
	k.trace.add(line)
	return nil
}

// lookup records only a failed lookup: a successful one is not an
// effect. Callers hold k.mu.
func (k *fakeKernel) lookup(op kernelOp, iface string) error {
	if k.scripted(op, iface, 0) {
		k.trace.add(fmt.Sprintf("%s %s%s", op, iface, failedMark))
		return fmt.Errorf("%s %s: %w", op, iface, errScripted)
	}
	return nil
}

func (k *fakeKernel) EnsureRule(ctx context.Context, r apply.FwmarkRule) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.mutate(ctx, opRule, "", r.Family, fmt.Sprintf("rule %s mark=%#x table=%d", r.Family, r.Mark, r.Table))
}

func (k *fakeKernel) IfIndex(name string) (int, error) {
	defer k.pause(opIfIndex, name) // runs after the unlock below
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.lookup(opIfIndex, name); err != nil {
		return 0, err
	}
	idx, ok := k.ifindex[name]
	if !ok {
		idx = len(k.ifindex) + 2 // ifindex 1 is conventionally lo
		k.ifindex[name] = idx
		k.ifname[idx] = name
	}
	return idx, nil
}

func (k *fakeKernel) WriteRoute(ctx context.Context, r apply.DefaultRoute) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	iface, ok := k.ifname[r.IfIndex]
	if !ok {
		iface = fmt.Sprintf("if%d", r.IfIndex)
	}
	group, ok := k.groups[r.Table]
	if !ok {
		group = fmt.Sprintf("table=%d", r.Table)
	}
	via := "via " + r.Gateway.String()
	if r.PointToPoint {
		via = "scope link"
	}
	return k.mutate(ctx, opRoute, iface, r.Family, fmt.Sprintf("route %s %s %s %s", group, r.Family, iface, via))
}

func (k *fakeKernel) InterfaceAddrs(name string) ([]net.IP, error) {
	defer k.pause(opAddrs, name) // runs after the unlock below
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.lookup(opAddrs, name); err != nil {
		return nil, err
	}
	return slices.Clone(k.addrs[name]), nil
}

func (k *fakeKernel) FlushConntrack(ctx context.Context, fam probe.Family, ip net.IP) (uint, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	var iface string
	for name, ips := range k.addrs {
		if slices.ContainsFunc(ips, ip.Equal) {
			iface = name
		}
	}
	if err := k.mutate(ctx, opFlush, iface, fam, fmt.Sprintf("flush %s %s", fam, ip)); err != nil {
		return 0, err
	}
	return 1, nil
}

// assertTrace compares one step's trace and prints a line diff on a
// mismatch.
func assertTrace(tb testing.TB, got, want []string) {
	tb.Helper()
	if slices.Equal(got, want) {
		return
	}
	tb.Errorf("trace mismatch (-want +got):\n%s", lineDiff(want, got))
}

// lineDiff renders a longest-common-subsequence diff of two line
// lists: kept lines are indented, removed lines start with "-" and
// added lines with "+".
func lineDiff(want, got []string) string {
	// lcs[i][j] is the LCS length of want[i:] and got[j:].
	lcs := make([][]int, len(want)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(got)+1)
	}
	for i := len(want) - 1; i >= 0; i-- {
		for j := len(got) - 1; j >= 0; j-- {
			if want[i] == got[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var b strings.Builder
	i, j := 0, 0
	for i < len(want) || j < len(got) {
		switch {
		case i < len(want) && j < len(got) && want[i] == got[j]:
			fmt.Fprintf(&b, "  %s\n", want[i])
			i++
			j++
		case j < len(got) && (i == len(want) || lcs[i][j+1] >= lcs[i+1][j]):
			fmt.Fprintf(&b, "+ %s\n", got[j])
			j++
		default:
			fmt.Fprintf(&b, "- %s\n", want[i])
			i++
		}
	}
	return b.String()
}
