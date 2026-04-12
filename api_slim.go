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
	stats, _ := globalServices.Dashboard(ctx)
	json.NewEncoder(w).Encode(stats)
}

// ─── Sessions as Agents (MCP compat) ────────────────────────────────────────

func handleSessionsAsAgentsAPI(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	agents, _ := globalServices.ListAgents(ctx)
	json.NewEncoder(w).Encode(agents)
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
		s, err := spawnSession(ctx, req.Name, req.Directory, nil, nil, nil)
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
	sessions, _ := globalServices.TmuxSessions()
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
		status, err := globalServices.GitStatus(dir)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(status)

	case "branches":
		branches, err := globalServices.GitBranches(dir, false)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"branches": branches})

	case "diff":
		diff, _ := globalServices.GitDiff(dir, false)
		json.NewEncoder(w).Encode(map[string]string{"diff": diff})

	case "log":
		gitLog, _ := globalServices.GitLog(dir)
		json.NewEncoder(w).Encode(map[string]string{"log": gitLog})

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
	roots, _ := globalServices.ListRoots(context.Background())
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
			refreshSessionStatuses(ctx)
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
				Mission   *string `json:"mission"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			if req.Name == "" {
				http.Error(w, `{"error":"name required"}`, http.StatusBadRequest)
				return
			}
			if req.Directory == "" {
				req.Directory = "."
			}
			s, err := spawnSession(ctx, req.Name, req.Directory, req.ContextID, nil, req.Mission)
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

		case http.MethodPatch:
			var body struct {
				Name    string  `json:"name"`
				Mission *string `json:"mission"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
				return
			}
			if body.Name != "" {
				if err := renameSession(ctx, sessionID, body.Name); err != nil {
					http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
					return
				}
			}
			if body.Mission != nil {
				if err := updateSessionMission(ctx, sessionID, *body.Mission); err != nil {
					http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
					return
				}
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
	dashboard, _ := globalServices.SwarmDashboard(ctx)
	json.NewEncoder(w).Encode(dashboard)
}

func init() {
	// Silence unused import warnings
	_ = log.Printf
}
