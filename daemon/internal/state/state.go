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
	"time"
)

// Version is the state-file schema version. Bumped on incompatible
// shape changes.
const Version = 1

// State is the daemon's externalized snapshot, written atomically
// on every Decision.
type State struct {
	Version   int              `json:"version"`
	UpdatedAt time.Time        `json:"updatedAt"`
	Wans      map[string]Wan   `json:"wans"`
	Groups    map[string]Group `json:"groups"`
}

// Wan is the per-WAN state slice.
type Wan struct {
	Interface string            `json:"interface"`
	Carrier   string            `json:"carrier"`   // "up" | "down" | "unknown"
	Operstate string            `json:"operstate"` // IFLA_OPERSTATE textual
	Healthy   bool              `json:"healthy"`
	Families  map[string]Family `json:"families"`
}

// Family is the per-(WAN, family) probe-summary slice.
type Family struct {
	Healthy  bool     `json:"healthy"`
	RTTMs    float64  `json:"rttMs"`
	JitterMs float64  `json:"jitterMs"`
	LossPct  float64  `json:"lossPct"`
	Targets  []string `json:"targets"`
}

// Group is the per-Group state slice.
type Group struct {
	Active         *string    `json:"active"`      // nil = no member healthy
	ActiveSince    *time.Time `json:"activeSince"` // nil if never active
	DecisionsTotal int        `json:"decisionsTotal"`
	Strategy       string     `json:"strategy"`
}

// Writer holds the destination path. One Writer per daemon
// instance; not safe for concurrent Write calls — serialize at
// the caller (the daemon's apply loop, which already serializes).
type Writer struct {
	Path string
}

// Write serializes `s` to JSON and writes it atomically to
// `w.Path` via the standard tmpfile + rename pattern. The
// resulting file's permissions are 0o644 — readable by Telegraf,
// `wanwatchctl`, ad-hoc scripts.
//
// On any error after the tmpfile is created, the tmpfile is
// removed (best-effort) so we don't litter the state directory.
//
// `UpdatedAt` is overwritten with `time.Now().UTC()` at write
// time — caller-provided values are ignored.
func (w *Writer) Write(s State) error {
	s.UpdatedAt = time.Now().UTC()
	s.Version = Version

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

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("state: write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("state: chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("state: close %s: %w", tmpName, err)
	}

	if err := os.Rename(tmpName, w.Path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("state: rename %s → %s: %w", tmpName, w.Path, err)
	}
	return nil
}
