package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

func setupSwarmDB(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	tables := []string{
		"swarm_goals", "swarm_artifacts", "swarm_decisions",
		"swarm_agent_notes", "swarm_tasks", "swarm_agents", "swarm_sessions",
	}
	for _, tbl := range tables {
		database.ExecContext(ctx, "DELETE FROM "+tbl)
	}
}

// swarmReq fires a request through handleAPI and returns the recorder.
func swarmReq(t *testing.T, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var rb io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rb = bytes.NewBuffer(b)
	}
	req := httptest.NewRequest(method, path, rb)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	handleAPI(w, req)
	return w
}

func mustDecodeJSON(t *testing.T, w *httptest.ResponseRecorder, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decode JSON (status=%d body=%s): %v", w.Code, w.Body.String(), err)
	}
}

// createSwarmSession creates a session via the API and returns its ID.
func createSwarmSession(t *testing.T, name string) string {
	t.Helper()
	w := swarmReq(t, "POST", "/api/swarm/sessions", map[string]string{"name": name})
	if w.Code != http.StatusCreated {
		t.Fatalf("create session: expected 201, got %d — %s", w.Code, w.Body.String())
	}
	var s SwarmSession
	mustDecodeJSON(t, w, &s)
	if s.ID == "" {
		t.Fatal("create session: empty ID returned")
	}
	return s.ID
}

