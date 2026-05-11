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
// probe and apply layers in Pass 4+.
type Wan struct {
	Type      string   `json:"_type"`
	Name      string   `json:"name"`
	Interface string   `json:"interface"`
	Gateways  Gateways `json:"gateways"`
	Probe     Probe    `json:"probe"`
}

// Gateways carries the per-family gateway IPs, as strings (Nix
// libnet convention — option values stay as strings; parsing into
// `net.IP` happens at the apply layer, where the family is
// needed by `netlink`).
type Gateways struct {
	V4 *string `json:"v4"`
	V6 *string `json:"v6"`
}

// Probe mirrors `wanwatch.probe.toJSONValue`.
type Probe struct {
	Type               string     `json:"_type"`
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
		return fmt.Errorf("%w: global.statePath is empty", ErrInvalidConfig)
	}
	if c.Global.HooksDir == "" {
		return fmt.Errorf("%w: global.hooksDir is empty", ErrInvalidConfig)
	}
	if c.Global.MetricsSocket == "" {
		return fmt.Errorf("%w: global.metricsSocket is empty", ErrInvalidConfig)
	}

	for key, wan := range c.Wans {
		if wan.Name != key {
			return fmt.Errorf("%w: wans[%q].name = %q (mismatch)", ErrInvalidConfig, key, wan.Name)
		}
		if wan.Interface == "" {
			return fmt.Errorf("%w: wans[%q].interface is empty", ErrInvalidConfig, key)
		}
		if wan.Gateways.V4 == nil && wan.Gateways.V6 == nil {
			return fmt.Errorf("%w: wans[%q] has no gateways", ErrInvalidConfig, key)
		}
	}

	for key, group := range c.Groups {
		if group.Name != key {
			return fmt.Errorf("%w: groups[%q].name = %q (mismatch)", ErrInvalidConfig, key, group.Name)
		}
		if len(group.Members) == 0 {
			return fmt.Errorf("%w: groups[%q] has no members", ErrInvalidConfig, key)
		}
		for i, m := range group.Members {
			if _, ok := c.Wans[m.Wan]; !ok {
				return fmt.Errorf("%w: groups[%q].members[%d].wan = %q is not declared in wans", ErrInvalidConfig, key, i, m.Wan)
			}
		}
	}

	return nil
}
