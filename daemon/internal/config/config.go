// Package config parses the daemon's configuration file rendered by
// `lib/internal/config.nix`. The Nix side is authoritative; this
// package performs a second pass — structural checks plus a mirror
// of the lib's value-range validation — that catches hand-edited,
// fuzzed, or mis-rendered files before they reach the runtime
// layers (a non-positive intervalMs, for one, would panic
// time.NewTicker deep in a prober goroutine).
//
// Schema in docs/specs/daemon-state.md / `docs/specs/daemon-config.md` (Pass 6).
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"slices"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
)

// SupportedSchema is the daemon-config schema version this build
// understands. It is bumped in lockstep with `lib/internal/config.nix`'s
// `schemaVersion` whenever the on-disk shape changes incompatibly
// (docs/specs/daemon-config.md, "Compatibility policy").
const SupportedSchema = 1

// Sentinel errors. Callers match with `errors.Is`.
var (
	// ErrSchemaMismatch is returned when the config file's schema
	// field doesn't match SupportedSchema.
	ErrSchemaMismatch = errors.New("config: schema mismatch")

	// ErrInvalidConfig is returned when structural validation fails
	// (missing fields, name/key disagreement, dangling references).
	ErrInvalidConfig = errors.New("config: invalid")
)

// Config is the daemon-config root.
type Config struct {
	Schema int                       `json:"schema"`
	Global Global                    `json:"global"`
	Wans   map[string]Wan            `json:"wans"`
	Groups map[string]selector.Group `json:"groups"`
}

// Global carries paths, log-level, and the hook timeout the daemon
// needs at startup.
type Global struct {
	StatePath     string `json:"statePath"`
	HooksDir      string `json:"hooksDir"`
	MetricsSocket string `json:"metricsSocket"`
	LogLevel      string `json:"logLevel"`
	HookTimeoutMs int    `json:"hookTimeoutMs"`
}

// Wan is the daemon-side view of a WAN, mirroring
// `wanwatch.wan.toJSONValue`. The selector consumes only the wan
// *name* (via Member.Wan); this struct's other fields drive the
// probe and apply layers.
//
// PointToPoint = true selects scope-link routes for this WAN
// (PPP / WireGuard / GRE / tun-style links with no broadcast
// next-hop). PointToPoint = false leaves the daemon to discover
// the gateway via netlink from the kernel's main routing table.
type Wan struct {
	Name         string `json:"name"`
	Interface    string `json:"interface"`
	PointToPoint bool   `json:"pointToPoint"`
	Probe        Probe  `json:"probe"`
}

// Probe mirrors `wanwatch.probe.toJSONValue`.
type Probe struct {
	Method             string     `json:"method"`
	Targets            Targets    `json:"targets"`
	IntervalMs         int        `json:"intervalMs"`
	TimeoutMs          int        `json:"timeoutMs"`
	WindowSize         int        `json:"windowSize"`
	Thresholds         Thresholds `json:"thresholds"`
	Hysteresis         Hysteresis `json:"hysteresis"`
	FamilyHealthPolicy string     `json:"familyHealthPolicy"`
}

// Targets is the per-family probe-target list mirroring
// `wanwatch.probe.toJSONValue.targets`. At least one of V4/V6 must
// be non-empty, and each bucket holds distinct, unzoned IP literals
// of its Family, with no IPv4-mapped address in V6. The Nix lib
// enforces this at evaluation and Validate mirrors it, so a bad
// Target fails at load instead of in a prober goroutine. Validate
// compares addresses, not strings, and accepts non-canonical text.
type Targets struct {
	V4 []string `json:"v4"`
	V6 []string `json:"v6"`
}

// Families lists the Families a WAN with these Targets serves, v4
// first: a Family is served when its bucket is non-empty. Mirrors
// `wanwatch.probe.families` on the Nix side. Validate keeps each
// bucket to literals of its Family, so no address parsing is needed.
func (t Targets) Families() []probe.Family {
	fams := make([]probe.Family, 0, 2)
	if len(t.V4) > 0 {
		fams = append(fams, probe.FamilyV4)
	}
	if len(t.V6) > 0 {
		fams = append(fams, probe.FamilyV6)
	}
	return fams
}

// For returns the Targets for family, in configuration order, and
// nil for a value that is not a Family.
func (t Targets) For(family probe.Family) []string {
	switch family {
	case probe.FamilyV4:
		return t.V4
	case probe.FamilyV6:
		return t.V6
	default:
		return nil
	}
}

// Thresholds mirrors the nested probe.thresholds submodule.
type Thresholds struct {
	LossPctDown int `json:"lossPctDown"`
	LossPctUp   int `json:"lossPctUp"`
	RttMsDown   int `json:"rttMsDown"`
	RttMsUp     int `json:"rttMsUp"`
}

// Hysteresis mirrors the nested probe.hysteresis submodule.
type Hysteresis struct {
	ConsecutiveDown int `json:"consecutiveDown"`
	ConsecutiveUp   int `json:"consecutiveUp"`
}

