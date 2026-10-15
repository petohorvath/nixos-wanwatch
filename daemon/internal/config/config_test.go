package config

import (
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
)

// goldenConfig is the daemon config that `wanwatch.config.render`
// produces from the unit suite's `fixtures.inputs.config`; the Nix
// test `internal.config.render.testMatchesGolden` keeps the two in
// agreement. Tests use it as the valid base and mutate fields to
// exercise validation.
//
//go:embed testdata/config.golden.json
var goldenConfig []byte

func TestParseAcceptsGolden(t *testing.T) {
	t.Parallel()
	cfg, err := Parse(goldenConfig)
	if err != nil {
		t.Fatalf("Parse(goldenConfig) error: %v", err)
	}
	if cfg.Schema != SupportedSchema {
		t.Errorf("Schema = %d, want %d", cfg.Schema, SupportedSchema)
	}
	if got := len(cfg.Wans); got != 3 {
		t.Errorf("len(Wans) = %d, want 3", got)
	}
	if got := len(cfg.Groups); got != 2 {
		t.Errorf("len(Groups) = %d, want 2", got)
	}
}

// TestParsePopulatesNestedFields decodes the golden's global settings,
// its full WAN, and its full Group, which set every field and mostly
// to values other than the Nix defaults, so a field decoded into the
// wrong place shows up.
func TestParsePopulatesNestedFields(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, goldenConfig)

	wantGlobal := Global{
		StatePath:     "/run/wanwatch/state.json",
		HooksDir:      "/etc/wanwatch/hooks",
		MetricsSocket: "/run/wanwatch/metrics.sock",
		LogLevel:      "debug",
		HookTimeoutMs: 2000,
	}
	if cfg.Global != wantGlobal {
		t.Errorf("Global = %+v, want %+v", cfg.Global, wantGlobal)
	}

	wantWan := Wan{
		Name:         "vpn",
		Interface:    "wg0",
		PointToPoint: true,
		Probe: Probe{
			Method: "icmp",
			Targets: Targets{
				V4: []string{"1.1.1.1"},
				V6: []string{"2606:4700:4700::1111"},
			},
			IntervalMs:         250,
			TimeoutMs:          200,
			WindowSize:         20,
			Thresholds:         Thresholds{LossPctDown: 25, LossPctUp: 5, RttMsDown: 400, RttMsUp: 150},
			Hysteresis:         Hysteresis{ConsecutiveDown: 2, ConsecutiveUp: 4},
			FamilyHealthPolicy: "any",
		},
	}
	if got := cfg.Wans["vpn"]; !reflect.DeepEqual(got, wantWan) {
		t.Errorf("Wans[vpn] = %+v, want %+v", got, wantWan)
	}

	wantGroup := selector.Group{
		Name:     "guest-uplink",
		Strategy: "primary-backup",
		Table:    1001,
		Mark:     1001,
		Members: map[string]selector.Member{
			"primary": {Wan: "primary", Priority: 1},
			"backup":  {Wan: "backup", Priority: 2},
		},
	}
	if got := cfg.Groups["guest-uplink"]; !reflect.DeepEqual(got, wantGroup) {
		t.Errorf("Groups[guest-uplink] = %+v, want %+v", got, wantGroup)
	}
}

// TestGoldenRoundTrips re-encodes the parsed golden and expects the
// same document. A field the daemon's types declare but the Nix
// renderer never writes decodes silently as its zero value; it
// surfaces here as a key the golden lacks.
func TestGoldenRoundTrips(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(mustParse(t, goldenConfig))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got, want any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("Unmarshal(encoded): %v", err)
	}
	if err := json.Unmarshal(goldenConfig, &want); err != nil {
		t.Fatalf("Unmarshal(goldenConfig): %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("re-encoded golden differs from testdata/config.golden.json.\n--- want\n%s\n--- got\n%s",
			goldenConfig, encoded)
	}
}

