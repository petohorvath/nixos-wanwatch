// Package config parses the daemon's configuration file rendered by
// `lib/internal/config.nix`. The Nix side is authoritative; this
// package performs a second pass — structural checks plus a mirror
// of the lib's value-range validation — that catches hand-edited,
// fuzzed, or mis-rendered files before they reach the runtime
// layers (a non-positive intervalMs, for one, would panic
// time.NewTicker deep in a prober goroutine).
//
// Schema in PLAN §5.5 / `docs/specs/daemon-config.md` (Pass 6).
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
)

// SupportedSchema is the daemon-config schema version this build
// understands. Bumped in lockstep with `lib/internal/config.nix`'s
// `schemaVersion` whenever the on-disk shape changes
// incompatibly.
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
	Targets            []string   `json:"targets"`
	IntervalMs         int        `json:"intervalMs"`
	TimeoutMs          int        `json:"timeoutMs"`
	WindowSize         int        `json:"windowSize"`
	Thresholds         Thresholds `json:"thresholds"`
	Hysteresis         Hysteresis `json:"hysteresis"`
	FamilyHealthPolicy string     `json:"familyHealthPolicy"`
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
// (positive intervals/timeouts/windows, threshold ranges and
// ordering, known method/strategy) so a bad value fails here with a
// clear message instead of panicking or silently degrading a
// runtime layer.
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
	if len(p.Targets) == 0 {
		return invalidf("wans[%q].probe.targets is empty", wanKey)
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
// member references a declared WAN.
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
	for i, m := range g.Members {
		if _, ok := wans[m.Wan]; !ok {
			return invalidf("groups[%q].members[%d].wan = %q is not declared in wans", key, i, m.Wan)
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