// Load reads the config file at `path`, unmarshals JSON, and runs
// structural validation. Returns ErrSchemaMismatch (wrapped) when
// the schema doesn't match, ErrInvalidConfig (wrapped) when
// structural checks fail, or a plain I/O error from os.ReadFile.
func Load(path string) (Config, error) {
	//nolint:gosec // operator-supplied --config path is the daemon's whole input contract
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: reading %s: %w", path, err)
	}
	return Parse(data)
}

// Parse unmarshals JSON bytes into a Config and runs structural
// validation. Useful for tests + for piping a rendered Nix string
// directly without round-tripping through disk.
func Parse(data []byte) (Config, error) {
	var cfg Config
	// DisallowUnknownFields turns a typo'd key ("hookdir" for
	// "hooksDir") into a loud parse error instead of a silently
	// dropped field that resurfaces later as a confusing "empty"
	// check.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("config: parsing JSON: %w", err)
	}
	// json.Unmarshal rejected trailing data; the streaming decoder
	// does not unless asked — a second Decode should hit EOF cleanly.
	if err := dec.Decode(new(struct{})); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("config: parsing JSON: unexpected trailing data")
	}
	if cfg.Schema != SupportedSchema {
		return Config{}, fmt.Errorf("%w: got schema %d, want %d", ErrSchemaMismatch, cfg.Schema, SupportedSchema)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate runs structural and value-range checks against a parsed
// Config. It catches hand-edits that bypassed the Nix-side
// validation — empty paths, name/key disagreement, group members
// referencing undeclared WANs — and mirrors the lib's value checks
// (Target literals, positive intervals/timeouts/windows, threshold
// ranges and ordering, known method/strategy) so a bad value fails
// here with a clear message instead of panicking or silently
// degrading a runtime layer.
//
// The first failure short-circuits — config errors are
// configuration bugs, not user input, so fix-one-find-the-next
// is acceptable here (unlike `wan.tryMake`'s aggregation).
func (c *Config) Validate() error {
	if c.Global.StatePath == "" {
		return invalidf("global.statePath is empty")
	}
	if c.Global.HooksDir == "" {
		return invalidf("global.hooksDir is empty")
	}
	if c.Global.MetricsSocket == "" {
		return invalidf("global.metricsSocket is empty")
	}
	if c.Global.HookTimeoutMs <= 0 {
		return invalidf("global.hookTimeoutMs = %d, want > 0", c.Global.HookTimeoutMs)
	}

	for key, wan := range c.Wans {
		if err := validateWan(key, wan); err != nil {
			return err
		}
	}
	for key, group := range c.Groups {
		if err := validateGroup(key, group, c.Wans); err != nil {
			return err
		}
	}
	return nil
}

// validateWan checks one WAN's structural fields plus its probe.
func validateWan(key string, w Wan) error {
	if w.Name != key {
		return invalidf("wans[%q].name = %q (mismatch)", key, w.Name)
	}
	if w.Interface == "" {
		return invalidf("wans[%q].interface is empty", key)
	}
	return validateProbe(key, w.Probe)
}

// validateProbe mirrors the lib's probe value checks. The
// non-positive guards matter most: a zero intervalMs panics
// time.NewTicker inside a prober goroutine, and a zero windowSize
// fails NewWindow — both far from the config file that caused them.
func validateProbe(wanKey string, p Probe) error {
	if p.Method != "icmp" {
		return invalidf("wans[%q].probe.method = %q, want \"icmp\"", wanKey, p.Method)
	}
	if len(p.Targets.V4) == 0 && len(p.Targets.V6) == 0 {
		return invalidf("wans[%q].probe.targets has no entries (both v4 and v6 empty)", wanKey)
	}
	if err := validateTargets(wanKey, p.Targets); err != nil {
		return err
	}
	if p.IntervalMs <= 0 {
		return invalidf("wans[%q].probe.intervalMs = %d, want > 0", wanKey, p.IntervalMs)
	}
	if p.TimeoutMs <= 0 {
		return invalidf("wans[%q].probe.timeoutMs = %d, want > 0", wanKey, p.TimeoutMs)
	}
	if p.WindowSize <= 0 {
		return invalidf("wans[%q].probe.windowSize = %d, want > 0", wanKey, p.WindowSize)
	}
	switch p.FamilyHealthPolicy {
	case "all", "any":
	default:
		return invalidf("wans[%q].probe.familyHealthPolicy = %q, want \"all\" or \"any\"", wanKey, p.FamilyHealthPolicy)
	}
	if err := validateThresholds(wanKey, p.Thresholds); err != nil {
		return err
	}
	return validateHysteresis(wanKey, p.Hysteresis)
}

// validateTargets mirrors the lib's Target checks. The Pinger fails
// on a Target that is not a literal of its bucket's Family, which
// cancels the daemon, and keys its Windows by Target, so a duplicate
// would put two Samples per cycle into one Window.
func validateTargets(wanKey string, t Targets) error {
	if err := validateTargetBucket(wanKey, targetFamilyV4, t.V4); err != nil {
		return err
	}
	return validateTargetBucket(wanKey, targetFamilyV6, t.V6)
}

// targetFamily describes the Family of one Targets bucket: its key in
// config.json, its name in error messages, and its address test.
type targetFamily struct {
	bucket string
	name   string
	holds  func(netip.Addr) bool
}

var (
	targetFamilyV4 = targetFamily{bucket: "v4", name: "IPv4", holds: netip.Addr.Is4}
	targetFamilyV6 = targetFamily{bucket: "v6", name: "IPv6", holds: netip.Addr.Is6}
)

// validateTargetBucket checks one Family's Targets. An IPv4-mapped
// address is IPv6 to netip but IPv4 to the Pinger, so it is rejected
// in v6; in v4 it already fails the Family test.
func validateTargetBucket(wanKey string, family targetFamily, targets []string) error {
	seen := make(map[netip.Addr]int, len(targets))
	for i, target := range targets {
		field := fmt.Sprintf("wans[%q].probe.targets.%s[%d] = %q", wanKey, family.bucket, i, target)
		addr, err := netip.ParseAddr(target)
		if err != nil {
			return invalidf("%s is not an IP address: %v", field, err)
		}
		if addr.Zone() != "" {
			return invalidf("%s has a zone", field)
		}
		if !family.holds(addr) {
			return invalidf("%s is not an %s address", field, family.name)
		}
		if addr.Is4In6() {
			return invalidf("%s is an IPv4-mapped address", field)
		}
		if first, ok := seen[addr]; ok {
			return invalidf("%s duplicates %s[%d]", field, family.bucket, first)
		}
		seen[addr] = i
	}
	return nil
}

// validateThresholds mirrors the lib's loss/RTT range and ordering
// checks. decision.go's band-pass assumes Up < Down in both
// dimensions; an inverted pair would flip the hysteresis bands.
func validateThresholds(wanKey string, t Thresholds) error {
	if t.LossPctDown < 0 || t.LossPctDown > 100 {
		return invalidf("wans[%q].probe.thresholds.lossPctDown = %d, want 0..100", wanKey, t.LossPctDown)
	}
	if t.LossPctUp < 0 || t.LossPctUp > 100 {
		return invalidf("wans[%q].probe.thresholds.lossPctUp = %d, want 0..100", wanKey, t.LossPctUp)
	}
	if t.LossPctUp >= t.LossPctDown {
		return invalidf("wans[%q].probe.thresholds: lossPctUp (%d) must be below lossPctDown (%d)", wanKey, t.LossPctUp, t.LossPctDown)
	}
	if t.RttMsUp <= 0 || t.RttMsDown <= 0 {
		return invalidf("wans[%q].probe.thresholds: rttMs values must be > 0 (up=%d down=%d)", wanKey, t.RttMsUp, t.RttMsDown)
	}
	if t.RttMsUp >= t.RttMsDown {
		return invalidf("wans[%q].probe.thresholds: rttMsUp (%d) must be below rttMsDown (%d)", wanKey, t.RttMsUp, t.RttMsDown)
	}
	return nil
}

// validateHysteresis requires positive consecutive-cycle counters —
// a zero counter would mean "flip on every sample", defeating the
// flap suppression hysteresis exists for.
func validateHysteresis(wanKey string, h Hysteresis) error {
	if h.ConsecutiveUp < 1 {
		return invalidf("wans[%q].probe.hysteresis.consecutiveUp = %d, want >= 1", wanKey, h.ConsecutiveUp)
	}
	if h.ConsecutiveDown < 1 {
		return invalidf("wans[%q].probe.hysteresis.consecutiveDown = %d, want >= 1", wanKey, h.ConsecutiveDown)
	}
	return nil
}

// validateGroup checks one group's structural fields, that its
// strategy is one the selector actually implements, and that every
// member is keyed by its own WAN name and references a declared WAN.
func validateGroup(key string, g selector.Group, wans map[string]Wan) error {
	if g.Name != key {
		return invalidf("groups[%q].name = %q (mismatch)", key, g.Name)
	}
	if known := selector.KnownStrategies(); !slices.Contains(known, g.Strategy) {
		return invalidf("groups[%q].strategy = %q, want one of %v", key, g.Strategy, known)
	}
	if g.Table <= 0 {
		return invalidf("groups[%q].table = %d, want > 0", key, g.Table)
	}
	if g.Mark <= 0 {
		return invalidf("groups[%q].mark = %d, want > 0", key, g.Mark)
	}
	if len(g.Members) == 0 {
		return invalidf("groups[%q] has no members", key)
	}
	for wan, m := range g.Members {
		if m.Wan != wan {
			return invalidf("groups[%q].members[%q].wan = %q (mismatch)", key, wan, m.Wan)
		}
		if _, ok := wans[wan]; !ok {
			return invalidf("groups[%q].members[%q] is not declared in wans", key, wan)
		}
	}
	return nil
}

// invalidf wraps ErrInvalidConfig with a formatted detail message —
// every structural-validation error has this shape, so factor the
// `%w: ` prefix out.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidConfig}, args...)...)
}
