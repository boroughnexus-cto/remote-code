package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// fleetMode represents the current operating mode of the agent fleet.
type fleetMode string

const (
	fleetModeNormal    fleetMode = "normal"
	fleetModeContain   fleetMode = "contain"    // pause spawning + no external writes
	fleetModeStabilize fleetMode = "stabilize"  // pause spawning + drain in-flight + allow reads
	fleetModeResume    fleetMode = "resume"      // transitional: clear overrides → normal
)

// fleetState is the global singleton tracking fleet operating mode. All field
// access must go through the mutex.
type fleetState struct {
	mu       sync.RWMutex
	mode     fleetMode
	paused   bool   // true = no new agents can be spawned
	noWrites bool   // true = external write integrations disabled (Plane, Komodo, n8n)
	setAt    time.Time
	setBy    string // "tui", "api", "ctrl+x"
	db       *sql.DB // may be nil (TUI-only mode); used for persistence
}

var globalFleetState = &fleetState{mode: fleetModeNormal}

// fleetStateSnapshot is a read-safe copy of fleetState for JSON serialisation.
type fleetStateSnapshot struct {
	Mode     fleetMode `json:"mode"`
	Paused   bool      `json:"paused"`
	NoWrites bool      `json:"no_writes"`
	SetAt    int64     `json:"set_at"`
	SetBy    string    `json:"set_by"`
}

// IsSpawnAllowed returns false when fleet is paused (contain or stabilize mode).
func (fs *fleetState) IsSpawnAllowed() bool {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	return !fs.paused
}

// IsWriteAllowed returns false in contain mode (external write integrations disabled).
func (fs *fleetState) IsWriteAllowed() bool {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	return !fs.noWrites
}

// Apply sets the fleet to a preset mode, persists it to DB, and logs the change.
func (fs *fleetState) Apply(mode fleetMode, setBy string) {
	fs.mu.Lock()
	prev := fs.mode

	switch mode {
	case fleetModeNormal, fleetModeResume:
		fs.mode, fs.paused, fs.noWrites = fleetModeNormal, false, false
	case fleetModeContain:
		fs.mode, fs.paused, fs.noWrites = fleetModeContain, true, true
	case fleetModeStabilize:
		fs.mode, fs.paused, fs.noWrites = fleetModeStabilize, true, false
	default:
		fs.mode = mode
	}
	fs.setAt = time.Now()
	fs.setBy = setBy
	newMode := fs.mode
	fs.mu.Unlock()

	log.Printf("fleet: mode changed %s → %s (by %s)", prev, newMode, setBy)
	fs.persistToDB() // best-effort; acquires its own RLock
}

// LoadFromDB restores fleet mode from the database on startup. Must be called
// after the DB is initialized. Safe to call with a nil db (no-op).
func (fs *fleetState) LoadFromDB(db *sql.DB) {
	if db == nil {
		return
	}
	fs.mu.Lock()
	fs.db = db
	fs.mu.Unlock()

	var modeStr, setBy string
	var setAt int64
	err := db.QueryRow(
		"SELECT mode, set_by, set_at FROM fleet_state WHERE id = 1",
	).Scan(&modeStr, &setBy, &setAt)
	if err == sql.ErrNoRows {
		return // fresh DB; default (normal) is already set
	}
	if err != nil {
		log.Printf("fleet: could not load state from DB (%v), using default", err)
		return
	}

	fs.mu.Lock()
	switch fleetMode(modeStr) {
	case fleetModeContain:
		fs.mode, fs.paused, fs.noWrites = fleetModeContain, true, true
	case fleetModeStabilize:
		fs.mode, fs.paused, fs.noWrites = fleetModeStabilize, true, false
	default:
		fs.mode, fs.paused, fs.noWrites = fleetModeNormal, false, false
	}
	if setAt > 0 {
		fs.setAt = time.Unix(setAt, 0)
	}
	fs.setBy = setBy
	restored := fs.mode
	fs.mu.Unlock()

	if restored != fleetModeNormal {
		log.Printf("fleet: ⚠ restored non-normal mode %s (set by %s) from DB", restored, setBy)
	}
}

// persistToDB writes the current mode to the fleet_state table (upsert).
// Best-effort: logs on failure but never panics.
func (fs *fleetState) persistToDB() {
	fs.mu.RLock()
	db := fs.db
	mode := string(fs.mode)
	setBy := fs.setBy
	fs.mu.RUnlock()

	if db == nil {
		return
	}
	_, err := db.Exec(
		`INSERT INTO fleet_state (id, mode, set_by, set_at)
		 VALUES (1, ?, ?, unixepoch())
		 ON CONFLICT(id) DO UPDATE SET mode=excluded.mode, set_by=excluded.set_by, set_at=excluded.set_at`,
		mode, setBy,
	)
	if err != nil {
		log.Printf("fleet: failed to persist state to DB: %v", err)
	}
}

// ModeString returns the current mode as a string (for status bar, API responses).
func (fs *fleetState) ModeString() string {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	return string(fs.mode)
}

// Snapshot returns a read-safe copy of current state for JSON serialisation.
func (fs *fleetState) Snapshot() fleetStateSnapshot {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	return fleetStateSnapshot{
		Mode:     fs.mode,
		Paused:   fs.paused,
		NoWrites: fs.noWrites,
		SetAt:    fs.setAt.Unix(),
		SetBy:    fs.setBy,
	}
}

// handleFleetAPI handles GET /api/swarm/fleet,
// POST /api/swarm/fleet/mode, and POST /api/swarm/fleet/halt.
func handleFleetAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Determine sub-path after /api/swarm/fleet
	sub := strings.TrimPrefix(r.URL.Path, "/api/swarm/fleet")
	sub = strings.TrimPrefix(sub, "/")

	switch sub {
	case "", "status":
		// GET /api/swarm/fleet — return current snapshot
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		json.NewEncoder(w).Encode(globalFleetState.Snapshot()) //nolint:errcheck

	case "mode":
		// POST /api/swarm/fleet/mode — body {"mode":"contain","set_by":"tui"}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Mode  fleetMode `json:"mode"`
			SetBy string    `json:"set_by"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
			return
		}
		if req.SetBy == "" {
			req.SetBy = "api"
		}
		globalFleetState.Apply(req.Mode, req.SetBy)
		json.NewEncoder(w).Encode(globalFleetState.Snapshot()) //nolint:errcheck

	case "halt":
		// POST /api/swarm/fleet/halt — emergency halt
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			SetBy string `json:"set_by"`
		}
		// best-effort decode: log failures but proceed (halt is emergency — must not block)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("fleet: halt request body decode failed (%v), proceeding with halt anyway", err)
		}
		setBy := req.SetBy
		if setBy == "" {
			setBy = "halt"
		}
		globalFleetState.Apply(fleetModeContain, setBy)
		log.Printf("⚠ FLEET HALT — all spawning suspended (by %s)", setBy)
		json.NewEncoder(w).Encode(globalFleetState.Snapshot()) //nolint:errcheck

	default:
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown fleet endpoint"}) //nolint:errcheck
	}
}
