package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os/exec"
	"regexp"
	"time"

	xansi "github.com/charmbracelet/x/ansi"
)

// validTmuxTargetRe restricts tmux target names to safe characters,
// preventing tmux target-grammar injection (colons, percent signs, etc).
var validTmuxTargetRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func validTmuxTarget(s string) bool {
	return validTmuxTargetRe.MatchString(s)
}

// handleSwarmTerminalAPI serves GET /api/swarm/sessions/:id/agents/:agentID/terminal
// Terminal capture runs server-side so the TUI doesn't need filesystem/tmux access.
func handleSwarmTerminalAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, sessionID, agentID string) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Verify agent belongs to session
	var tmuxSession string
	err := database.QueryRowContext(ctx,
		"SELECT COALESCE(tmux_session,'') FROM swarm_agents WHERE id=? AND session_id=?",
		agentID, sessionID,
	).Scan(&tmuxSession)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if tmuxSession == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"content": "(agent not running)"})
		return
	}

	// Guard against tmux target injection
	if !validTmuxTarget(tmuxSession) {
		log.Printf("swarm: rejected invalid tmux target %q for agent %s", tmuxSession, agentID[:8])
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(tctx,
		"tmux", "capture-pane", "-p", "-S", "-300", "-t", tmuxSession,
	).Output()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	content := xansi.Strip(string(out))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"content": content})
}
