package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
      "pointToPoint": false,
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
	if w.PointToPoint {
		t.Errorf("PointToPoint = true, want false")
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

func TestValidateRejectsWanWithoutProbeTargets(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, validConfig)
	w := cfg.Wans["primary"]
	w.Probe.Targets = nil
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

// FuzzParse exercises the parser with arbitrary byte inputs. The
// invariant is shape-level: Parse never panics; every accept path
// produces a non-zero Config with Schema == SupportedSchema; every
// reject path returns either ErrSchemaMismatch or ErrInvalidConfig
// (wrapped) or a JSON-decode error — never a bare value-and-error
// or a value-without-error.
//
// `go test -run none -fuzz=FuzzParse ./internal/config` runs an
// actual fuzz campaign; `go test ./...` (CI default) only exercises
// the seed corpus, which is itself a useful regression net.
func FuzzParse(f *testing.F) {
	f.Add([]byte(`{"schema":1}`))
	f.Add([]byte(`{"schema":99}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))
	f.Add([]byte(`not json`))
	f.Add([]byte(`{"schema":1,"global":{"statePath":"a","hooksDir":"b","metricsSocket":"c","logLevel":"info"},"wans":{},"groups":{}}`))
	f.Add([]byte(`{"schema":1,"wans":null,"groups":null}`))
	f.Add([]byte(`{"schema":"not-an-int"}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		cfg, err := Parse(raw)
		// Panic protection is the headline contract — gophers fuzz
		// for panics. If the body got here without recover, parse
		// didn't panic on this input; pass.
		if err == nil {
			// Accept paths must produce the version we claim to
			// support; otherwise a future schema bump that forgot
			// to update Parse would slip through.
			if cfg.Schema != SupportedSchema {
				t.Errorf("Parse(%q) returned nil err with Schema=%d, want %d",
					raw, cfg.Schema, SupportedSchema)
			}
			return
		}
		// Reject paths surface as either of our sentinels (wrapped)
		// or a stdlib JSON-decode error. Anything else is a missed
		// classification.
		if errors.Is(err, ErrSchemaMismatch) || errors.Is(err, ErrInvalidConfig) {
			return
		}
		// json.Unmarshal returns *json.SyntaxError / *json.UnmarshalTypeError.
		// We don't import encoding/json here; checking the wrapped
		// message prefix is enough — Parse adds "config: parsing JSON:".
		if !strings.Contains(err.Error(), "parsing JSON") {
			t.Errorf("Parse(%q) returned an unclassified error: %v", raw, err)
		}
	})
}
