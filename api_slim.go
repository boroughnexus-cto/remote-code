package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
)

// handleAPI is the main REST API router. Maintains URL contract for MCP servers
// (tkn-remote-code-nuc, tkn-remote-code-auto).
func handleAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Content-Type", "application/json")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/")
	pathParts := strings.Split(path, "/")
	if len(pathParts) == 0 {
		http.Error(w, `{"error":"invalid path"}`, http.StatusBadRequest)
		return
	}

	ctx := context.Background()

	switch pathParts[0] {
	case "dashboard":
		handleDashboardAPI(w, r, ctx)
	case "agents":
		// MCP: rc_list_agents → returns sessions as "agents"
		handleSessionsAsAgentsAPI(w, r, ctx)
	case "task-executions":
		// MCP: rc_list_executions, rc_run_task, rc_send_input
		handleTaskExecutionsAPI(w, r, ctx, pathParts[1:])
	case "tmux-sessions":
		handleTmuxSessionsAPI(w, r)
	case "git":
		handleGitAPI(w, r, pathParts[1:])
	case "roots":
		handleRootsAPI(w, r)
	case "projects":
		handleProjectsAPI(w, r)
	case "swarm":
		handleSwarmSubAPI(w, r, ctx, pathParts[1:])
	default:
		http.Error(w, `{"error":"unknown endpoint"}`, http.StatusNotFound)
	}
}

// ─── Dashboard ───────────────────────────────────────────────────────────────

type DashboardStats struct {
	ActiveSessions           int           `json:"active_sessions"`
	Projects                 int           `json:"projects"`
	TaskExecutions           int           `json:"task_executions"`
	Agents                   int           `json:"agents"`
	GitChangesAwaitingReview []interface{} `json:"git_changes_awaiting_review"`
	AgentsWaitingForInput    []interface{} `json:"agents_waiting_for_input"`
	RemotePorts              []interface{} `json:"remote_ports"`
	DirectoryDevServers      []interface{} `json:"directory_dev_servers"`
}

func handleDashboardAPI(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	sessions, _ := listSessions(ctx)
	running := 0
	for _, s := range sessions {
		if s.Status == "running" {
			running++
		}
	}

	stats := DashboardStats{
		ActiveSessions:           running,
		Agents:                   0,
		Projects:                 0,
		TaskExecutions:           0,
		GitChangesAwaitingReview: []interface{}{},
		AgentsWaitingForInput:    []interface{}{},
		RemotePorts:              []interface{}{},
		DirectoryDevServers:      []interface{}{},
	}
	json.NewEncoder(w).Encode(stats)
}

// ─── Sessions as Agents (MCP compat) ────────────────────────────────────────

func handleSessionsAsAgentsAPI(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// MCP expects: {configured: [], detected: {agents: [...]}}
	type detectedAgent struct {
		Available bool   `json:"available"`
		Command   string `json:"command"`
		Name      string `json:"name"`
		Path      string `json:"path"`
	}

	claudePath, _ := exec.LookPath("claude")
	agents := []detectedAgent{{
		Available: claudePath != "",
		Command:   "claude",
		Name:      "claude",
		Path:      claudePath,
	}}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"configured": []interface{}{},
		"detected":   map[string]interface{}{"agents": agents},
	})
}

// ─── Task Executions (MCP compat) ───────────────────────────────────────────

func handleTaskExecutionsAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, pathParts []string) {
	switch r.Method {
	case http.MethodGet:
		// rc_list_executions → return sessions as executions
		sessions, _ := listSessions(ctx)
		json.NewEncoder(w).Encode(sessions)

	case http.MethodPost:
		// rc_run_task → create a new session
		if len(pathParts) > 0 {
			// Handle sub-resources like /:id/input
			if len(pathParts) >= 2 && pathParts[1] == "input" {
				handleSessionInput(w, r, ctx, pathParts[0])
				return
			}
		}
		var req struct {
			Name      string `json:"name"`
			Directory string `json:"directory"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Name == "" {
			req.Name = "session-" + generateID()
		}
		if req.Directory == "" {
			req.Directory = "."
		}
		s, err := spawnSession(ctx, req.Name, req.Directory, nil, nil)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(s)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func handleSessionInput(w http.ResponseWriter, r *http.Request, ctx context.Context, sessionID string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Input string `json:"input"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	s, err := getSession(ctx, sessionID)
	if err != nil {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
	if err := injectToSession(s.TmuxSession, req.Input); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ─── Tmux Sessions ──────────────────────────────────────────────────────────

type TmuxSessionInfo struct {
	Name    string `json:"name"`
	Created string `json:"created"`
	Preview string `json:"preview"`
}

func handleTmuxSessionsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}\t#{session_created_string}").Output()
	if err != nil {
		json.NewEncoder(w).Encode([]TmuxSessionInfo{})
		return
	}

	var sessions []TmuxSessionInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		created := ""
		if len(parts) > 1 {
			created = parts[1]
		}
		sessions = append(sessions, TmuxSessionInfo{
			Name:    parts[0],
			Created: created,
		})
	}
	json.NewEncoder(w).Encode(sessions)
}

// ─── Git ─────────────────────────────────────────────────────────────────────

func handleGitAPI(w http.ResponseWriter, r *http.Request, pathParts []string) {
	if len(pathParts) == 0 {
		http.Error(w, `{"error":"missing git subcommand"}`, http.StatusBadRequest)
		return
	}

	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = "."
	}

	switch pathParts[0] {
	case "status":
		out, err := runGitCommand(dir, "status", "--porcelain=v1")
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		branch, _ := runGitCommand(dir, "rev-parse", "--abbrev-ref", "HEAD")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"branch":   strings.TrimSpace(branch),
			"output":   out,
			"is_dirty": strings.TrimSpace(out) != "",
		})

	case "branches":
		out, err := runGitCommand(dir, "branch", "--format=%(refname:short)")
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		branches := strings.Split(strings.TrimSpace(out), "\n")
		json.NewEncoder(w).Encode(map[string]interface{}{"branches": branches})

	case "diff":
		out, _ := runGitCommand(dir, "diff")
		json.NewEncoder(w).Encode(map[string]string{"diff": out})

	case "log":
		out, _ := runGitCommand(dir, "log", "--oneline", "-20")
		json.NewEncoder(w).Encode(map[string]string{"log": out})

	default:
		http.Error(w, `{"error":"unknown git subcommand"}`, http.StatusBadRequest)
	}
}

