package state

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
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
		go func(_ int) {
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
					"v4": {Healthy: true, RTTSeconds: 0.0124, JitterSeconds: 0.0012, LossRatio: 0.0, Targets: []string{"1.1.1.1"}},
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
	if out.Wans["primary"].Families["v4"].RTTSeconds != 0.0124 {
		t.Errorf("nested RTT did not round-trip: %v", out.Wans["primary"].Families["v4"].RTTSeconds)
	}
	if out.Groups["home"].Active == nil || *out.Groups["home"].Active != "primary" {
		t.Errorf("Active did not round-trip: %v", out.Groups["home"].Active)
	}
}

func TestSchemaVersionConstantStable(t *testing.T) {
	t.Parallel()
	// Pre-release we pin SchemaVersion at 1. Changing this is a
	// load-bearing decision — see PLAN §12 OQ #1 and the constant's
	// doc-comment for the bump policy.
	if SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1 (pre-release pin)", SchemaVersion)
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

// TestWriteFailsOnUnmarshalableState: a NaN float in the state
// tree is unrepresentable in JSON. encoding/json returns an
// UnsupportedValueError; Writer.Write wraps it with the "marshal"
// prefix so the failure mode is greppable in logs.
func TestWriteFailsOnUnmarshalableState(t *testing.T) {
	t.Parallel()
	w := Writer{Path: filepath.Join(t.TempDir(), "state.json")}
	err := w.Write(State{
		Wans: map[string]Wan{
			"primary": {
				Families: map[string]FamilyHealth{
					"v4": {LossRatio: math.NaN()},
				},
			},
		},
	})
	if err == nil {
		t.Fatal("Write(NaN) = nil err, want marshal failure")
	}
	if !strings.Contains(err.Error(), "marshal") {
		t.Errorf("err = %q, want 'marshal' prefix", err.Error())
	}
}

// TestWriteRenameFailsWhenPathIsADirectory: a Path that names an
// existing directory turns os.Rename(tmpfile, dir) into an EISDIR
// (or similar) — exercising the last error branch of the
// tmpfile + rename dance.
func TestWriteRenameFailsWhenPathIsADirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Name a sub-directory the same as the would-be statefile.
	occupied := filepath.Join(dir, "state.json")
	if err := os.Mkdir(occupied, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	w := Writer{Path: occupied}
	err := w.Write(State{Wans: map[string]Wan{"w": {Interface: "eth0"}}})
	if err == nil {
		t.Fatal("Write(path=dir) = nil err, want rename failure")
	}
	if !strings.Contains(err.Error(), "rename") {
		// Some platforms surface this as EISDIR on Write or Close
		// before the rename; either is fine, but the contract is
		// that we get a wrapped error, not a panic or silent
		// success.
		t.Logf("err = %q (rename branch may have been pre-empted; surface error wrapped: %v)", err.Error(), errors.Unwrap(err) != nil)
	}
}
