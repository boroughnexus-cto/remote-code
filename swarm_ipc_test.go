package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ─── readNewEvents tests ───────────────────────────────────────────────────────

func writeJSONLFile(t *testing.T, path string, events []IPCEvent) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			t.Fatalf("encode event: %v", err)
		}
	}
}

func TestReadNewEvents_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	os.WriteFile(path, []byte{}, 0644)

	newOffset, events := readNewEvents(path, 0)
	if newOffset != 0 {
		t.Errorf("expected offset 0 for empty file, got %d", newOffset)
	}
	if len(events) != 0 {
		t.Errorf("expected no events, got %d", len(events))
	}
}

func TestReadNewEvents_FileNotExist(t *testing.T) {
	newOffset, events := readNewEvents("/nonexistent/events.jsonl", 42)
	if newOffset != 42 {
		t.Errorf("expected original offset 42 on missing file, got %d", newOffset)
	}
	if len(events) != 0 {
		t.Errorf("expected no events, got %d", len(events))
	}
}

func TestReadNewEvents_SingleEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	ev := IPCEvent{Event: "task_progress", TaskID: "abc123", Note: "working on it"}
	writeJSONLFile(t, path, []IPCEvent{ev})

	newOffset, events := readNewEvents(path, 0)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Event != "task_progress" {
		t.Errorf("expected event 'task_progress', got %q", events[0].Event)
	}
	if events[0].Note != "working on it" {
		t.Errorf("unexpected note: %q", events[0].Note)
	}
	if newOffset <= 0 {
		t.Errorf("expected positive new offset, got %d", newOffset)
	}
}

func TestReadNewEvents_MultipleEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	evs := []IPCEvent{
		{Event: "task_accepted", TaskID: "t1"},
		{Event: "task_progress", TaskID: "t1", Note: "step 1"},
		{Event: "heartbeat", ContextPct: 0.45},
	}
	writeJSONLFile(t, path, evs)

	newOffset, events := readNewEvents(path, 0)
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[2].Event != "heartbeat" {
		t.Errorf("expected 3rd event 'heartbeat', got %q", events[2].Event)
	}
	_ = newOffset
}

func TestReadNewEvents_ResumeFromOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	ev1 := IPCEvent{Event: "task_accepted", TaskID: "t1"}
	ev2 := IPCEvent{Event: "task_progress", TaskID: "t1", Note: "later"}
	writeJSONLFile(t, path, []IPCEvent{ev1, ev2})

	// First read: get ev1
	offset1, events1 := readNewEvents(path, 0)
	if len(events1) != 2 {
		t.Fatalf("expected 2 events on first read, got %d", len(events1))
	}

	// Append a third event
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	ev3 := IPCEvent{Event: "heartbeat", ContextPct: 0.5}
	json.NewEncoder(f).Encode(ev3)
	f.Close()

	// Second read from saved offset: should only get ev3
	_, events2 := readNewEvents(path, offset1)
	if len(events2) != 1 {
		t.Fatalf("expected 1 new event from offset, got %d", len(events2))
	}
	if events2[0].Event != "heartbeat" {
		t.Errorf("expected 'heartbeat', got %q", events2[0].Event)
	}
}

func TestReadNewEvents_SkipsBadJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	// Mix valid and invalid JSON lines
	content := `{"event":"task_accepted","task_id":"t1"}` + "\n" +
		`not json at all` + "\n" +
		`{"event":"heartbeat","context_pct":0.3}` + "\n"
	os.WriteFile(path, []byte(content), 0644)

	_, events := readNewEvents(path, 0)
	if len(events) != 2 {
		t.Errorf("expected 2 valid events (bad JSON skipped), got %d", len(events))
	}
}