func runGitCommand(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ─── Roots ───────────────────────────────────────────────────────────────────

func handleRootsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// Return unique directories from sessions
	sessions, _ := listSessions(context.Background())
	seen := make(map[string]bool)
	var roots []map[string]string
	for _, s := range sessions {
		if !seen[s.Directory] {
			seen[s.Directory] = true
			roots = append(roots, map[string]string{"path": s.Directory})
		}
	}
	if roots == nil {
		roots = []map[string]string{}
	}
	json.NewEncoder(w).Encode(roots)
}

// ─── Projects (minimal stub) ────────────────────────────────────────────────

func handleProjectsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	json.NewEncoder(w).Encode([]interface{}{})
}

// ─── Swarm sub-API ──────────────────────────────────────────────────────────

func handleSwarmSubAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, pathParts []string) {
	if len(pathParts) == 0 {
		http.Error(w, `{"error":"missing swarm subpath"}`, http.StatusNotFound)
		return
	}

	switch pathParts[0] {
	case "config":
		handleConfigAPI(w, r)
	case "pool":
		handlePoolStatusAPI(w, r)
	case "sessions":
		handleSwarmSessionsAPI(w, r, ctx, pathParts[1:])
	case "dashboard":
		handleSwarmDashboardAPI(w, r, ctx)
	default:
		http.Error(w, `{"error":"unknown swarm endpoint"}`, http.StatusNotFound)
	}
}

func handleSwarmSessionsAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, pathParts []string) {
	if len(pathParts) == 0 {
		switch r.Method {
		case http.MethodGet:
			sessions, err := listSessions(ctx)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
				return
			}
			if sessions == nil {
				sessions = []Session{}
			}
			json.NewEncoder(w).Encode(sessions)

		case http.MethodPost:
			var req struct {
				Name      string  `json:"name"`
				Directory string  `json:"directory"`
				ContextID *string `json:"context_id"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			if req.Name == "" {
				http.Error(w, `{"error":"name required"}`, http.StatusBadRequest)
				return
			}
			if req.Directory == "" {
				req.Directory = "."
			}
			s, err := spawnSession(ctx, req.Name, req.Directory, req.ContextID, nil)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(s)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}

	sessionID := pathParts[0]
	subPath := pathParts[1:]

	if len(subPath) == 0 {
		switch r.Method {
		case http.MethodGet:
			s, err := getSession(ctx, sessionID)
			if err != nil {
				http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(s)

		case http.MethodDelete:
			if err := deleteSession(ctx, sessionID); err != nil {
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}

	switch subPath[0] {
	case "terminal":
		s, err := getSession(ctx, sessionID)
		if err != nil {
			http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
			return
		}
		content, err := captureTerminal(s.TmuxSession)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"content": content})

	case "input":
		handleSessionInput(w, r, ctx, sessionID)

	default:
		http.Error(w, `{"error":"unknown session endpoint"}`, http.StatusNotFound)
	}
}

func handleSwarmDashboardAPI(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	sessions, _ := listSessions(ctx)
	running := 0
	for _, s := range sessions {
		if s.Status == "running" && !s.Hidden {
			running++
		}
	}

	poolInfo := map[string]interface{}{"enabled": false}
	if globalPool != nil {
		poolInfo = globalPool.Status()
		poolInfo["enabled"] = true
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"sessions": sessions,
		"running":  running,
		"total":    len(sessions),
		"pool":     poolInfo,
	})
}

func init() {
	// Silence unused import warnings
	_ = log.Printf
}
