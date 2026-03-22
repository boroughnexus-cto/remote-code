package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TODO swarm.go: add case "fleet" in handleSwarmAPI to route to handleFleetAPI
// TODO main.go: no init needed — globalFleetState initialises at var declaration time

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

// Apply sets the fleet to a preset mode. Logs the change.
func (fs *fleetState) Apply(mode fleetMode, setBy string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	prev := fs.mode

	switch mode {
	case fleetModeNormal:
		fs.mode = fleetModeNormal
		fs.paused = false
		fs.noWrites = false
	case fleetModeContain:
		fs.mode = fleetModeContain
		fs.paused = true
		fs.noWrites = true
	case fleetModeStabilize:
		fs.mode = fleetModeStabilize
		fs.paused = true
		fs.noWrites = false
	case fleetModeResume:
		// Transitional: clear all overrides → set mode to normal
		fs.mode = fleetModeNormal
		fs.paused = false
		fs.noWrites = false
	default:
		fs.mode = mode
	}

	fs.setAt = time.Now()
	fs.setBy = setBy

	log.Printf("fleet: mode changed %s → %s (by %s)", prev, fs.mode, setBy)
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
		// best-effort decode; ignore errors (halt is emergency — proceed regardless)
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
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
