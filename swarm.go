package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// -----------------
// Swarm Types
// -----------------

type SwarmSession struct {
	ID                    string  `json:"id"`
	Name                  string  `json:"name"`
	AutopilotEnabled      bool    `json:"autopilot_enabled"`
	AutopilotPlaneProject *string `json:"autopilot_plane_project_id,omitempty"`
	CreatedAt             int64   `json:"created_at"`
	UpdatedAt             int64   `json:"updated_at"`
}

// WorkQueueItem is the BFF struct returned by GET /api/swarm/plane/issues.
// It wraps Plane issue data without leaking raw Plane API structure to the TUI.
type WorkQueueItem struct {
	PlaneIssueID string `json:"plane_issue_id"`
	Title        string `json:"title"`
	Priority     string `json:"priority"`
	SequenceID   int    `json:"sequence_id"`
	StateGroup   string `json:"state_group"`
}

type SwarmAgent struct {
	ID            string  `json:"id"`
	SessionID     string  `json:"session_id"`
	Name          string  `json:"name"`
	Role          string  `json:"role"`
	WorktreePath  *string `json:"worktree_path"`
	TmuxSession   *string `json:"tmux_session"`
	Project       *string `json:"project"`
	RepoPath      *string `json:"repo_path"`
	Status        string  `json:"status"`
	CurrentFile   *string `json:"current_file"`
	CurrentTaskID *string `json:"current_task_id"`
	LatestNote    *string `json:"latest_note"`
	Mission       *string `json:"mission"`
	ContextPct    float64 `json:"context_pct"`
	ContextState  string  `json:"context_state"`
	ModelName       string  `json:"model_name,omitempty"`
	TokensUsed      int64   `json:"tokens_used,omitempty"`
	StatusChangedAt int64   `json:"status_changed_at,omitempty"`
	CreatedAt       int64   `json:"created_at"`
}

type SwarmTask struct {
	ID            string   `json:"id"`
	SessionID     string   `json:"session_id"`
	Title         string   `json:"title"`
	Description   *string  `json:"description"`
	Stage         string   `json:"stage"`
	AgentID       *string  `json:"agent_id"`
	Project       *string  `json:"project"`
	Branch        *string  `json:"branch"`
	WorktreePath  *string  `json:"worktree_path"`
	PRUrl         *string  `json:"pr_url"`
	GoalID        *string  `json:"goal_id,omitempty"`
	Confidence    *float64 `json:"confidence,omitempty"`
	TokensUsed    *int64   `json:"tokens_used,omitempty"`
	BlockedReason *string  `json:"blocked_reason,omitempty"`
	Phase         *string  `json:"phase,omitempty"`
	PhaseOrder    *int64   `json:"phase_order,omitempty"`
	CIStatus      *string  `json:"ci_status,omitempty"`
	CIRunUrl      *string  `json:"ci_run_url,omitempty"`
	StageChangedAt int64   `json:"stage_changed_at,omitempty"`
	CreatedAt     int64    `json:"created_at"`
	UpdatedAt     int64    `json:"updated_at"`
}

type SwarmEvent struct {
	ID        int64  `json:"id"`
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	Type      string `json:"type"`
	Payload   string `json:"payload,omitempty"`
	Ts        int64  `json:"ts"`
}

type SwarmState struct {
	Session     SwarmSession      `json:"session"`
	Agents      []SwarmAgent      `json:"agents"`
	Tasks       []SwarmTask       `json:"tasks"`
	Events      []SwarmEvent      `json:"events"`
	Goals       []SwarmGoal       `json:"goals"`
	Escalations []SwarmEscalation `json:"escalations"`
}

// -----------------
// WebSocket Hub
// -----------------

// wsClient wraps a WebSocket connection with a per-connection write mutex.
// Gorilla WS requires one concurrent writer per connection; the mutex prevents
// races between the debouncer timer and other broadcast paths.
type wsClient struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

type SwarmHub struct {
	mu      sync.RWMutex
	clients map[string]map[*wsClient]bool
}

var swarmHub = &SwarmHub{
	clients: make(map[string]map[*wsClient]bool),
}

// broadcastDebouncer coalesces rapid mutations into a single broadcast per session,
// eliminating goroutine-per-mutation races and out-of-order state snapshots.
type broadcastDebouncer struct {
	mu      sync.Mutex
	pending map[string]*time.Timer
}

var swarmBroadcaster = &broadcastDebouncer{
	pending: make(map[string]*time.Timer),
}

func (b *broadcastDebouncer) schedule(sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t, ok := b.pending[sessionID]; ok {
		// Reset the timer — coalesces any mutations within the window
		t.Reset(100 * time.Millisecond)
		return
	}
	b.pending[sessionID] = time.AfterFunc(100*time.Millisecond, func() {
		b.mu.Lock()
		delete(b.pending, sessionID)
		b.mu.Unlock()
		broadcastSwarmState(sessionID)
	})
}

// scheduleWithDelay schedules fn to run after delay, coalescing rapid calls.
func (b *broadcastDebouncer) scheduleWithDelay(key string, delay time.Duration, fn func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t, ok := b.pending[key]; ok {
		t.Reset(delay)
		return
	}
	b.pending[key] = time.AfterFunc(delay, func() {
		b.mu.Lock()
		delete(b.pending, key)
		b.mu.Unlock()
		fn()
	})
}

func (h *SwarmHub) subscribe(sessionID string, conn *websocket.Conn) *wsClient {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[sessionID] == nil {
		h.clients[sessionID] = make(map[*wsClient]bool)
	}
	client := &wsClient{conn: conn}
	h.clients[sessionID][client] = true
	return client
}