// createSwarmAgent creates an agent via the API and returns its ID.
func createSwarmAgent(t *testing.T, sessionID, name string) string {
	t.Helper()
	w := swarmReq(t, "POST", "/api/swarm/sessions/"+sessionID+"/agents",
		map[string]string{"name": name, "role": "worker"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create agent: expected 201, got %d — %s", w.Code, w.Body.String())
	}
	var a SwarmAgent
	mustDecodeJSON(t, w, &a)
	return a.ID
}

// createSwarmTask creates a task via the API and returns its ID.
func createSwarmTask(t *testing.T, sessionID, title string) string {
	t.Helper()
	w := swarmReq(t, "POST", "/api/swarm/sessions/"+sessionID+"/tasks",
		map[string]string{"title": title, "stage": "queued"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create task: expected 201, got %d — %s", w.Code, w.Body.String())
	}
	var task SwarmTask
	mustDecodeJSON(t, w, &task)
	return task.ID
}

// createSwarmGoal creates a goal via the API and returns its ID.
func createSwarmGoal(t *testing.T, sessionID, description string) string {
	t.Helper()
	w := swarmReq(t, "POST", "/api/swarm/sessions/"+sessionID+"/goals",
		map[string]string{"description": description})
	if w.Code != http.StatusCreated {
		t.Fatalf("create goal: expected 201, got %d — %s", w.Code, w.Body.String())
	}
	var g SwarmGoal
	mustDecodeJSON(t, w, &g)
	return g.ID
}

// ─── Session Tests ────────────────────────────────────────────────────────────

func TestSwarmSession_Create(t *testing.T) {
	setupSwarmDB(t)

	w := swarmReq(t, "POST", "/api/swarm/sessions", map[string]string{"name": "test-session"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var s SwarmSession
	mustDecodeJSON(t, w, &s)

	if s.ID == "" {
		t.Error("expected non-empty ID")
	}
	if s.Name != "test-session" {
		t.Errorf("expected name 'test-session', got %q", s.Name)
	}
	if s.CreatedAt == 0 {
		t.Error("expected non-zero created_at")
	}
}

func TestSwarmSession_Create_AutoSpawnsSiBot(t *testing.T) {
	setupSwarmDB(t)

	sessionID := createSwarmSession(t, "sibot-test")

	// Verify SiBot orchestrator was auto-created
	var count int
	database.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM swarm_agents WHERE session_id=? AND role='orchestrator' AND name='SiBot'",
		sessionID,
	).Scan(&count)

	if count != 1 {
		t.Errorf("expected 1 SiBot orchestrator, got %d", count)
	}
}

func TestSwarmSession_Create_NameRequired(t *testing.T) {
	w := swarmReq(t, "POST", "/api/swarm/sessions", map[string]string{"name": ""})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestSwarmSession_List(t *testing.T) {
	setupSwarmDB(t)

	createSwarmSession(t, "session-a")
	createSwarmSession(t, "session-b")

	w := swarmReq(t, "GET", "/api/swarm/sessions", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var sessions []SwarmSession
	mustDecodeJSON(t, w, &sessions)

	if len(sessions) < 2 {
		t.Errorf("expected at least 2 sessions, got %d", len(sessions))
	}
}

func TestSwarmSession_Get_State(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "state-test")

	w := swarmReq(t, "GET", "/api/swarm/sessions/"+sessionID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var state SwarmState
	mustDecodeJSON(t, w, &state)

	if state.Session.ID != sessionID {
		t.Errorf("expected session ID %s, got %s", sessionID, state.Session.ID)
	}
	if state.Agents == nil {
		t.Error("agents should be non-nil array")
	}
	if state.Tasks == nil {
		t.Error("tasks should be non-nil array")
	}
	if state.Goals == nil {
		t.Error("goals should be non-nil array")
	}
	if state.Escalations == nil {
		t.Error("escalations should be non-nil array")
	}
}

func TestSwarmSession_Get_NotFound(t *testing.T) {
	w := swarmReq(t, "GET", "/api/swarm/sessions/nonexistent-session-id", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestSwarmSession_Delete(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "to-delete")

	w := swarmReq(t, "DELETE", "/api/swarm/sessions/"+sessionID, nil)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}

	// Confirm gone
	w2 := swarmReq(t, "GET", "/api/swarm/sessions/"+sessionID, nil)
	if w2.Code != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", w2.Code)
	}
}

// ─── Agent Tests ──────────────────────────────────────────────────────────────

func TestSwarmAgent_Create(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "agent-test")

	w := swarmReq(t, "POST", "/api/swarm/sessions/"+sessionID+"/agents",
		map[string]string{"name": "alice", "role": "worker", "mission": "Write tests"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var a SwarmAgent
	mustDecodeJSON(t, w, &a)

	if a.ID == "" {
		t.Error("expected non-empty agent ID")
	}
	if a.Name != "alice" {
		t.Errorf("expected name 'alice', got %q", a.Name)
	}
	if a.Role != "worker" {
		t.Errorf("expected role 'worker', got %q", a.Role)
	}
	if a.Status != "idle" {
		t.Errorf("expected status 'idle', got %q", a.Status)
	}
}

func TestSwarmAgent_Create_DefaultsRoleToWorker(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "role-default-test")

	w := swarmReq(t, "POST", "/api/swarm/sessions/"+sessionID+"/agents",
		map[string]string{"name": "bob"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}

	var a SwarmAgent
	mustDecodeJSON(t, w, &a)
	if a.Role != "worker" {
		t.Errorf("expected default role 'worker', got %q", a.Role)
	}
}

func TestSwarmAgent_Terminal_NotFound(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "terminal-test")

	w := swarmReq(t, "GET", "/api/swarm/sessions/"+sessionID+"/agents/nonexistent/terminal", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing agent, got %d", w.Code)
	}
}

func TestSwarmAgent_Terminal_NoTmux(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "terminal-notmux-test")
	agentID := createSwarmAgent(t, sessionID, "idle-agent")

	w := swarmReq(t, "GET", "/api/swarm/sessions/"+sessionID+"/agents/"+agentID+"/terminal", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	mustDecodeJSON(t, w, &resp)
	if resp["content"] == "" {
		t.Error("expected non-empty content message for idle agent")
	}
}

// ─── Task Tests ───────────────────────────────────────────────────────────────

func TestSwarmTask_Create(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "task-test")

	w := swarmReq(t, "POST", "/api/swarm/sessions/"+sessionID+"/tasks",
		map[string]string{"title": "Fix the bug", "stage": "queued"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var task SwarmTask
	mustDecodeJSON(t, w, &task)

	if task.ID == "" {
		t.Error("expected non-empty task ID")
	}
	if task.Title != "Fix the bug" {
		t.Errorf("expected title 'Fix the bug', got %q", task.Title)
	}
	if task.Stage != "queued" {
		t.Errorf("expected stage 'queued', got %q", task.Stage)
	}
}

func TestSwarmTask_Create_TitleRequired(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "task-validation-test")

	w := swarmReq(t, "POST", "/api/swarm/sessions/"+sessionID+"/tasks",
		map[string]string{"title": "", "stage": "queued"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// ─── Task State Machine Tests ─────────────────────────────────────────────────

func TestTaskStateMachine_ValidTransitions(t *testing.T) {
	cases := []struct {
		from, to string
	}{
		{"queued", "assigned"},
		{"assigned", "accepted"},
		{"assigned", "queued"},
		{"accepted", "running"},
		{"accepted", "blocked"},
		{"accepted", "failed"},
		{"running", "complete"},
		{"running", "blocked"},
		{"running", "needs_review"},
		{"running", "needs_human"},
		{"running", "failed"},
		{"running", "timed_out"},
		{"blocked", "queued"},
		{"blocked", "running"},
		{"blocked", "failed"},
		{"needs_review", "running"},
		{"needs_review", "complete"},
		{"needs_review", "failed"},
		{"needs_human", "running"},
		{"needs_human", "complete"},
		{"needs_human", "failed"},
	}

	for _, c := range cases {
		t.Run(c.from+"->"+c.to, func(t *testing.T) {
			if !isValidTransition(c.from, c.to) {
				t.Errorf("expected %s→%s to be valid", c.from, c.to)
			}
		})
	}
}

func TestTaskStateMachine_InvalidTransitions(t *testing.T) {
	cases := []struct {
		from, to string
	}{
		{"queued", "running"},     // must go through assigned→accepted first
		{"queued", "complete"},
		{"complete", "running"},   // terminal state
		{"complete", "queued"},
		{"failed", "running"},     // terminal state
		{"timed_out", "running"},  // terminal state
		{"running", "queued"},     // can't go back to queued directly
		{"", "queued"},            // unknown state
	}

	for _, c := range cases {
		t.Run(c.from+"->"+c.to, func(t *testing.T) {
			if isValidTransition(c.from, c.to) {
				t.Errorf("expected %s→%s to be invalid", c.from, c.to)
			}
		})
	}
}

func TestTaskStateMachine_Idempotent(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "idempotent-test")
	taskID := createSwarmTask(t, sessionID, "idempotent task")

	ctx := context.Background()

	// Transition to queued (already there) — should be no-op, no error
	if err := transitionTask(ctx, taskID, "queued"); err != nil {
		t.Errorf("idempotent transition queued→queued should not error: %v", err)
	}
}

func TestTaskStateMachine_DB(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "db-state-test")
	taskID := createSwarmTask(t, sessionID, "db transition test")

	ctx := context.Background()

	// queued → assigned
	if err := transitionTask(ctx, taskID, "assigned"); err != nil {
		t.Fatalf("queued→assigned: %v", err)
	}

	// assigned → accepted
	if err := transitionTask(ctx, taskID, "accepted"); err != nil {
		t.Fatalf("assigned→accepted: %v", err)
	}

	// accepted → running
	if err := transitionTask(ctx, taskID, "running"); err != nil {
		t.Fatalf("accepted→running: %v", err)
	}

	// running → complete
	if err := transitionTask(ctx, taskID, "complete"); err != nil {
		t.Fatalf("running→complete: %v", err)
	}

	// complete → anything should fail
	if err := transitionTask(ctx, taskID, "running"); err == nil {
		t.Error("expected error transitioning from terminal state 'complete'")
	}

	// Verify final state
	var stage string
	database.QueryRowContext(ctx, "SELECT stage FROM swarm_tasks WHERE id=?", taskID).Scan(&stage)
	if stage != "complete" {
		t.Errorf("expected stage 'complete', got %q", stage)
	}
}

// ─── Goal Tests ───────────────────────────────────────────────────────────────

func TestGoal_Create(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "goal-create-test")

	w := swarmReq(t, "POST", "/api/swarm/sessions/"+sessionID+"/goals",
		map[string]string{"description": "Build the feature"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var g SwarmGoal
	mustDecodeJSON(t, w, &g)

	if g.ID == "" {
		t.Error("expected non-empty goal ID")
	}
	if g.Description != "Build the feature" {
		t.Errorf("expected description 'Build the feature', got %q", g.Description)
	}
	if g.Status != "active" {
		t.Errorf("expected status 'active', got %q", g.Status)
	}
}

func TestGoal_Create_DescriptionRequired(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "goal-validation-test")

	w := swarmReq(t, "POST", "/api/swarm/sessions/"+sessionID+"/goals",
		map[string]string{"description": ""})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestGoal_List(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "goal-list-test")

	createSwarmGoal(t, sessionID, "Goal one")
	createSwarmGoal(t, sessionID, "Goal two")

	w := swarmReq(t, "GET", "/api/swarm/sessions/"+sessionID+"/goals", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var goals []SwarmGoal
	mustDecodeJSON(t, w, &goals)

	// Verify both created goals appear (session-scoped, so no leakage from other sessions).
	found := map[string]bool{}
	for _, g := range goals {
		found[g.Description] = true
	}
	if !found["Goal one"] || !found["Goal two"] {
		t.Errorf("expected to find both goals, got: %+v", goals)
	}
}

func TestGoal_TaskLimit_Enforced(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "task-limit-test")
	goalID := createSwarmGoal(t, sessionID, "limited goal")

	ctx := context.Background()
	now := time.Now().Unix()

	// Fill to the limit (maxTasksPerGoal = 8)
	for i := 0; i < maxTasksPerGoal; i++ {
		id := generateSwarmID()
		_, err := database.ExecContext(ctx,
			"INSERT INTO swarm_tasks (id,session_id,title,stage,goal_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?)",
			id, sessionID, fmt.Sprintf("task %d", i), "queued", goalID, now, now,
		)
		if err != nil {
			t.Fatalf("insert task %d: %v", i, err)
		}
	}

	// Creating one more via API should return 422
	w := swarmReq(t, "POST", "/api/swarm/sessions/"+sessionID+"/tasks",
		map[string]interface{}{"title": "overflow task", "stage": "queued", "goal_id": goalID})
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 when exceeding task limit, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGoal_Reconciliation_AutoComplete(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "reconcile-test")
	goalID := createSwarmGoal(t, sessionID, "reconcile me")

	ctx := context.Background()
	now := time.Now().Unix()

	// Create two tasks under this goal
	taskIDs := make([]string, 2)
	for i := 0; i < 2; i++ {
		id := generateSwarmID()
		database.ExecContext(ctx,
			"INSERT INTO swarm_tasks (id,session_id,title,stage,goal_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?)",
			id, sessionID, fmt.Sprintf("sub-task %d", i), "queued", goalID, now, now,
		)
		taskIDs[i] = id
	}

	// Mark both tasks complete
	for _, id := range taskIDs {
		database.ExecContext(ctx,
			"UPDATE swarm_tasks SET stage='complete', updated_at=? WHERE id=?",
			time.Now().Unix(), id,
		)
	}

	// Run reconciler
	reconcileGoal(ctx, sessionID, goalID)

	// Goal should now be complete
	var status string
	database.QueryRowContext(ctx, "SELECT status FROM swarm_goals WHERE id=?", goalID).Scan(&status)
	if status != "complete" {
		t.Errorf("expected goal status 'complete', got %q", status)
	}
}

func TestGoal_Reconciliation_NotComplete_WhenTasksActive(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "reconcile-active-test")
	goalID := createSwarmGoal(t, sessionID, "still going")

	ctx := context.Background()
	now := time.Now().Unix()

	// One complete, one still running
	id1 := generateSwarmID()
	id2 := generateSwarmID()
	database.ExecContext(ctx,
		"INSERT INTO swarm_tasks (id,session_id,title,stage,goal_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?)",
		id1, sessionID, "done task", "complete", goalID, now, now,
	)
	database.ExecContext(ctx,
		"INSERT INTO swarm_tasks (id,session_id,title,stage,goal_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?)",
		id2, sessionID, "active task", "running", goalID, now, now,
	)

	reconcileGoal(ctx, sessionID, goalID)

	var status string
	database.QueryRowContext(ctx, "SELECT status FROM swarm_goals WHERE id=?", goalID).Scan(&status)
	if status != "active" {
		t.Errorf("expected goal status 'active' (task still running), got %q", status)
	}
}

// ─── Escalation Tests ─────────────────────────────────────────────────────────

func TestEscalation_Load_Empty(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "esc-empty-test")

	escs, err := loadEscalations(sessionID)
	if err != nil {
		t.Fatalf("loadEscalations: %v", err)
	}
	if len(escs) != 0 {
		t.Errorf("expected empty escalations, got %d", len(escs))
	}
}

func TestEscalation_Load_WithFiles(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "esc-files-test")

	dir := swarmEscalationsDir(sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(swarmSessionDir(sessionID)) })

	// Write a valid escalation file
	escData := map[string]string{
		"session_id": sessionID,
		"agent_id":   "aabbccdd-0000-0000-0000-000000000000",
		"task_id":    "tttttttt-0000-0000-0000-000000000000",
		"reason":     "blocked on auth issue",
		"ts":         fmt.Sprintf("%d", time.Now().Unix()),
	}
	data, _ := json.Marshal(escData)
	fname := fmt.Sprintf("esc_%d_aabbccdd.json", time.Now().UnixNano())
	os.WriteFile(filepath.Join(dir, fname), data, 0644)

	escs, err := loadEscalations(sessionID)
	if err != nil {
		t.Fatalf("loadEscalations: %v", err)
	}
	if len(escs) != 1 {
		t.Fatalf("expected 1 escalation, got %d", len(escs))
	}
	if escs[0].Reason != "blocked on auth issue" {
		t.Errorf("unexpected reason: %q", escs[0].Reason)
	}
}

func TestEscalation_Respond_InvalidID_Rejected(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "esc-invalid-id-test")

	// Path traversal attempt
	w := swarmReq(t, "POST",
		"/api/swarm/sessions/"+sessionID+"/escalations/../../etc/passwd/respond",
		map[string]string{"text": "hi"})
	// Router won't even route this correctly — but in case it does, expect 4xx
	if w.Code < 400 {
		t.Errorf("expected 4xx for path traversal ID, got %d", w.Code)
	}
}