func TestParseRejectsSchemaMismatch(t *testing.T) {
	t.Parallel()
	_, err := Parse(goldenWith(t, 99, "schema"))
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

// TestParseRejectsUnknownFields covers the keys DisallowUnknownFields
// exists to catch. Each is a JSON-decode error, neither sentinel.
func TestParseRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		config []byte
	}{
		{"typo'd top-level key", goldenWith(t, map[string]any{}, "globl")},
		// Member has no weight; a stale config must not pass silently.
		{"member weight", goldenWith(t, 100, "groups", "home-uplink", "members", "primary", "weight")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(tt.config)
			if err == nil {
				t.Fatal("Parse(unknown field) err = nil")
			}
			if errors.Is(err, ErrSchemaMismatch) || errors.Is(err, ErrInvalidConfig) {
				t.Errorf("err wrapped unexpected sentinel: %v", err)
			}
		})
	}
}

func TestParseRejectsTrailingData(t *testing.T) {
	t.Parallel()
	// A complete config followed by junk: the first Decode succeeds,
	// the second catches the leftover bytes.
	_, err := Parse([]byte(string(goldenConfig) + "  trailing"))
	if err == nil {
		t.Fatal("Parse(trailing data) err = nil")
	}
	if errors.Is(err, ErrSchemaMismatch) || errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err wrapped unexpected sentinel: %v", err)
	}
}

func TestValidateRejectsEmptyStatePath(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, goldenConfig)
	cfg.Global.StatePath = ""
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("Validate(empty statePath) = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsEmptyHooksDir(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, goldenConfig)
	cfg.Global.HooksDir = ""
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsEmptyMetricsSocket(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, goldenConfig)
	cfg.Global.MetricsSocket = ""
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsNonPositiveHookTimeout(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, goldenConfig)
	cfg.Global.HookTimeoutMs = 0
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsWanNameKeyMismatch(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, goldenConfig)
	w := cfg.Wans["primary"]
	w.Name = "renamed"
	cfg.Wans["primary"] = w
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsWanWithoutProbeTargets(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, goldenConfig)
	w := cfg.Wans["primary"]
	w.Probe.Targets = Targets{}
	cfg.Wans["primary"] = w
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsWanWithoutInterface(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, goldenConfig)
	w := cfg.Wans["primary"]
	w.Interface = ""
	cfg.Wans["primary"] = w
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsGroupNameKeyMismatch(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, goldenConfig)
	g := cfg.Groups["home-uplink"]
	g.Name = "renamed"
	cfg.Groups["home-uplink"] = g
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsGroupEmptyMembers(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, goldenConfig)
	g := cfg.Groups["home-uplink"]
	g.Members = nil
	cfg.Groups["home-uplink"] = g
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsDanglingMemberWan(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, goldenConfig)
	g := cfg.Groups["home-uplink"]
	g.Members = map[string]selector.Member{"phantom": {Wan: "phantom", Priority: 1}}
	cfg.Groups["home-uplink"] = g
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

func TestValidateRejectsMemberWanKeyMismatch(t *testing.T) {
	t.Parallel()
	cfg := mustParse(t, goldenConfig)
	g := cfg.Groups["home-uplink"]
	m := g.Members["primary"]
	m.Wan = "renamed"
	g.Members["primary"] = m
	cfg.Groups["home-uplink"] = g
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want wrap of ErrInvalidConfig", err)
	}
}