func (h *SwarmHub) unsubscribe(sessionID string, client *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[sessionID] != nil {
		delete(h.clients[sessionID], client)
	}
}

func (h *SwarmHub) broadcast(sessionID string, msg interface{}) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("swarm hub: marshal error: %v", err)
		return
	}
	h.mu.RLock()
	clients := make([]*wsClient, 0, len(h.clients[sessionID]))
	for c := range h.clients[sessionID] {
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	// Collect failures after write loop, then unsubscribe to avoid modifying map during iteration
	var failed []*wsClient
	for _, c := range clients {
		c.mu.Lock()
		err := c.conn.WriteMessage(websocket.TextMessage, data)
		c.mu.Unlock()
		if err != nil {
			log.Printf("swarm hub: write error: %v", err)
			failed = append(failed, c)
		}
	}
	for _, c := range failed {
		h.unsubscribe(sessionID, c)
	}
}

// -----------------
// Helper Functions
// -----------------

func generateSwarmID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x%x%x%x%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// writeSwarmEvent logs an event to the swarm_events table.
func writeSwarmEvent(ctx context.Context, sessionID, agentID, taskID, eventType, payload string) {
	if _, err := database.ExecContext(ctx,
		"INSERT INTO swarm_events (session_id, agent_id, task_id, type, payload, ts) VALUES (?, ?, ?, ?, ?, ?)",
		sessionID, swarmNullStr(agentID), swarmNullStr(taskID), eventType, swarmNullStr(payload), time.Now().Unix(),
	); err != nil {
		log.Printf("swarm: writeSwarmEvent error: %v", err)
	}
}

func swarmNullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// isValidModelName returns true if s is safe to pass as a --model flag value.
// Allows alphanumeric, hyphen, dot, colon, underscore; max 128 chars.
func isValidModelName(s string) bool {
	if len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == ':' || c == '_') {
			return false
		}
	}
	return true
}

func scanNullString(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	return &ns.String
}

// -----------------
// DB Helpers
// -----------------

