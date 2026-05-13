package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// validConfig is a minimal-but-realistic config JSON used as a base
// in tests; individual tests mutate fields to exercise validation.
const validConfig = `{
  "schema": 1,
  "global": {
    "statePath": "/run/wanwatch/state.json",
    "hooksDir": "/etc/wanwatch/hooks",
    "metricsSocket": "/run/wanwatch/metrics.sock",
    "logLevel": "info"
  },
  "wans": {
    "primary": {
      "name": "primary",
      "interface": "eth0",
      "gateways": { "v4": "192.0.2.1", "v6": null },
      "probe": {
        "method": "icmp",
        "targets": ["1.1.1.1"],
        "intervalMs": 500,
        "timeoutMs": 1000,
        "windowSize": 10,
        "thresholds": { "lossPctDown": 30, "lossPctUp": 10, "rttMsDown": 500, "rttMsUp": 250 },
        "hysteresis": { "consecutiveDown": 3, "consecutiveUp": 5 },
        "familyHealthPolicy": "all"
      }
    }
  },
  "groups": {
    "home": {
      "name": "home",
      "strategy": "primary-backup",
      "table": 100,
      "mark": 100,
      "members": [
        { "wan": "primary", "weight": 100, "priority": 1 }
      ]
    }
  }
}`

func TestParseAcceptsValidConfig(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte(validConfig))
	if err != nil {
		t.Fatalf("Parse(validConfig) error: %v", err)
	}
	if cfg.Schema != SupportedSchema {
		t.Errorf("Schema = %d, want %d", cfg.Schema, SupportedSchema)
	}
	if cfg.Global.StatePath != "/run/wanwatch/state.json" {
		t.Errorf("Global.StatePath = %q, unexpected", cfg.Global.StatePath)
	}
	if got := len(cfg.Wans); got != 1 {
		t.Errorf("len(Wans) = %d, want 1", got)
	}
	if got := len(cfg.Groups); got != 1 {
		t.Errorf("len(Groups) = %d, want 1", got)
	}
}

func TestParsePopulatesNestedFields(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte(validConfig))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	w, ok := cfg.Wans["primary"]
	if !ok {
		t.Fatal("Wans[primary] missing")
	}
	if w.Probe.Method != "icmp" {
		t.Errorf("Probe.Method = %q, want %q", w.Probe.Method, "icmp")
	}
	if got := len(w.Probe.Targets); got != 1 {
		t.Errorf("len(Probe.Targets) = %d, want 1", got)
	}
	if w.Probe.Thresholds.LossPctDown != 30 {
		t.Errorf("LossPctDown = %d, want 30", w.Probe.Thresholds.LossPctDown)
	}
	if w.Gateways.V4 == nil || *w.Gateways.V4 != "192.0.2.1" {
		t.Errorf("Gateways.V4 = %v, want pointer to %q", w.Gateways.V4, "192.0.2.1")
	}
	if w.Gateways.V6 != nil {
		t.Errorf("Gateways.V6 = %v, want nil", w.Gateways.V6)
	}
}

func TestParseRejectsSchemaMismatch(t *testing.T) {
	t.Parallel()
	bad := `{"schema": 99, "global": {"statePath":"a","hooksDir":"b","metricsSocket":"c","logLevel":"info"}, "wans":{}, "groups":{}}`
	_, err := Parse([]byte(bad))
	if !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("err = %v, want wrap of ErrSchemaMismatch", err)
	}
}

func TestParseRejectsInvalidJSON(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte("not json"))
	if err == nil {
		t.Fatal("Parse(non-json) err = nil")
	}
	if errors.Is(err, ErrSchemaMismatch) || errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err wrapped unexpected sentinel: %v", err)
	}
}

func TestValidateRejectsEmptyStatePath(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, validConfig)
	cfg.Global.StatePath = ""
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("Validate(empty statePath) = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsEmptyHooksDir(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, validConfig)
	cfg.Global.HooksDir = ""
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsEmptyMetricsSocket(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, validConfig)
	cfg.Global.MetricsSocket = ""
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsWanNameKeyMismatch(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, validConfig)
	w := cfg.Wans["primary"]
	w.Name = "renamed"
	cfg.Wans["primary"] = w
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsWanWithoutGateways(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, validConfig)
	w := cfg.Wans["primary"]
	w.Gateways.V4 = nil
	w.Gateways.V6 = nil
	cfg.Wans["primary"] = w
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsWanWithoutInterface(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, validConfig)
	w := cfg.Wans["primary"]
	w.Interface = ""
	cfg.Wans["primary"] = w
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsGroupNameKeyMismatch(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, validConfig)
	g := cfg.Groups["home"]
	g.Name = "renamed"
	cfg.Groups["home"] = g
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsGroupEmptyMembers(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, validConfig)
	g := cfg.Groups["home"]
	g.Members = nil
	cfg.Groups["home"] = g
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsDanglingMemberWan(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, validConfig)
	g := cfg.Groups["home"]
	g.Members[0].Wan = "phantom"
	cfg.Groups["home"] = g
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestLoadReadsFile(t *testing.T) {
	t.Parallel()
	tmp := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(tmp, []byte(validConfig), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := Load(tmp)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Schema != SupportedSchema {
		t.Errorf("Schema = %d", cfg.Schema)
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Parallel()
	_, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err == nil {
		t.Fatal("Load(missing) returned nil error")
	}
}

// mustParse is a test helper that parses validConfig and reports
// `t.Fatal` on the unexpected case. Returns a `*Config` so callers
// can mutate sub-fields in their negative-case scenarios.
func mustParse(t *testing.T, raw string) Config {
	t.Helper()
	cfg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("baseline Parse failed unexpectedly: %v", err)
	}
	return cfg
}