func TestReadNewEvents_LargeEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	// Create an event with a very large Note field (>64KB, default scanner limit)
	largeNote := strings.Repeat("x", 128*1024) // 128KB
	ev := IPCEvent{Event: "decision_made", Content: largeNote}

	data, _ := json.Marshal(ev)
	os.WriteFile(path, append(data, '\n'), 0644)

	_, events := readNewEvents(path, 0)
	if len(events) != 1 {
		t.Fatalf("expected 1 large event, got %d (scanner may have dropped it)", len(events))
	}
	if len(events[0].Content) != 128*1024 {
		t.Errorf("expected content length %d, got %d", 128*1024, len(events[0].Content))
	}
}

// ─── processHandoffFile tests ─────────────────────────────────────────────────

func makeHandoff(t *testing.T, dir, taskID string) string {
	t.Helper()
	h := IPCHandoff{
		SchemaVersion: "1",
		TaskID:        taskID,
		AgentID:       "test-agent",
		Status:        "complete",
		Summary:       "All done",
		Confidence:    0.9,
		TestsPassed:   true,
		CompletedAt:   time.Now().Unix(),
	}
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "handoff.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProcessHandoffFile_DeletesOnSuccess(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "handoff-success-test")
	taskID := createSwarmTask(t, sessionID, "handoff task")

	// Transition task to running state (required for CompleteTask via running→complete)
	ctx := context.Background()
	transitionTask(ctx, taskID, "assigned")
	transitionTask(ctx, taskID, "accepted")
	transitionTask(ctx, taskID, "running")

	dir := t.TempDir()
	agentID := generateSwarmID() // any ID; CompleteTask only needs taskID to exist

	path := makeHandoff(t, dir, taskID)

	if err := processHandoffFile(ctx, sessionID, agentID, path); err != nil {
		t.Fatalf("processHandoffFile: %v", err)
	}

	// File should be gone after successful processing
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected handoff file to be deleted after successful processing")
	}

	// Task should now be complete (or needs_review — confidence=0.9, tests passed)
	var stage string
	database.QueryRowContext(ctx, "SELECT stage FROM swarm_tasks WHERE id=?", taskID).Scan(&stage)
	if stage != "complete" && stage != "needs_review" {
		t.Errorf("expected task in terminal/review state, got %q", stage)
	}
}

func TestProcessHandoffFile_PreservesFileOnFailure(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "handoff-failure-test")

	dir := t.TempDir()
	agentID := generateSwarmID()

	// Task ID that does NOT exist in the DB
	nonexistentTaskID := generateSwarmID()
	path := makeHandoff(t, dir, nonexistentTaskID)

	err := processHandoffFile(context.Background(), sessionID, agentID, path)
	if err == nil {
		t.Error("expected error when task not found")
	}

	// File should still exist (not deleted on failure)
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		t.Error("handoff file should be preserved when processing fails")
	}
}

func TestProcessHandoffFile_MissingTaskID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "handoff.json")

	// Write handoff with empty task_id
	h := IPCHandoff{SchemaVersion: "1", TaskID: "", Status: "complete", Confidence: 0.9}
	data, _ := json.Marshal(h)
	os.WriteFile(path, data, 0644)

	err := processHandoffFile(context.Background(), "sid", "aid", path)
	if err == nil {
		t.Error("expected error for handoff with empty task_id")
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		t.Error("file should be preserved on validation failure")
	}
}

// ─── HandoffPath validation tests ────────────────────────────────────────────

func TestHandoffPath_GuardRejectsOutsideOutbox(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "handoff-guard-test")
	agentID := generateSwarmID()

	// Check the guard logic: /etc/passwd is not under agent outbox
	expectedDir := filepath.Clean(swarmOutboxDir(sessionID, agentID))
	cleanPath := filepath.Clean("/etc/passwd")

	if strings.HasPrefix(cleanPath, expectedDir+string(filepath.Separator)) {
		t.Error("/etc/passwd should NOT be under the outbox dir")
	}
}

