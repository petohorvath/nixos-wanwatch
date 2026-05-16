// Package state owns the daemon's externalized view —
// `/run/wanwatch/state.json` — plus the hook runner that fires
// user scripts on Decision events.
//
// State writes are atomic (tmpfile + os.Rename) so readers see
// either the old or the new file, never a partial one.
//
// Hook execution is best-effort: failures are logged but don't
// block the apply transaction. PLAN §5.5 fixes the env-var contract;
// `hooks.go` implements the dispatcher.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SchemaVersion is the state-file schema version. Pre-release we
// keep it pinned at 1 — there are no external consumers and bumping
// for in-tree refactors is bookkeeping noise. The first tagged
// release freezes shape 1; future incompatible changes bump it.
const SchemaVersion = 1

// State is the daemon's externalized snapshot, written atomically
// on every Decision.
type State struct {
	Schema    int              `json:"schema"`
	UpdatedAt time.Time        `json:"updatedAt"`
	Wans      map[string]Wan   `json:"wans"`
	Groups    map[string]Group `json:"groups"`
}

// Wan is the per-WAN state slice.
type Wan struct {
	Interface string                  `json:"interface"`
	Carrier   string                  `json:"carrier"`   // "up" | "down" | "unknown"
	Operstate string                  `json:"operstate"` // IFLA_OPERSTATE textual
	Healthy   bool                    `json:"healthy"`
	Gateways  Gateways                `json:"gateways"`
	Families  map[string]FamilyHealth `json:"families"`
}

// Gateways is the per-WAN snapshot of the kernel's main-table
// default-route next-hops, one per family.
//
// Empty string for a family means either (a) the daemon has not
// yet observed a default route on that interface, or (b) the
// route is scope-link (point-to-point) and has no next-hop IP.
// Consumers needing to distinguish the two should consult the
// WAN's `pointToPoint` configuration.
type Gateways struct {
	V4 string `json:"v4"`
	V6 string `json:"v6"`
}

// FamilyHealth is the per-(WAN, family) probe-summary slice
// surfaced in state.json. The type was renamed from `Family` to
// avoid colliding with `probe.Family` (the IP-family enum used
// everywhere else). Numeric units are seconds and a [0, 1] ratio
// for parity with the Prometheus gauges.
type FamilyHealth struct {
	Healthy       bool     `json:"healthy"`
	RTTSeconds    float64  `json:"rttSeconds"`
	JitterSeconds float64  `json:"jitterSeconds"`
	LossRatio     float64  `json:"lossRatio"`
	Targets       []string `json:"targets"`
}

// Group is the per-Group state slice.
type Group struct {
	Active         *string    `json:"active"`      // nil = no member healthy
	ActiveSince    *time.Time `json:"activeSince"` // nil if never active
	DecisionsTotal int        `json:"decisionsTotal"`
	Strategy       string     `json:"strategy"`
}

// Writer holds the destination path. One Writer per daemon
// instance; `mu` serializes concurrent Write calls so the
// tmpfile + rename pattern can't be raced into a corrupted
// snapshot. The daemon's apply loop is single-goroutine today
// and would be safe without the mutex, but contract-enforcing
// is cheap and survives future refactors.
type Writer struct {
	Path string
	mu   sync.Mutex
}

// Write serializes `s` to JSON and writes it atomically to
// `w.Path` via the standard tmpfile + rename pattern. The
// resulting file's permissions are 0o644 — readable by Telegraf,
// `wanwatchctl`, ad-hoc scripts.
//
// On any error after the tmpfile is created, the tmpfile is
// removed (best-effort) so we don't litter the state directory.
//
// `UpdatedAt` is filled with `time.Now().UTC()` at write time when
// the caller passes the zero value; otherwise the caller's timestamp
// is preserved (so a Decision can stamp state.json and the hook env
// vars with the same wall-clock moment).
func (w *Writer) Write(s State) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = time.Now().UTC()
	}
	s.Schema = SchemaVersion

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal: %w", err)
	}

	dir := filepath.Dir(w.Path)
	tmp, err := os.CreateTemp(dir, filepath.Base(w.Path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("state: create tmpfile in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	// Tmpfile cleanup is hoisted out of every error path; the
	// `renamed` flag tells us when the file has been promoted to
	// its final name and no longer needs cleanup.
	renamed := false
	defer func() {
		if !renamed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("state: write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("state: chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("state: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, w.Path); err != nil {
		return fmt.Errorf("state: rename %s → %s: %w", tmpName, w.Path, err)
	}
	renamed = true
	return nil
}