// TestValidateRejectsBadValues covers the value-range mirror of the
// lib's probe/group checks: one case per guard in validateProbe,
// validateThresholds, validateHysteresis, and validateGroup.
func TestValidateRejectsBadValues(t *testing.T) {
	t.Parallel()

	// setWan parses goldenConfig, applies fn to the "primary" WAN, and
	// writes it back — the value map can't be mutated in place.
	setWan := func(fn func(w *Wan)) func(t *testing.T) Config {
		return func(t *testing.T) Config {
			cfg := mustParse(t, goldenConfig)
			w := cfg.Wans["primary"]
			fn(&w)
			cfg.Wans["primary"] = w
			return cfg
		}
	}
	// setGroup is setWan's analogue for the "home-uplink" Group.
	setGroup := func(fn func(g *selector.Group)) func(t *testing.T) Config {
		return func(t *testing.T) Config {
			cfg := mustParse(t, goldenConfig)
			g := cfg.Groups["home-uplink"]
			fn(&g)
			cfg.Groups["home-uplink"] = g
			return cfg
		}
	}

	tests := []struct {
		name  string
		build func(t *testing.T) Config
	}{
		{"non-positive intervalMs", setWan(func(w *Wan) { w.Probe.IntervalMs = 0 })},
		{"non-positive timeoutMs", setWan(func(w *Wan) { w.Probe.TimeoutMs = 0 })},
		{"non-positive windowSize", setWan(func(w *Wan) { w.Probe.WindowSize = 0 })},
		{"unknown probe method", setWan(func(w *Wan) { w.Probe.Method = "tcp" })},
		{"unknown familyHealthPolicy", setWan(func(w *Wan) { w.Probe.FamilyHealthPolicy = "some" })},
		{"lossPctDown over 100", setWan(func(w *Wan) { w.Probe.Thresholds.LossPctDown = 101 })},
		{"lossPctUp negative", setWan(func(w *Wan) { w.Probe.Thresholds.LossPctUp = -1 })},
		{"loss thresholds inverted", setWan(func(w *Wan) {
			w.Probe.Thresholds.LossPctUp = 40
			w.Probe.Thresholds.LossPctDown = 30
		})},
		{"non-positive rttMsUp", setWan(func(w *Wan) { w.Probe.Thresholds.RttMsUp = 0 })},
		{"rtt thresholds inverted", setWan(func(w *Wan) {
			w.Probe.Thresholds.RttMsUp = 600
			w.Probe.Thresholds.RttMsDown = 500
		})},
		{"non-positive consecutiveUp", setWan(func(w *Wan) { w.Probe.Hysteresis.ConsecutiveUp = 0 })},
		{"non-positive consecutiveDown", setWan(func(w *Wan) { w.Probe.Hysteresis.ConsecutiveDown = 0 })},
		{"unknown strategy", setGroup(func(g *selector.Group) { g.Strategy = "bogus-strategy" })},
		{"non-positive table", setGroup(func(g *selector.Group) { g.Table = 0 })},
		{"non-positive mark", setGroup(func(g *selector.Group) { g.Mark = 0 })},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := tt.build(t)
			if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("Validate() = %v, want wrap of ErrInvalidConfig", err)
			}
		})
	}
}

func TestLoadReadsFile(t *testing.T) {
	t.Parallel()
	tmp := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(tmp, goldenConfig, 0o600); err != nil {
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

// mustParse parses raw, failing the test when Parse rejects it, so
// callers can mutate the result for their negative cases.
func mustParse(t testing.TB, raw []byte) Config {
	t.Helper()
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("baseline Parse failed unexpectedly: %v", err)
	}
	return cfg
}

// goldenWith returns goldenConfig with value set at the object path,
// creating the last key. It edits the decoded document rather than
// the text, so it does not depend on the golden's formatting.
func goldenWith(t testing.TB, value any, path ...string) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(goldenConfig, &doc); err != nil {
		t.Fatalf("Unmarshal(goldenConfig): %v", err)
	}
	obj := doc
	for _, key := range path[:len(path)-1] {
		next, ok := obj[key].(map[string]any)
		if !ok {
			t.Fatalf("goldenWith: %q is not an object in the golden", key)
		}
		obj = next
	}
	obj[path[len(path)-1]] = value
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return out
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
	f.Add([]byte(`{"schema":1,"global":{"statePath":"a","hooksDir":"b","metricsSocket":"c","logLevel":"info","hookTimeoutMs":5000},"wans":{},"groups":{}}`))
	f.Add([]byte(`{"schema":1,"wans":null,"groups":null}`))
	f.Add([]byte(`{"schema":"not-an-int"}`))
	f.Add([]byte(`{"schema":1,"bogusKey":true}`))
	f.Add([]byte(`{"schema":1} trailing`))
	f.Add(goldenConfig)

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
