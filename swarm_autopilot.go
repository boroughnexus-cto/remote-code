package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

// ─── Autopilot ────────────────────────────────────────────────────────────────
//
// Autopilot enables per-session Plane→goal sync. When a session has
// autopilot_enabled=1 and an autopilot_plane_project_id set, the Plane adapter
// will poll that project and create swarm goals for "started" issues.
//
// API:
//   PATCH /api/swarm/sessions/:id/autopilot
//   {"enabled": true, "plane_project_id": "<uuid>"}
//
// The TUI toggles this with the A key.

// handleSwarmAutopilotAPI handles PATCH /api/swarm/sessions/:id/autopilot
func handleSwarmAutopilotAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, sessionID string) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPatch {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Enabled        bool   `json:"enabled"`
		PlaneProjectID string `json:"plane_project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON"})
		return
	}

	if req.Enabled && req.PlaneProjectID == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "plane_project_id required when enabling autopilot"})
		return
	}

	enabledInt := 0
	if req.Enabled {
		enabledInt = 1
	}

	now := time.Now().Unix()
	if _, err := database.ExecContext(ctx,
		"UPDATE swarm_sessions SET autopilot_enabled=?, autopilot_plane_project_id=?, updated_at=? WHERE id=?",
		enabledInt, swarmNullStr(req.PlaneProjectID), now, sessionID,
	); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	writeSwarmEvent(ctx, sessionID, "", "", "autopilot_toggled",
		fmt.Sprintf("enabled=%v project=%s", req.Enabled, req.PlaneProjectID))
	swarmBroadcaster.schedule(sessionID)

	action := "disabled"
	if req.Enabled {
		action = "enabled"
		// Trigger an immediate Plane sync for this session
		go planeSyncSession(context.Background(), req.PlaneProjectID, sessionID)
	}
	log.Printf("swarm/autopilot: %s for session %s", action, sessionID[:8])

	json.NewEncoder(w).Encode(map[string]interface{}{
		"session_id":        sessionID,
		"autopilot_enabled": req.Enabled,
		"plane_project_id":  req.PlaneProjectID,
	})
}

// ─── Integration config validation ───────────────────────────────────────────

// validateIntegrationConfig logs warnings (non-fatal) for partial integration
// configurations at startup. Helps catch missing env vars early.
func validateIntegrationConfig() {
	type check struct {
		name string
		vars []string
	}
	checks := []check{
		{"Plane adapter", []string{"PLANE_API_URL", "PLANE_API_KEY", "PLANE_WORKSPACE"}},
		{"Komodo auto-deploy", []string{"KOMODO_API_URL", "KOMODO_API_KEY", "KOMODO_API_SECRET"}},
		{"Obsidian notes", []string{"OBSIDIAN_API_URL", "OBSIDIAN_API_KEY", "OBSIDIAN_VAULT_FOLDER"}},
	}
	for _, c := range checks {
		set := 0
		for _, v := range c.vars {
			if os.Getenv(v) != "" {
				set++
			}
		}
		if set > 0 && set < len(c.vars) {
			log.Printf("swarm/config: WARNING — %s partially configured (%d/%d vars set); feature may not work",
				c.name, set, len(c.vars))
		}
		if set == len(c.vars) {
			log.Printf("swarm/config: %s configured ✓", c.name)
		}
	}
}
