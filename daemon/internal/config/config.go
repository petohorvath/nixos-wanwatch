// Package config parses the daemon's configuration file rendered by
// `lib/internal/config.nix`. The Nix side is authoritative for
// shape and value-level validation; this package performs a second
// structural pass that catches hand-edited or fuzzed files.
//
// Schema in PLAN §5.5 / `docs/specs/daemon-config.md` (Pass 6).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

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

// Global carries paths + log-level the daemon needs at startup.
type Global struct {
	StatePath     string `json:"statePath"`
	HooksDir      string `json:"hooksDir"`
	MetricsSocket string `json:"metricsSocket"`
	LogLevel      string `json:"logLevel"`
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
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("config: parsing JSON: %w", err)
	}
	if cfg.Schema != SupportedSchema {
		return Config{}, fmt.Errorf("%w: got schema %d, want %d", ErrSchemaMismatch, cfg.Schema, SupportedSchema)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate runs structural checks against a parsed Config. Catches
// hand-edits that bypassed the Nix-side validation: empty paths,
// name/key disagreement, group members referencing undeclared WANs.
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

	for key, wan := range c.Wans {
		if wan.Name != key {
			return invalidf("wans[%q].name = %q (mismatch)", key, wan.Name)
		}
		if wan.Interface == "" {
			return invalidf("wans[%q].interface is empty", key)
		}
		if len(wan.Probe.Targets) == 0 {
			return invalidf("wans[%q].probe.targets is empty", key)
		}
	}

	for key, group := range c.Groups {
		if group.Name != key {
			return invalidf("groups[%q].name = %q (mismatch)", key, group.Name)
		}
		if len(group.Members) == 0 {
			return invalidf("groups[%q] has no members", key)
		}
		for i, m := range group.Members {
			if _, ok := c.Wans[m.Wan]; !ok {
				return invalidf("groups[%q].members[%d].wan = %q is not declared in wans", key, i, m.Wan)
			}
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
