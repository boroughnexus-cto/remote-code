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
	CreatedAt     int64   `json:"created_at"`
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

type SwarmHub struct {
	mu      sync.RWMutex
	clients map[string]map[*websocket.Conn]bool
}

var swarmHub = &SwarmHub{
	clients: make(map[string]map[*websocket.Conn]bool),
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

func (h *SwarmHub) subscribe(sessionID string, conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[sessionID] == nil {
		h.clients[sessionID] = make(map[*websocket.Conn]bool)
	}
	h.clients[sessionID][conn] = true
}

func (h *SwarmHub) unsubscribe(sessionID string, conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[sessionID] != nil {
		delete(h.clients[sessionID], conn)
	}
}

func (h *SwarmHub) broadcast(sessionID string, msg interface{}) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("swarm hub: marshal error: %v", err)
		return
	}
	h.mu.RLock()
	conns := make([]*websocket.Conn, 0, len(h.clients[sessionID]))
	for conn := range h.clients[sessionID] {
		conns = append(conns, conn)
	}
	h.mu.RUnlock()
	for _, conn := range conns {
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			log.Printf("swarm hub: write error: %v", err)
			h.unsubscribe(sessionID, conn)
		}
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
	database.ExecContext(ctx,
		"INSERT INTO swarm_events (session_id, agent_id, task_id, type, payload, ts) VALUES (?, ?, ?, ?, ?, ?)",
		sessionID, swarmNullStr(agentID), swarmNullStr(taskID), eventType, swarmNullStr(payload), time.Now().Unix(),
	)
}

func swarmNullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
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
		        (SELECT content FROM swarm_agent_notes WHERE agent_id = a.id ORDER BY created_at DESC LIMIT 1)
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
		if err := agentRows.Scan(&a.ID, &a.SessionID, &a.Name, &a.Role,
			&worktreePath, &tmuxSession, &project,
			&repoPath, &a.Status,
			&currentFile, &currentTaskID, &mission,
			&a.ContextPct, &a.ContextState, &a.CreatedAt, &latestNote); err != nil {
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
		agents = append(agents, a)
	}

	taskRows, err := database.QueryContext(ctx,
		`SELECT id, session_id, title, description, stage, agent_id, project,
		        branch, worktree_path, pr_url, goal_id, confidence, tokens_used, blocked_reason,
		        phase, phase_order, ci_status, ci_run_url,
		        created_at, updated_at
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
		var tokensUsed, phaseOrder sql.NullInt64
		if err := taskRows.Scan(&t.ID, &t.SessionID, &t.Title, &description,
			&t.Stage, &agentID, &project, &branch, &worktreePath, &prUrl,
			&goalID, &confidence, &tokensUsed, &blockedReason,
			&phase, &phaseOrder, &ciStatus, &ciRunUrl,
			&t.CreatedAt, &t.UpdatedAt); err != nil {
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
		tasks = append(tasks, t)
	}

	eventRows, err := database.QueryContext(ctx,
		`SELECT id, session_id, COALESCE(agent_id,''), COALESCE(task_id,''), type, COALESCE(payload,''), ts
		 FROM swarm_events WHERE session_id = ? ORDER BY ts DESC LIMIT 100`,
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

	swarmHub.subscribe(sessionID, conn)
	defer swarmHub.unsubscribe(sessionID, conn)

	// Send full state snapshot on connect
	ctx := context.Background()
	state, err := getSwarmState(ctx, sessionID)
	if err != nil {
		log.Printf("swarm ws: failed to load state for %s: %v", sessionID, err)
		return
	}
	msg := map[string]interface{}{"type": "swarm_state", "state": state}
	if data, err := json.Marshal(msg); err == nil {
		conn.WriteMessage(websocket.TextMessage, data)
	}

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
	if len(pathParts) > 0 && pathParts[0] == "dashboard" {
		handleSwarmDashboardAPI(w, r, ctx)
		return
	}
	if len(pathParts) == 0 || pathParts[0] != "sessions" {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown swarm endpoint"})
		return
	}
	handleSwarmSessionsAPI(w, r, ctx, pathParts[1:])
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
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "name required"})
				return
			}
			id := generateSwarmID()
			now := time.Now().Unix()
			if _, err := database.ExecContext(ctx,
				"INSERT INTO swarm_sessions (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)",
				id, req.Name, now, now); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			// Auto-create SiBot orchestrator for every session
			sibotID := generateSwarmID()
			sibotMission := "Orchestrate and coordinate the swarm to achieve the user's goals"
			database.ExecContext(ctx,
				"INSERT INTO swarm_agents (id, session_id, name, role, mission, status, created_at) VALUES (?, ?, 'SiBot', 'orchestrator', ?, 'idle', ?)",
				sibotID, id, sibotMission, now)
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

		case http.MethodDelete:
			database.ExecContext(ctx, "DELETE FROM swarm_sessions WHERE id = ?", sessionID)
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
		// Route: /goals, /goals/:gid/budget
		if len(subPath) >= 3 && subPath[2] == "budget" {
			handleSwarmGoalBudgetAPI(w, r, ctx, sessionID, subPath[1])
		} else {
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
	writeSwarmEvent(ctx, sessionID, agentID, "", "orchestrator_message", truncate(req.Text, 120))
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
		LEFT JOIN swarm_events e ON e.session_id = s.id
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

	// Resolve plane project ID for this session
	var projectID string
	database.QueryRowContext(ctx,
		"SELECT COALESCE(autopilot_plane_project_id,'') FROM swarm_sessions WHERE id=?", sessionID,
	).Scan(&projectID)

	if projectID == "" {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "no plane_project_id configured for this session"})
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

	items, err := planeFetchWorkQueueItems(ctx, projectID, groups)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
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

	// Sub-actions: spawn / despawn / inject / terminal
	if len(subPath) > 0 {
		if subPath[0] == "terminal" {
			handleSwarmTerminalAPI(w, r, ctx, sessionID, agentID)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch subPath[0] {
		case "note":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
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
			writeSwarmEvent(ctx, sessionID, agentID, "", "inject_brief", truncate(req.Text, 120))
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
		return
	}

	switch r.Method {
	case http.MethodPatch:
		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)
		now := time.Now().Unix()
		if status, ok := req["status"].(string); ok {
			database.ExecContext(ctx,
				"UPDATE swarm_agents SET status = ? WHERE id = ? AND session_id = ?",
				status, agentID, sessionID)
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
		_ = now
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
				"UPDATE swarm_tasks SET stage = ?, updated_at = ? WHERE id = ? AND session_id = ?",
				stage, now, taskID, sessionID)
			writeSwarmEvent(ctx, sessionID, "", taskID, "task_moved", stage)
			// Auto-create PR when task moves to deploy
			if stage == "deploy" {
				go tryCreatePR(context.Background(), sessionID, taskID)
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
		if title, ok := req["title"].(string); ok {
			database.ExecContext(ctx,
				"UPDATE swarm_tasks SET title = ?, updated_at = ? WHERE id = ? AND session_id = ?",
				title, now, taskID, sessionID)
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