func TestEscalation_Respond_MalformedID_Rejected(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "esc-malformed-test")

	// ID doesn't match validEscIDRe
	w := swarmReq(t, "POST",
		"/api/swarm/sessions/"+sessionID+"/escalations/not_an_esc_id/respond",
		map[string]string{"text": "response text"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for malformed escalation ID, got %d", w.Code)
	}
}

func TestEscalation_Respond_NotFound(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "esc-notfound-test")
	t.Cleanup(func() { os.RemoveAll(swarmSessionDir(sessionID)) })
	os.MkdirAll(swarmEscalationsDir(sessionID), 0755)

	// Valid ID format but file doesn't exist → 404/conflict
	w := swarmReq(t, "POST",
		"/api/swarm/sessions/"+sessionID+"/escalations/esc_1234567890_aabbccdd/respond",
		map[string]string{"text": "response text"})
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409 (already responded / not found), got %d: %s", w.Code, w.Body.String())
	}
}

func TestEscalation_API_ListViaState(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "esc-api-list-test")

	dir := swarmEscalationsDir(sessionID)
	os.MkdirAll(dir, 0755)
	t.Cleanup(func() { os.RemoveAll(swarmSessionDir(sessionID)) })

	// Write an escalation file
	escData := map[string]string{
		"session_id": sessionID,
		"agent_id":   "00000000-0000-0000-0000-000000000001",
		"task_id":    "00000000-0000-0000-0000-000000000002",
		"reason":     "needs human review",
		"ts":         fmt.Sprintf("%d", time.Now().Unix()),
	}
	data, _ := json.Marshal(escData)
	fname := fmt.Sprintf("esc_%d_00000000.json", time.Now().UnixNano())
	os.WriteFile(filepath.Join(dir, fname), data, 0644)

	// GET via the session state should include escalations
	w := swarmReq(t, "GET", "/api/swarm/sessions/"+sessionID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var state SwarmState
	mustDecodeJSON(t, w, &state)
	if len(state.Escalations) == 0 {
		t.Error("expected at least one escalation in session state")
	}
}
