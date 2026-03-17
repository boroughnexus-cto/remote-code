package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SwarmEscalation represents a pending human-intervention request.
type SwarmEscalation struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	TaskID    string `json:"task_id"`
	Reason    string `json:"reason"`
	Ts        int64  `json:"ts"`
}

// validEscIDRe matches filenames written by writeEscalation:
//   esc_<nanosecond_ts>_<taskID_8_chars>.json
//   e.g. esc_1710698400000000000_ab12cd34
var validEscIDRe = regexp.MustCompile(`^esc_[0-9]+_[a-f0-9]+$`)

// loadEscalations scans the escalations dir and returns pending (unclaimed) entries.
func loadEscalations(sessionID string) ([]SwarmEscalation, error) {
	dir := swarmEscalationsDir(sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []SwarmEscalation{}, nil
		}
		return nil, err
	}

	var out []SwarmEscalation
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue // skip .claimed and other files
		}
		stem := strings.TrimSuffix(name, ".json")
		if !validEscIDRe.MatchString(stem) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var raw map[string]string
		if err := json.Unmarshal(data, &raw); err != nil {
			continue
		}
		ts, _ := strconv.ParseInt(raw["ts"], 10, 64)
		out = append(out, SwarmEscalation{
			ID:        stem,
			SessionID: raw["session_id"],
			AgentID:   raw["agent_id"],
			TaskID:    raw["task_id"],
			Reason:    raw["reason"],
			Ts:        ts,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ts < out[j].Ts })
	return out, nil
}

// handleSwarmEscalationsAPI handles:
//   GET  /api/swarm/sessions/:id/escalations
//   POST /api/swarm/sessions/:id/escalations/:escID/respond
func handleSwarmEscalationsAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, sessionID string, pathParts []string) {
	w.Header().Set("Content-Type", "application/json")

	if len(pathParts) == 0 {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		escs, err := loadEscalations(sessionID)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(escs)
		return
	}

	// /escalations/:escID/respond
	if len(pathParts) < 2 || pathParts[1] != "respond" || r.Method != http.MethodPost {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	escID := pathParts[0]

	// Strict ID validation — path traversal guard
	if !validEscIDRe.MatchString(escID) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid escalation id"})
		return
	}

	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "text required"})
		return
	}

	// Build path and verify it's within the escalations dir (defence in depth)
	dir := swarmEscalationsDir(sessionID)
	path := filepath.Join(dir, escID+".json")
	cleanPath := filepath.Clean(path)
	cleanDir := filepath.Clean(dir)
	if !strings.HasPrefix(cleanPath, cleanDir+string(filepath.Separator)) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid path"})
		return
	}

	// Atomic claim via Rename — only one responder can win this race
	claimedPath := path + ".claimed"
	if err := os.Rename(path, claimedPath); err != nil {
		if os.IsNotExist(err) {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": "already responded"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Read the claimed file for agent/task IDs
	data, _ := os.ReadFile(claimedPath)
	var raw map[string]string
	json.Unmarshal(data, &raw)
	agentID := raw["agent_id"]
	taskID := raw["task_id"]

	// Inject response to agent
	if agentID != "" {
		if err := injectToSwarmAgent(ctx, agentID, req.Text); err != nil {
			// Restore file so operator can retry
			os.Rename(claimedPath, path) //nolint:errcheck
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "inject failed: " + err.Error()})
			return
		}
	}

	// Transition task back to running only from valid blocked states
	if taskID != "" {
		var curStage string
		database.QueryRowContext(ctx, "SELECT COALESCE(stage,'') FROM swarm_tasks WHERE id=?", taskID).Scan(&curStage)
		if curStage == "blocked" || curStage == "needs_human" || curStage == "needs_review" {
			transitionTask(ctx, taskID, "running") //nolint:errcheck
		}
	}

	os.Remove(claimedPath) //nolint:errcheck
	writeSwarmEvent(ctx, sessionID, agentID, taskID, "escalation_resolved",
		truncate(strings.TrimSpace(req.Text), 80))

	// Note the ts of the resolution so the TUI's escalation list
	// updates promptly on the next poll.
	database.ExecContext(ctx, //nolint:errcheck
		"UPDATE swarm_agents SET last_event_ts=? WHERE id=?",
		time.Now().Unix(), agentID,
	)
	swarmBroadcaster.schedule(sessionID)
	w.WriteHeader(http.StatusNoContent)
}