func getSwarmState(ctx context.Context, sessionID string) (*SwarmState, error) {
	var session SwarmSession
	var autopilotPlaneProject sql.NullString
	var autopilotEnabled int
	err := database.QueryRowContext(ctx,
		`SELECT id, name, COALESCE(autopilot_enabled,0), COALESCE(autopilot_plane_project_id,''), created_at, updated_at
		 FROM swarm_sessions WHERE id = ?`,
		sessionID,
	).Scan(&session.ID, &session.Name, &autopilotEnabled, &autopilotPlaneProject, &session.CreatedAt, &session.UpdatedAt)
	session.AutopilotEnabled = autopilotEnabled == 1
	if autopilotPlaneProject.Valid && autopilotPlaneProject.String != "" {
		session.AutopilotPlaneProject = &autopilotPlaneProject.String
	}
	if err != nil {
		return nil, err
	}

	agentRows, err := database.QueryContext(ctx,
		`SELECT a.id, a.session_id, a.name, a.role, a.worktree_path, a.tmux_session, a.project,
		        a.repo_path, a.status, a.current_file, a.current_task_id, a.mission,
		        COALESCE(a.context_pct,0), COALESCE(a.context_state,'normal'), a.created_at,
		        (SELECT content FROM swarm_agent_notes WHERE agent_id = a.id ORDER BY created_at DESC LIMIT 1),
		        COALESCE(a.model_name,''), a.tokens_used, a.status_changed_at
		 FROM swarm_agents a WHERE a.session_id = ?
		 ORDER BY CASE WHEN a.role = 'orchestrator' THEN 0 ELSE 1 END, a.created_at ASC`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer agentRows.Close()

	agents := []SwarmAgent{}
	for agentRows.Next() {
		var a SwarmAgent
		var worktreePath, tmuxSession, project, repoPath, currentFile, currentTaskID, mission, latestNote sql.NullString
		var tokensUsed, statusChangedAt sql.NullInt64
		if err := agentRows.Scan(&a.ID, &a.SessionID, &a.Name, &a.Role,
			&worktreePath, &tmuxSession, &project,
			&repoPath, &a.Status,
			&currentFile, &currentTaskID, &mission,
			&a.ContextPct, &a.ContextState, &a.CreatedAt, &latestNote,
			&a.ModelName, &tokensUsed, &statusChangedAt); err != nil {
			return nil, err
		}
		a.WorktreePath = scanNullString(worktreePath)
		a.TmuxSession = scanNullString(tmuxSession)
		a.Project = scanNullString(project)
		a.RepoPath = scanNullString(repoPath)
		a.CurrentFile = scanNullString(currentFile)
		a.CurrentTaskID = scanNullString(currentTaskID)
		a.Mission = scanNullString(mission)
		a.LatestNote = scanNullString(latestNote)
		if tokensUsed.Valid {
			a.TokensUsed = tokensUsed.Int64
		}
		if statusChangedAt.Valid {
			a.StatusChangedAt = statusChangedAt.Int64
		}
		agents = append(agents, a)
	}

	taskRows, err := database.QueryContext(ctx,
		`SELECT id, session_id, title, description, stage, agent_id, project,
		        branch, worktree_path, pr_url, goal_id, confidence, tokens_used, blocked_reason,
		        phase, phase_order, ci_status, ci_run_url,
		        created_at, updated_at, stage_changed_at
		 FROM swarm_tasks WHERE session_id = ?
		 ORDER BY COALESCE(phase_order, 9999), created_at ASC`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer taskRows.Close()

	tasks := []SwarmTask{}
	for taskRows.Next() {
		var t SwarmTask
		var description, agentID, project, branch, worktreePath, prUrl, goalID, blockedReason sql.NullString
		var phase, ciStatus, ciRunUrl sql.NullString
		var confidence sql.NullFloat64
		var tokensUsed, phaseOrder, stageChangedAt sql.NullInt64
		if err := taskRows.Scan(&t.ID, &t.SessionID, &t.Title, &description,
			&t.Stage, &agentID, &project, &branch, &worktreePath, &prUrl,
			&goalID, &confidence, &tokensUsed, &blockedReason,
			&phase, &phaseOrder, &ciStatus, &ciRunUrl,
			&t.CreatedAt, &t.UpdatedAt, &stageChangedAt); err != nil {
			return nil, err
		}
		t.Description = scanNullString(description)
		t.AgentID = scanNullString(agentID)
		t.Project = scanNullString(project)
		t.Branch = scanNullString(branch)
		t.WorktreePath = scanNullString(worktreePath)
		t.PRUrl = scanNullString(prUrl)
		t.GoalID = scanNullString(goalID)
		t.BlockedReason = scanNullString(blockedReason)
		t.Phase = scanNullString(phase)
		t.CIStatus = scanNullString(ciStatus)
		t.CIRunUrl = scanNullString(ciRunUrl)
		if confidence.Valid {
			t.Confidence = &confidence.Float64
		}
		if tokensUsed.Valid {
			t.TokensUsed = &tokensUsed.Int64
		}
		if phaseOrder.Valid {
			t.PhaseOrder = &phaseOrder.Int64
		}
		if stageChangedAt.Valid {
			t.StageChangedAt = stageChangedAt.Int64
		}
		tasks = append(tasks, t)
	}

	eventRows, err := database.QueryContext(ctx,
		`SELECT id, session_id, COALESCE(agent_id,''), COALESCE(task_id,''), type, COALESCE(payload,''), ts
		 FROM swarm_events WHERE session_id = ? ORDER BY id ASC LIMIT 500`,
		sessionID,
	)
	events := []SwarmEvent{}
	if err == nil {
		defer eventRows.Close()
		for eventRows.Next() {
			var e SwarmEvent
			eventRows.Scan(&e.ID, &e.SessionID, &e.AgentID, &e.TaskID, &e.Type, &e.Payload, &e.Ts)
			events = append(events, e)
		}
	}

	goals := listGoals(ctx, sessionID)
	escalations, _ := loadEscalations(sessionID)
	if escalations == nil {
		escalations = []SwarmEscalation{}
	}
	return &SwarmState{Session: session, Agents: agents, Tasks: tasks, Events: events, Goals: goals, Escalations: escalations}, nil
}

func broadcastSwarmState(sessionID string) {
	ctx := context.Background()
	state, err := getSwarmState(ctx, sessionID)
	if err != nil {
		log.Printf("swarm: broadcastSwarmState error for %s: %v", sessionID, err)
		return
	}
	swarmHub.broadcast(sessionID, map[string]interface{}{
		"type":  "swarm_state",
		"state": state,
	})
}

// -----------------
// WebSocket Handler
// -----------------

func handleSwarmWebSocket(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		http.Error(w, "session required", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("swarm ws upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// Send full state snapshot before subscribing to avoid a data race on the
	// initial write (the connection is not yet in the hub, so no concurrent broadcasts).
	ctx := context.Background()
	state, err := getSwarmState(ctx, sessionID)
	if err != nil {
		log.Printf("swarm ws: failed to load state for %s: %v", sessionID, err)
		return
	}
	msg := map[string]interface{}{"type": "swarm_state", "state": state}
	if data, err := json.Marshal(msg); err == nil {
		conn.WriteMessage(websocket.TextMessage, data) //nolint:errcheck
	}

	client := swarmHub.subscribe(sessionID, conn)
	defer swarmHub.unsubscribe(sessionID, client)

	// Drain incoming messages (reserved for future client→server events)
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
}

// -----------------
// REST Handlers
// -----------------

func handleSwarmAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, pathParts []string) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB limit on all swarm endpoints
	if len(pathParts) > 0 && pathParts[0] == "dashboard" {
		handleSwarmDashboardAPI(w, r, ctx)
		return
	}
	if len(pathParts) > 0 && pathParts[0] == "role-prompts" {
		handleSwarmRolePromptsAPI(w, r, ctx, pathParts[1:])
		return
	}
	if len(pathParts) > 0 && pathParts[0] == "cleanup" {
		handleSwarmCleanupAPI(w, r, ctx)
		return
	}
	if len(pathParts) > 0 && pathParts[0] == "transcribe" {
		handleSwarmTranscribeAPI(w, r)
		return
	}
	if len(pathParts) > 0 && pathParts[0] == "tts" {
		handleSwarmTTSAPI(w, r)
		return
	}
	if len(pathParts) > 0 && pathParts[0] == "voice" {
		handleSwarmVoiceAPI(w, r, ctx, pathParts[1:])
		return
	}
	if len(pathParts) > 0 && pathParts[0] == "sessions" && len(pathParts) > 1 && pathParts[1] == "bulk" {
		handleSwarmBulkAPI(w, r, ctx)
		return
	}
	if len(pathParts) == 0 || pathParts[0] != "sessions" {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown swarm endpoint"})
		return
	}
	handleSwarmSessionsAPI(w, r, ctx, pathParts[1:])
}

// handleSwarmRolePromptsAPI handles GET /api/swarm/role-prompts
// and PUT /api/swarm/role-prompts/:role
func handleSwarmRolePromptsAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, pathParts []string) {
	w.Header().Set("Content-Type", "application/json")
	if len(pathParts) == 0 {
		// GET — list all roles and their prompts + versions
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		rows, err := database.QueryContext(ctx,
			"SELECT role, prompt, version, updated_at FROM swarm_role_prompts ORDER BY role")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
			return
		}
		defer rows.Close()
		type rolePromptRow struct {
			Role      string `json:"role"`
			Prompt    string `json:"prompt"`
			Version   int    `json:"version"`
			UpdatedAt string `json:"updated_at"`
		}
		var result []rolePromptRow
		for rows.Next() {
			var rp rolePromptRow
			rows.Scan(&rp.Role, &rp.Prompt, &rp.Version, &rp.UpdatedAt) //nolint:errcheck
			result = append(result, rp)
		}
		json.NewEncoder(w).Encode(result) //nolint:errcheck
		return
	}

	// PUT /api/swarm/role-prompts/:role
	role := pathParts[0]
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Prompt == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "prompt required"}) //nolint:errcheck
		return
	}
	_, err := database.ExecContext(ctx,
		`INSERT INTO swarm_role_prompts (role, prompt, version, updated_at)
		 VALUES (?, ?, 1, CURRENT_TIMESTAMP)
		 ON CONFLICT(role) DO UPDATE SET
		   prompt = excluded.prompt,
		   version = version + 1,
		   updated_at = CURRENT_TIMESTAMP`,
		role, body.Prompt,
	)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "role": role}) //nolint:errcheck
}

func handleSwarmSessionsAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, pathParts []string) {
	if len(pathParts) == 0 {
		switch r.Method {
		case http.MethodGet:
			rows, err := database.QueryContext(ctx,
				"SELECT id, name, created_at, updated_at FROM swarm_sessions ORDER BY created_at DESC")
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			defer rows.Close()
			sessions := []SwarmSession{}
			for rows.Next() {
				var s SwarmSession
				rows.Scan(&s.ID, &s.Name, &s.CreatedAt, &s.UpdatedAt)
				sessions = append(sessions, s)
			}
			json.NewEncoder(w).Encode(sessions)

		case http.MethodPost:
			var req struct {
				Name     string `json:"name"`
				Template string `json:"template"` // optional: blank/dev/research/fullstack/devops
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "name required"})
				return
			}
			id := generateSwarmID()
			now := time.Now().Unix()
			// Wrap session + SiBot + template agents in a transaction.
			tx, err := database.BeginTx(ctx, nil)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			defer tx.Rollback() //nolint:errcheck — no-op after Commit
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO swarm_sessions (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)",
				id, req.Name, now, now); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			sibotID := generateSwarmID()
			sibotMission := "Orchestrate and coordinate the swarm to achieve the user's goals"
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO swarm_agents (id, session_id, name, role, mission, status, created_at) VALUES (?, ?, 'SiBot', 'orchestrator', ?, 'idle', ?)",
				sibotID, id, sibotMission, now); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			// Optionally seed template agents (unknown/empty template = blank).
			type tmplAgent struct{ name, role, mission string }
			sessionTemplates := map[string][]tmplAgent{
				"dev": {
					{"Dev-1", "senior-dev", "Implement features and fix bugs"},
					{"QA-1", "qa-agent", "Write and run tests, review code quality"},
				},
				"research": {
					{"Researcher", "researcher", "Gather and synthesise information"},
					{"Writer", "worker", "Write reports and summaries"},
				},
				"fullstack": {
					{"Frontend", "senior-dev", "Build and maintain the frontend"},
					{"Backend", "senior-dev", "Build and maintain the backend"},
					{"QA-1", "qa-agent", "Test both layers"},
				},
				"devops": {
					{"DevOps", "devops-agent", "CI/CD, infra, and deployments"},
					{"Dev-1", "senior-dev", "Feature development"},
				},
			}
			for _, ta := range sessionTemplates[req.Template] {
				aid := generateSwarmID()
				if _, err := tx.ExecContext(ctx,
					"INSERT INTO swarm_agents (id, session_id, name, role, mission, status, created_at) VALUES (?, ?, ?, ?, ?, 'idle', ?)",
					aid, id, ta.name, ta.role, ta.mission, now); err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}
			}
			if err := tx.Commit(); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(SwarmSession{ID: id, Name: req.Name, CreatedAt: now, UpdatedAt: now})

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
			state, err := getSwarmState(ctx, sessionID)
			if err == sql.ErrNoRows {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{"error": "session not found"})
				return
			}
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			json.NewEncoder(w).Encode(state)

		case http.MethodPatch:
			var req struct {
				Name string `json:"name"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			if req.Name == "" {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "name required"})
				return
			}
			database.ExecContext(ctx,
				"UPDATE swarm_sessions SET name = ?, updated_at = ? WHERE id = ?",
				req.Name, time.Now().Unix(), sessionID)
			swarmBroadcaster.schedule(sessionID)
			w.WriteHeader(http.StatusNoContent)

		case http.MethodDelete:
			// Despawn running agents first so tmux sessions are cleaned up
			agentRows, _ := database.QueryContext(ctx,
				"SELECT id FROM swarm_agents WHERE session_id = ? AND tmux_session IS NOT NULL", sessionID)
			if agentRows != nil {
				var agentIDs []string
				for agentRows.Next() {
					var aid string
					agentRows.Scan(&aid) //nolint:errcheck
					agentIDs = append(agentIDs, aid)
				}
				agentRows.Close()
				for _, aid := range agentIDs {
					despawnSwarmAgent(ctx, sessionID, aid) //nolint:errcheck
				}
			}
			// Delete tables without FK to swarm_sessions (application-level cascade)
			database.ExecContext(ctx, "DELETE FROM swarm_agent_notes WHERE session_id = ?", sessionID) //nolint:errcheck
			database.ExecContext(ctx, "DELETE FROM swarm_events WHERE session_id = ?", sessionID)      //nolint:errcheck
			database.ExecContext(ctx, "DELETE FROM swarm_goals WHERE session_id = ?", sessionID)       //nolint:errcheck
			// Delete session — FK ON DELETE CASCADE handles swarm_agents and swarm_tasks
			database.ExecContext(ctx, "DELETE FROM swarm_sessions WHERE id = ?", sessionID) //nolint:errcheck
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}

	switch subPath[0] {
	case "agents":
		handleSwarmAgentsAPI(w, r, ctx, sessionID, subPath[1:])
	case "tasks":
		handleSwarmTasksAPI(w, r, ctx, sessionID, subPath[1:])
	case "goals":
		// subPath is derived from strings.Split which never produces empty segments
		// for clean URLs, so subPath[1] != "" reliably identifies a single-goal route.
		switch {
		case len(subPath) >= 3 && subPath[1] != "" && subPath[2] == "budget":
			handleSwarmGoalBudgetAPI(w, r, ctx, sessionID, subPath[1])
		case len(subPath) >= 2 && subPath[1] != "":
			handleSwarmGoalAPI(w, r, ctx, sessionID, subPath[1])
		default:
			handleSwarmGoalsAPI(w, r, ctx, sessionID)
		}
	case "triage":
		handleSwarmTriageAPI(w, r, ctx, sessionID, subPath[1:])
	case "escalations":
		handleSwarmEscalationsAPI(w, r, ctx, sessionID, subPath[1:])
	case "orchestrator":
		handleSwarmOrchestratorAPI(w, r, ctx, sessionID, subPath[1:])
	case "resume":
		handleSwarmResumeAPI(w, r, ctx, sessionID)
	case "autopilot":
		handleSwarmAutopilotAPI(w, r, ctx, sessionID)
	case "plane":
		if len(subPath) > 1 && subPath[1] == "issues" {
			handleSwarmPlaneIssuesAPI(w, r, ctx, sessionID)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// handleSwarmResumeAPI spawns all configured-but-idle agents in a session.
// POST /sessions/:id/resume
func handleSwarmResumeAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, sessionID string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	rows, err := database.QueryContext(ctx,
		"SELECT id FROM swarm_agents WHERE session_id = ? AND repo_path IS NOT NULL AND tmux_session IS NULL",
		sessionID,
	)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	var agentIDs []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		agentIDs = append(agentIDs, id)
	}

	spawned := 0
	var spawnErrors []string
	for _, agentID := range agentIDs {
		if err := spawnSwarmAgent(ctx, sessionID, agentID); err != nil {
			spawnErrors = append(spawnErrors, fmt.Sprintf("%s: %v", agentID[:8], err))
		} else {
			spawned++
		}
	}

	writeSwarmEvent(ctx, sessionID, "", "", "session_resumed",
		fmt.Sprintf("Resumed %d agent(s)", spawned))
	swarmBroadcaster.schedule(sessionID)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"spawned": spawned,
		"errors":  spawnErrors,
	})
}

// handleSwarmOrchestratorAPI handles POST /sessions/:id/orchestrator/message
// It finds the live orchestrator agent and injects the message into its tmux session.
func handleSwarmOrchestratorAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, sessionID string, pathParts []string) {
	if len(pathParts) == 0 || pathParts[0] != "message" || r.Method != http.MethodPost {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "text required"})
		return
	}

	// Find the live orchestrator agent for this session
	var agentID string
	err := database.QueryRowContext(ctx,
		"SELECT id FROM swarm_agents WHERE session_id = ? AND role = 'orchestrator' AND tmux_session IS NOT NULL LIMIT 1",
		sessionID,
	).Scan(&agentID)
	if err != nil {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "no live orchestrator agent in this session — spawn one first"})
		return
	}

	if err := injectToSwarmAgent(ctx, agentID, req.Text); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	writeSwarmEvent(ctx, sessionID, agentID, "", "orchestrator_message", req.Text)
	w.WriteHeader(http.StatusNoContent)
}

// handleSwarmDashboardAPI returns aggregated stats for all sessions in one query.
// GET /api/swarm/dashboard
func handleSwarmDashboardAPI(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	rows, err := database.QueryContext(ctx, `
		SELECT
			s.id, s.name, s.created_at, s.updated_at,
			COUNT(DISTINCT a.id)                                                     AS agent_count,
			COUNT(DISTINCT CASE WHEN a.tmux_session IS NOT NULL THEN a.id END)       AS live_agents,
			COUNT(DISTINCT CASE WHEN a.status='stuck'   THEN a.id END)               AS stuck_agents,
			COUNT(DISTINCT CASE WHEN a.status='waiting' THEN a.id END)               AS waiting_agents,
			COALESCE(SUM(CASE WHEN t.stage='spec'       THEN 1 ELSE 0 END), 0)       AS spec_count,
			COALESCE(SUM(CASE WHEN t.stage='implement'  THEN 1 ELSE 0 END), 0)       AS implement_count,
			COALESCE(SUM(CASE WHEN t.stage='test'       THEN 1 ELSE 0 END), 0)       AS test_count,
			COALESCE(SUM(CASE WHEN t.stage='deploy'     THEN 1 ELSE 0 END), 0)       AS deploy_count,
			COALESCE(SUM(CASE WHEN t.stage='done'       THEN 1 ELSE 0 END), 0)       AS done_count,
			COALESCE(MAX(e.ts), 0)                                                   AS last_event_ts
		FROM swarm_sessions s
		LEFT JOIN swarm_agents a ON a.session_id = s.id
		LEFT JOIN swarm_tasks  t ON t.session_id = s.id
		LEFT JOIN (
			SELECT session_id, MAX(ts) AS ts FROM swarm_events GROUP BY session_id
		) e ON e.session_id = s.id
		GROUP BY s.id
		ORDER BY s.updated_at DESC
	`)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	type SessionStats struct {
		ID             string         `json:"id"`
		Name           string         `json:"name"`
		CreatedAt      int64          `json:"created_at"`
		UpdatedAt      int64          `json:"updated_at"`
		AgentCount     int            `json:"agent_count"`
		LiveAgents     int            `json:"live_agents"`
		StuckAgents    int            `json:"stuck_agents"`
		WaitingAgents  int            `json:"waiting_agents"`
		TasksByStage   map[string]int `json:"tasks_by_stage"`
		LastEventTs    int64          `json:"last_event_ts"`
	}

	var sessions []SessionStats
	for rows.Next() {
		var s SessionStats
		var spec, implement, test, deploy, done int
		if err := rows.Scan(
			&s.ID, &s.Name, &s.CreatedAt, &s.UpdatedAt,
			&s.AgentCount, &s.LiveAgents, &s.StuckAgents, &s.WaitingAgents,
			&spec, &implement, &test, &deploy, &done, &s.LastEventTs,
		); err != nil {
			continue
		}
		s.TasksByStage = map[string]int{
			"spec": spec, "implement": implement, "test": test, "deploy": deploy, "done": done,
		}
		sessions = append(sessions, s)
	}
	if sessions == nil {
		sessions = []SessionStats{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"sessions": sessions})
}

// handleSwarmBulkAPI returns full state for multiple sessions in one call.
// GET /api/swarm/sessions/bulk?ids=id1,id2,...
// Reduces TUI fetchAll from 1+N HTTP round-trips to 2 (dashboard + bulk).
const maxBulkSessionIDs = 100

func handleSwarmBulkAPI(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rawIDs := r.URL.Query().Get("ids")
	if rawIDs == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{}) //nolint:errcheck
		return
	}
	ids := strings.Split(rawIDs, ",")
	if len(ids) > maxBulkSessionIDs {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("too many ids (max %d)", maxBulkSessionIDs)}) //nolint:errcheck
		return
	}
	result := make(map[string]*SwarmState, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		if state, err := getSwarmState(ctx, id); err == nil {
			result[id] = state
		}
	}
	json.NewEncoder(w).Encode(result) //nolint:errcheck
}

// swarmAPIBase returns the base URL agents use to reach this server.
// Respects SWARM_API_BASE_URL env var so remote/containerised agents work.
// Falls back to http://localhost:{PORT} (default 8080).
func swarmAPIBase() string {
	if base := os.Getenv("SWARM_API_BASE_URL"); base != "" {
		return base
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	return "http://localhost:" + port
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// handleSwarmPlaneIssuesAPI returns work queue items for the session's Plane project.
// GET /api/swarm/sessions/:id/plane/issues?state_group=backlog,unstarted
func handleSwarmPlaneIssuesAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, sessionID string) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Resolve plane project ID: per-session config takes priority, then PLANE_PROJECT_ID env var
	var projectID string
	database.QueryRowContext(ctx,
		"SELECT COALESCE(autopilot_plane_project_id,'') FROM swarm_sessions WHERE id=?", sessionID,
	).Scan(&projectID)
	if projectID == "" {
		projectID = os.Getenv("PLANE_PROJECT_ID")
	}
	if projectID == "" {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "no plane_project_id configured (set PLANE_PROJECT_ID env var or enable autopilot with a project)"})
		return
	}

	// Parse requested state groups (default: backlog,unstarted)
	groupParam := r.URL.Query().Get("state_group")
	if groupParam == "" {
		groupParam = "backlog,unstarted"
	}
	var groups []string
	for _, g := range strings.Split(groupParam, ",") {
		if t := strings.TrimSpace(g); t != "" {
			groups = append(groups, t)
		}
	}

	// Serve from background cache when available (populated by startPlaneAdapter).
	// Fall back to a live fetch only if the cache hasn't been primed yet.
	key := cacheKey(projectID, groups)
	items, cached := globalPlaneCache.get(key)
	if !cached {
		var err error
		items, err = planeFetchWorkQueueItems(ctx, projectID, groups)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		if items != nil {
			globalPlaneCache.set(key, items)
		}
	}
	if items == nil {
		items = []WorkQueueItem{}
	}
	json.NewEncoder(w).Encode(items)
}


func handleSwarmAgentsAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, sessionID string, pathParts []string) {
	if len(pathParts) == 0 {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Name     string `json:"name"`
			Role     string `json:"role"`
			Project  string `json:"project"`
			RepoPath string `json:"repo_path"`
			Mission  string `json:"mission"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "name required"})
			return
		}
		if req.Role == "" {
			req.Role = "worker"
		}
		id := generateSwarmID()
		now := time.Now().Unix()
		if _, err := database.ExecContext(ctx,
			"INSERT INTO swarm_agents (id, session_id, name, role, project, repo_path, mission, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'idle', ?)",
			id, sessionID, req.Name, req.Role, swarmNullStr(req.Project), swarmNullStr(req.RepoPath), swarmNullStr(req.Mission), now); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		agent := SwarmAgent{ID: id, SessionID: sessionID, Name: req.Name, Role: req.Role, Status: "idle", CreatedAt: now}
		if req.Project != "" {
			agent.Project = &req.Project
		}
		if req.RepoPath != "" {
			agent.RepoPath = &req.RepoPath
		}
		if req.Mission != "" {
			agent.Mission = &req.Mission
		}
		swarmBroadcaster.schedule(sessionID)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(agent)
		return
	}

	agentID := pathParts[0]
	subPath := pathParts[1:]

	// Sub-actions: spawn / despawn / inject / terminal / git
	if len(subPath) > 0 {
		if subPath[0] == "terminal" {
			handleSwarmTerminalAPI(w, r, ctx, sessionID, agentID)
			return
		}
		if subPath[0] == "git" {
			handleSwarmAgentGitAPI(w, r, ctx, sessionID, agentID)
			return
		}
		// GET notes is allowed without the POST-only guard below.
		if subPath[0] == "note" && r.Method == http.MethodGet {
			type agentNoteResp struct {
				ID        int64  `json:"id"`
				Content   string `json:"content"`
				CreatedBy string `json:"created_by"`
				CreatedAt int64  `json:"created_at"`
			}
			noteRows, err := database.QueryContext(ctx,
				`SELECT id, content, created_by, created_at
				 FROM swarm_agent_notes
				 WHERE agent_id = ? AND session_id = ?
				 ORDER BY created_at DESC LIMIT 50`,
				agentID, sessionID)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			defer noteRows.Close()
			notes := []agentNoteResp{}
			for noteRows.Next() {
				var n agentNoteResp
				noteRows.Scan(&n.ID, &n.Content, &n.CreatedBy, &n.CreatedAt) //nolint:errcheck
				notes = append(notes, n)
			}
			json.NewEncoder(w).Encode(notes)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch subPath[0] {
		case "note":
			var req struct {
				Content   string `json:"content"`
				CreatedBy string `json:"created_by"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Content == "" {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "content required"})
				return
			}
			if len(req.Content) > 2000 {
				req.Content = req.Content[:2000]
			}
			if req.CreatedBy == "" {
				req.CreatedBy = "user"
			}
			if _, err := database.ExecContext(ctx,
				"INSERT INTO swarm_agent_notes (agent_id, session_id, content, created_by, created_at) VALUES (?, ?, ?, ?, ?)",
				agentID, sessionID, req.Content, req.CreatedBy, time.Now().Unix(),
			); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			swarmBroadcaster.schedule(sessionID)
			w.WriteHeader(http.StatusNoContent)
		case "spawn":
			if err := spawnSwarmAgent(ctx, sessionID, agentID); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			swarmBroadcaster.schedule(sessionID)
			w.WriteHeader(http.StatusNoContent)
		case "despawn":
			if err := despawnSwarmAgent(ctx, sessionID, agentID); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			swarmBroadcaster.schedule(sessionID)
			w.WriteHeader(http.StatusNoContent)
		case "inject":
			var req struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "text required"})
				return
			}
			if err := injectToSwarmAgent(ctx, agentID, req.Text); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			writeSwarmEvent(ctx, sessionID, agentID, "", "inject_brief", req.Text)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
		return
	}

	switch r.Method {
	case http.MethodPatch:
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON"})
			return
		}
		if status, ok := req["status"].(string); ok {
			setAgentStatus(ctx, agentID, status)
		}
		if f, ok := req["current_file"].(string); ok {
			database.ExecContext(ctx,
				"UPDATE swarm_agents SET current_file = ? WHERE id = ? AND session_id = ?",
				f, agentID, sessionID)
		}
		if taskID, ok := req["current_task_id"].(string); ok {
			database.ExecContext(ctx,
				"UPDATE swarm_agents SET current_task_id = ? WHERE id = ? AND session_id = ?",
				taskID, agentID, sessionID)
		}
		if name, ok := req["name"].(string); ok && name != "" {
			database.ExecContext(ctx,
				"UPDATE swarm_agents SET name = ? WHERE id = ? AND session_id = ?",
				name, agentID, sessionID)
		}
		if mission, ok := req["mission"]; ok {
			if mission == nil {
				database.ExecContext(ctx, "UPDATE swarm_agents SET mission = NULL WHERE id = ? AND session_id = ?", agentID, sessionID)
			} else if s, ok := mission.(string); ok {
				database.ExecContext(ctx, "UPDATE swarm_agents SET mission = ? WHERE id = ? AND session_id = ?", swarmNullStr(s), agentID, sessionID)
			}
		}
		if project, ok := req["project"]; ok {
			if project == nil {
				database.ExecContext(ctx, "UPDATE swarm_agents SET project = NULL WHERE id = ? AND session_id = ?", agentID, sessionID)
			} else if s, ok := project.(string); ok {
				database.ExecContext(ctx, "UPDATE swarm_agents SET project = ? WHERE id = ? AND session_id = ?", swarmNullStr(s), agentID, sessionID)
			}
		}
		if repoPath, ok := req["repo_path"]; ok {
			if repoPath == nil {
				database.ExecContext(ctx, "UPDATE swarm_agents SET repo_path = NULL WHERE id = ? AND session_id = ?", agentID, sessionID)
			} else if s, ok := repoPath.(string); ok {
				database.ExecContext(ctx, "UPDATE swarm_agents SET repo_path = ? WHERE id = ? AND session_id = ?", swarmNullStr(s), agentID, sessionID)
			}
		}
		if modelName, ok := req["model_name"]; ok {
			if modelName == nil {
				database.ExecContext(ctx, "UPDATE swarm_agents SET model_name = NULL WHERE id = ? AND session_id = ?", agentID, sessionID)
			} else if s, ok := modelName.(string); ok {
				if s != "" && !isValidModelName(s) {
					w.WriteHeader(http.StatusBadRequest)
					json.NewEncoder(w).Encode(map[string]string{"error": "invalid model_name: only alphanumeric, hyphens, dots, colons, underscores allowed"})
					return
				}
				database.ExecContext(ctx, "UPDATE swarm_agents SET model_name = ? WHERE id = ? AND session_id = ?", swarmNullStr(s), agentID, sessionID)
			}
		}
		swarmBroadcaster.schedule(sessionID)
		w.WriteHeader(http.StatusNoContent)

	case http.MethodDelete:
		database.ExecContext(ctx, "DELETE FROM swarm_agents WHERE id = ? AND session_id = ?", agentID, sessionID)
		swarmBroadcaster.schedule(sessionID)
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func handleSwarmTasksAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, sessionID string, pathParts []string) {
	if len(pathParts) == 0 {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Stage       string `json:"stage"`
			Project     string `json:"project"`
			GoalID      string `json:"goal_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Title == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "title required"})
			return
		}
		if req.Stage == "" {
			req.Stage = "spec"
		}
		if req.GoalID != "" {
			if err := checkGoalTaskLimit(ctx, req.GoalID); err != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
		}
		id := generateSwarmID()
		now := time.Now().Unix()
		if _, err := database.ExecContext(ctx,
			"INSERT INTO swarm_tasks (id, session_id, title, description, stage, project, goal_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			id, sessionID, req.Title, swarmNullStr(req.Description), req.Stage, swarmNullStr(req.Project), swarmNullStr(req.GoalID), now, now); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		task := SwarmTask{ID: id, SessionID: sessionID, Title: req.Title, Stage: req.Stage, CreatedAt: now, UpdatedAt: now}
		if req.Description != "" {
			task.Description = &req.Description
		}
		if req.Project != "" {
			task.Project = &req.Project
		}
		if req.GoalID != "" {
			task.GoalID = &req.GoalID
		}
		writeSwarmEvent(ctx, sessionID, "", id, "task_created", req.Title)
		swarmBroadcaster.schedule(sessionID)
		// Auto-dispatch to an idle worker if the task starts queued.
		if req.Stage == "queued" || req.Stage == "" {
			go autoDispatchQueuedTasks(context.Background(), sessionID)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(task)
		return
	}

	taskID := pathParts[0]

	// Sub-action: create-pr
	if len(pathParts) > 1 && pathParts[1] == "create-pr" {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		prUrl, err := createPRForTask(ctx, sessionID, taskID)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		swarmBroadcaster.schedule(sessionID)
		json.NewEncoder(w).Encode(map[string]string{"pr_url": prUrl})
		return
	}

	switch r.Method {
	case http.MethodPatch:
		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)
		now := time.Now().Unix()
		if stage, ok := req["stage"].(string); ok {
			database.ExecContext(ctx,
				"UPDATE swarm_tasks SET stage = ?, updated_at = ?, stage_changed_at = ? WHERE id = ? AND session_id = ?",
				stage, now, now, taskID, sessionID)
			writeSwarmEvent(ctx, sessionID, "", taskID, "task_moved", stage)
			// Auto-create PR when task moves to deploy
			if stage == "deploy" {
				go tryCreatePR(context.Background(), sessionID, taskID)
			}
			// Auto-dispatch if moved back to queued (e.g. blocked recovery)
			if stage == "queued" {
				go autoDispatchQueuedTasks(context.Background(), sessionID)
			}
			// Local + Telegram notification when task completes
			if stage == "done" {
				var taskTitle, sessionName string
				database.QueryRowContext(ctx, "SELECT title FROM swarm_tasks WHERE id = ?", taskID).Scan(&taskTitle)           //nolint:errcheck
				database.QueryRowContext(ctx, "SELECT name FROM swarm_sessions WHERE id = ?", sessionID).Scan(&sessionName) //nolint:errcheck
				notifyTaskDone(sessionName, taskTitle, taskID)
			}
		}
		if agentID, ok := req["agent_id"].(string); ok {
			database.ExecContext(ctx,
				"UPDATE swarm_tasks SET agent_id = ?, updated_at = ? WHERE id = ? AND session_id = ?",
				agentID, now, taskID, sessionID)
		}
		if agentID, ok := req["agent_id"]; ok && agentID == nil {
			database.ExecContext(ctx,
				"UPDATE swarm_tasks SET agent_id = NULL, updated_at = ? WHERE id = ? AND session_id = ?",
				now, taskID, sessionID)
		}
		if title, ok := req["title"].(string); ok && title != "" {
			database.ExecContext(ctx,
				"UPDATE swarm_tasks SET title = ?, updated_at = ? WHERE id = ? AND session_id = ?",
				title, now, taskID, sessionID)
		}
		if desc, ok := req["description"]; ok {
			if desc == nil {
				database.ExecContext(ctx, "UPDATE swarm_tasks SET description = NULL, updated_at = ? WHERE id = ? AND session_id = ?", now, taskID, sessionID)
			} else if s, ok := desc.(string); ok {
				database.ExecContext(ctx, "UPDATE swarm_tasks SET description = ?, updated_at = ? WHERE id = ? AND session_id = ?", swarmNullStr(s), now, taskID, sessionID)
			}
		}
		if proj, ok := req["project"]; ok {
			if proj == nil {
				database.ExecContext(ctx, "UPDATE swarm_tasks SET project = NULL, updated_at = ? WHERE id = ? AND session_id = ?", now, taskID, sessionID)
			} else if s, ok := proj.(string); ok {
				database.ExecContext(ctx, "UPDATE swarm_tasks SET project = ?, updated_at = ? WHERE id = ? AND session_id = ?", swarmNullStr(s), now, taskID, sessionID)
			}
		}
		swarmBroadcaster.schedule(sessionID)
		w.WriteHeader(http.StatusNoContent)

	case http.MethodDelete:
		database.ExecContext(ctx, "DELETE FROM swarm_tasks WHERE id = ? AND session_id = ?", taskID, sessionID)
		swarmBroadcaster.schedule(sessionID)
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
