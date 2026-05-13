package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteCreatesFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	w := Writer{Path: path}

	if err := w.Write(State{}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat after Write: %v", err)
	}
}

func TestWriteIsAtomic(t *testing.T) {
	t.Parallel()
	// Write an initial file, then a second time; the inode may
	// change (because of rename), but the file at `path` must
	// always be valid JSON at every observable moment. Hard to
	// race in a single-process test, so we check the easy part:
	// after Write, the result parses as a complete State.
	path := filepath.Join(t.TempDir(), "state.json")
	w := Writer{Path: path}

	first := State{
		Wans: map[string]Wan{"primary": {Interface: "eth0"}},
	}
	second := State{
		Wans: map[string]Wan{"primary": {Interface: "eth0"}, "backup": {Interface: "wwan0"}},
	}

	if err := w.Write(first); err != nil {
		t.Fatalf("Write first: %v", err)
	}
	if err := w.Write(second); err != nil {
		t.Fatalf("Write second: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var out State
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := len(out.Wans); got != 2 {
		t.Errorf("len(Wans) = %d, want 2 (second write should have replaced first)", got)
	}
}

func TestWriteSetsSchemaAndUpdatedAt(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	w := Writer{Path: path}

	if err := w.Write(State{Schema: 99}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var out State
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.Schema != SchemaVersion {
		t.Errorf("Schema = %d, want %d (caller-supplied 99 must be overwritten)", out.Schema, SchemaVersion)
	}
	if out.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt is zero — Write must stamp it")
	}
}

func TestWritePermissionMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	w := Writer{Path: path}

	if err := w.Write(State{}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("perm = %o, want 0644", got)
	}
}

func TestWriteErrorsOnMissingParentDir(t *testing.T) {
	t.Parallel()
	// CreateTemp creates the tmpfile in the parent of `Path`. If
	// that directory doesn't exist, CreateTemp itself fails — the
	// error must propagate, not be swallowed.
	bogus := filepath.Join(t.TempDir(), "no-such-dir", "state.json")
	w := Writer{Path: bogus}

	err := w.Write(State{})
	if err == nil {
		t.Fatal("Write to non-existent dir returned nil error")
	}
}

func TestWriteConcurrentSerializationSafe(t *testing.T) {
	t.Parallel()
	// Writer isn't safe for concurrent calls per the doc, but
	// the file-system atomicity must hold even if multiple
	// processes call Write concurrently. Sanity test: many
	// sequential writes, parse each result.
	path := filepath.Join(t.TempDir(), "state.json")
	w := Writer{Path: path}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			_ = w.Write(State{Wans: map[string]Wan{"w": {Interface: "eth0"}}})
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var out State
	if err := json.Unmarshal(data, &out); err != nil {
		t.Errorf("after concurrent writes, file did not parse cleanly: %v", err)
	}
}

func TestWriteEmbedsFamilyAndGroupState(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	w := Writer{Path: path}

	active := "primary"
	s := State{
		Wans: map[string]Wan{
			"primary": {
				Interface: "eth0",
				Carrier:   "up",
				Operstate: "up",
				Healthy:   true,
				Families: map[string]FamilyHealth{
					"v4": {Healthy: true, RTTMs: 12.4, JitterMs: 1.2, LossPct: 0.0, Targets: []string{"1.1.1.1"}},
				},
			},
		},
		Groups: map[string]Group{
			"home": {
				Active:         &active,
				DecisionsTotal: 1,
				Strategy:       "primary-backup",
			},
		},
	}

	if err := w.Write(s); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, _ := os.ReadFile(path)
	var out State
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.Wans["primary"].Families["v4"].RTTMs != 12.4 {
		t.Errorf("nested RTT did not round-trip: %v", out.Wans["primary"].Families["v4"].RTTMs)
	}
	if out.Groups["home"].Active == nil || *out.Groups["home"].Active != "primary" {
		t.Errorf("Active did not round-trip: %v", out.Groups["home"].Active)
	}
}

func TestSchemaVersionConstantStable(t *testing.T) {
	t.Parallel()
	// The SchemaVersion constant pairs with the schema bump
	// procedure in PLAN §12 OQ #1. A change here is a load-bearing
	// decision.
	//
	// Schema 2 added the per-WAN `gateways.{v4,v6}` field.
	if SchemaVersion != 2 {
		t.Errorf("SchemaVersion = %d, want 2 (gateways field landed)", SchemaVersion)
	}
}

func TestWriteEmitsPerWanGateways(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	w := Writer{Path: path}

	s := State{
		Wans: map[string]Wan{
			"primary": {
				Interface: "eth0",
				Gateways:  Gateways{V4: "192.0.2.1", V6: "2001:db8::1"},
			},
			"ptp": {
				Interface: "wg0",
				// Scope-link / point-to-point: both empty.
				Gateways: Gateways{},
			},
		},
	}

	if err := w.Write(s); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, _ := os.ReadFile(path)
	var out State
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := out.Wans["primary"].Gateways.V4; got != "192.0.2.1" {
		t.Errorf("primary.gateways.v4 = %q, want 192.0.2.1", got)
	}
	if got := out.Wans["primary"].Gateways.V6; got != "2001:db8::1" {
		t.Errorf("primary.gateways.v6 = %q, want 2001:db8::1", got)
	}
	if got := out.Wans["ptp"].Gateways.V4; got != "" {
		t.Errorf("ptp.gateways.v4 = %q, want empty", got)
	}
}

// Sanity-check we caught the os-not-exist case explicitly rather
// than swallowing it.
func TestWriteReturnsRecognizableErrors(t *testing.T) {
	t.Parallel()
	w := Writer{Path: "/nonexistent-dir/state.json"}
	err := w.Write(State{})
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("Write error has no wrapped underlying error: %v", err)
	}
}