func TestHandoffPath_GuardAllowsInsideOutbox(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "handoff-allowed-test")
	agentID := generateSwarmID()

	outboxDir := swarmOutboxDir(sessionID, agentID)
	legitPath := filepath.Join(outboxDir, "handoff.json")

	expectedDir := filepath.Clean(outboxDir)
	cleanPath := filepath.Clean(legitPath)

	if !strings.HasPrefix(cleanPath, expectedDir+string(filepath.Separator)) {
		t.Errorf("legitimate path %q should be under outbox %q", cleanPath, expectedDir)
	}
}

// ─── checkGoalTaskLimit tests ─────────────────────────────────────────────────

func TestCheckGoalTaskLimit_UnderLimit(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "limit-under-test")

	ctx := context.Background()
	goalID := generateSwarmID()
	now := time.Now().Unix()
	database.ExecContext(ctx,
		"INSERT INTO swarm_goals (id,session_id,description,status,created_at,updated_at) VALUES (?,?,?,?,?,?)",
		goalID, sessionID, "test goal", "active", now, now,
	)

	// Add 3 tasks (under limit of 8)
	for i := 0; i < 3; i++ {
		id := generateSwarmID()
		database.ExecContext(ctx,
			"INSERT INTO swarm_tasks (id,session_id,title,stage,goal_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?)",
			id, sessionID, fmt.Sprintf("t%d", i), "queued", goalID, now, now,
		)
	}

	if err := checkGoalTaskLimit(ctx, goalID); err != nil {
		t.Errorf("expected no error under limit, got: %v", err)
	}
}

func TestCheckGoalTaskLimit_AtLimit(t *testing.T) {
	setupSwarmDB(t)
	sessionID := createSwarmSession(t, "limit-at-test")

	ctx := context.Background()
	goalID := generateSwarmID()
	now := time.Now().Unix()
	database.ExecContext(ctx,
		"INSERT INTO swarm_goals (id,session_id,description,status,created_at,updated_at) VALUES (?,?,?,?,?,?)",
		goalID, sessionID, "full goal", "active", now, now,
	)

	for i := 0; i < maxTasksPerGoal; i++ {
		id := generateSwarmID()
		database.ExecContext(ctx,
			"INSERT INTO swarm_tasks (id,session_id,title,stage,goal_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?)",
			id, sessionID, fmt.Sprintf("t%d", i), "queued", goalID, now, now,
		)
	}

	if err := checkGoalTaskLimit(ctx, goalID); err == nil {
		t.Error("expected error when at task limit")
	}
}

// ─── validTmuxTarget tests ────────────────────────────────────────────────────

func TestValidTmuxTarget(t *testing.T) {
	valid := []string{
		"swarm-abc123",
		"session_1",
		"agent.worker.1",
		"abc",
	}
	for _, v := range valid {
		if !validTmuxTarget(v) {
			t.Errorf("expected %q to be a valid tmux target", v)
		}
	}

	invalid := []string{
		"session:window",  // colon is tmux grammar
		"sess%0",          // percent sign
		"a b",             // space
		"a\tb",            // tab
		"../etc/passwd",   // traversal chars
		"",                // empty
	}
	for _, v := range invalid {
		if validTmuxTarget(v) {
			t.Errorf("expected %q to be an invalid tmux target", v)
		}
	}
}

// ─── validEscIDRe tests ───────────────────────────────────────────────────────

func TestValidEscIDRe(t *testing.T) {
	valid := []string{
		"esc_1710698400000000000_ab12cd34",
		"esc_0_deadbeef",
	}
	for _, v := range valid {
		if !validEscIDRe.MatchString(v) {
			t.Errorf("expected %q to match validEscIDRe", v)
		}
	}

	invalid := []string{
		"../../etc/passwd",
		"esc_123_UPPERCASE",   // uppercase not allowed
		"esc_abc_deadbeef",    // non-numeric timestamp
		"notanesc",
		"esc_123_",            // empty hex part
	}
	for _, v := range invalid {
		if validEscIDRe.MatchString(v) {
			t.Errorf("expected %q NOT to match validEscIDRe", v)
		}
	}
}
