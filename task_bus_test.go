package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// setupTestDB swaps the global `database` for a fresh temp DB, runs all
// migrations, and returns a cleanup function that restores the original DB
// and removes the temp file.
func setupTestDB(t *testing.T) func() {
	t.Helper()
	prev := database
	testDB, dsn := initTestDatabase()
	database = testDB
	return func() {
		database = prev
		testDB.Close()
		// Remove the temp file (strip query params to get the real path)
		path := strings.SplitN(dsn, "?", 2)[0]
		os.Remove(path)
	}
}

// taskTestDB sets up an in-memory test DB with all migrations applied.
func taskTestDB(t *testing.T) func() {
	t.Helper()
	return setupTestDB(t)
}

// ptr helpers
func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

// ─── createTask ───────────────────────────────────────────────────────────────

func TestCreateTask_Basic(t *testing.T) {
	defer taskTestDB(t)()
	ctx := context.Background()

	task, err := createTask(ctx, CreateTaskRequest{
		SessionID: strPtr("sess-1"),
		SenderID:  "test",
		Type:      "goal",
		Payload:   `{"text":"hello"}`,
	})
	if err != nil {
		t.Fatalf("createTask: %v", err)
	}
	if task.State != "pending" {
		t.Errorf("expected state=pending, got %s", task.State)
	}
	if task.Priority != 5 {
		t.Errorf("expected default priority=5, got %d", task.Priority)
	}
}

func TestCreateTask_Dedup(t *testing.T) {
	defer taskTestDB(t)()
	ctx := context.Background()

	req := CreateTaskRequest{
		SessionID:  strPtr("sess-1"),
		SenderID:   "test",
		Type:       "goal",
		Payload:    `{}`,
		ExternalID: strPtr("plane-issue-123"),
	}

	if _, err := createTask(ctx, req); err != nil {
		t.Fatalf("first createTask: %v", err)
	}
	_, err := createTask(ctx, req)
	if err != ErrTaskDuplicate {
		t.Errorf("expected ErrTaskDuplicate, got %v", err)
	}
}

// ─── getTaskInbox ─────────────────────────────────────────────────────────────

func TestGetTaskInbox_OrderedByPriority(t *testing.T) {
	defer taskTestDB(t)()
	ctx := context.Background()
	sess := "sess-inbox"

	// Insert tasks with priorities 3, 1, 2
	for _, p := range []int{3, 1, 2} {
		if _, err := createTask(ctx, CreateTaskRequest{
			SessionID: strPtr(sess),
			SenderID:  "test",
			Type:      "goal",
			Payload:   fmt.Sprintf(`{"p":%d}`, p),
			Priority:  p,
		}); err != nil {
			t.Fatalf("createTask p=%d: %v", p, err)
		}
	}

	tasks, err := getTaskInbox(ctx, sess, 50)
	if err != nil {
		t.Fatalf("getTaskInbox: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}
	if tasks[0].Priority != 1 || tasks[1].Priority != 2 || tasks[2].Priority != 3 {
		t.Errorf("wrong order: %v %v %v", tasks[0].Priority, tasks[1].Priority, tasks[2].Priority)
	}
}

func TestGetTaskInbox_LimitCap(t *testing.T) {
	defer taskTestDB(t)()
	ctx := context.Background()
	sess := "sess-cap"
	for i := 0; i < 5; i++ {
		createTask(ctx, CreateTaskRequest{SessionID: strPtr(sess), SenderID: "test", Type: "goal", Payload: "{}"}) //nolint:errcheck
	}
	tasks, err := getTaskInbox(ctx, sess, 2)
	if err != nil {
		t.Fatalf("getTaskInbox: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks (limit=2), got %d", len(tasks))
	}
}

// ─── acceptTask ───────────────────────────────────────────────────────────────

func TestAcceptTask_Success(t *testing.T) {
	defer taskTestDB(t)()
	ctx := context.Background()
	sess := "sess-accept"

	task, _ := createTask(ctx, CreateTaskRequest{SessionID: strPtr(sess), SenderID: "test", Type: "goal", Payload: "{}"})
	if err := acceptTask(ctx, task.ID, sess, "agent-1"); err != nil {
		t.Fatalf("acceptTask: %v", err)
	}

	got, _ := getTask(ctx, task.ID)
	if got.State != "accepted" {
		t.Errorf("expected accepted, got %s", got.State)
	}
	if got.AgentID == nil || *got.AgentID != "agent-1" {
		t.Errorf("expected agent_id=agent-1, got %v", got.AgentID)
	}
}

func TestAcceptTask_OptimisticLock(t *testing.T) {
	defer taskTestDB(t)()
	ctx := context.Background()
	sess := "sess-lock"

	task, _ := createTask(ctx, CreateTaskRequest{SessionID: strPtr(sess), SenderID: "test", Type: "goal", Payload: "{}"})

	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = acceptTask(ctx, task.ID, sess, fmt.Sprintf("agent-%d", idx))
		}(i)
	}
	wg.Wait()

	wins := 0
	for _, err := range results {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("expected exactly 1 winner, got %d (results: %v %v)", wins, results[0], results[1])
	}
}

func TestAcceptTask_WrongSession(t *testing.T) {
	defer taskTestDB(t)()
	ctx := context.Background()

	task, _ := createTask(ctx, CreateTaskRequest{SessionID: strPtr("sess-A"), SenderID: "test", Type: "goal", Payload: "{}"})
	err := acceptTask(ctx, task.ID, "sess-B", "agent-1")
	if err != ErrTaskConflict {
		t.Errorf("expected ErrTaskConflict for wrong session, got %v", err)
	}
}

// ─── rejectTask ───────────────────────────────────────────────────────────────

func TestRejectTask(t *testing.T) {
	defer taskTestDB(t)()
	ctx := context.Background()
	sess := "sess-reject"

	task, _ := createTask(ctx, CreateTaskRequest{SessionID: strPtr(sess), SenderID: "test", Type: "goal", Payload: "{}"})
	if err := rejectTask(ctx, task.ID, sess, "agent-1", "not applicable"); err != nil {
		t.Fatalf("rejectTask: %v", err)
	}
	got, _ := getTask(ctx, task.ID)
	if got.State != "rejected" {
		t.Errorf("expected rejected, got %s", got.State)
	}
}

// ─── completeTask ─────────────────────────────────────────────────────────────

func TestCompleteTask_OnlyAcceptingAgent(t *testing.T) {
	defer taskTestDB(t)()
	ctx := context.Background()
	sess := "sess-complete"

	task, _ := createTask(ctx, CreateTaskRequest{SessionID: strPtr(sess), SenderID: "test", Type: "goal", Payload: "{}"})
	acceptTask(ctx, task.ID, sess, "agent-A") //nolint:errcheck

	// Wrong agent should fail
	err := completeTask(ctx, task.ID, sess, "agent-B", "done")
	if err != ErrTaskConflict {
		t.Errorf("expected conflict for wrong agent, got %v", err)
	}

	// Correct agent should succeed
	if err := completeTask(ctx, task.ID, sess, "agent-A", "done"); err != nil {
		t.Errorf("completeTask by correct agent: %v", err)
	}
	got, _ := getTask(ctx, task.ID)
	if got.State != "completed" {
		t.Errorf("expected completed, got %s", got.State)
	}
}

// ─── failTask ─────────────────────────────────────────────────────────────────

func TestFailTask(t *testing.T) {
	defer taskTestDB(t)()
	ctx := context.Background()
	sess := "sess-fail"

	task, _ := createTask(ctx, CreateTaskRequest{SessionID: strPtr(sess), SenderID: "test", Type: "goal", Payload: "{}"})
	acceptTask(ctx, task.ID, sess, "agent-1") //nolint:errcheck
	if err := failTask(ctx, task.ID, sess, "agent-1", "unrecoverable"); err != nil {
		t.Fatalf("failTask: %v", err)
	}
	got, _ := getTask(ctx, task.ID)
	if got.State != "failed" {
		t.Errorf("expected failed, got %s", got.State)
	}
}

// ─── deferTask ────────────────────────────────────────────────────────────────

func TestDeferTask(t *testing.T) {
	defer taskTestDB(t)()
	ctx := context.Background()
	sess := "sess-defer"

	task, _ := createTask(ctx, CreateTaskRequest{SessionID: strPtr(sess), SenderID: "test", Type: "goal", Payload: "{}"})
	until := time.Now().Add(1 * time.Hour)
	if err := deferTask(ctx, task.ID, sess, "agent-1", until); err != nil {
		t.Fatalf("deferTask: %v", err)
	}
	got, _ := getTask(ctx, task.ID)
	if got.State != "deferred" {
		t.Errorf("expected deferred, got %s", got.State)
	}
	if got.DeferUntil == nil {
		t.Error("expected defer_until to be set")
	}
}

// ─── TTL sweeper ─────────────────────────────────────────────────────────────

func TestTTLSweeper_ExpiresTask(t *testing.T) {
	defer taskTestDB(t)()
	ctx := context.Background()
	sess := "sess-ttl"

	// TTL=0 means expires immediately on first sweep
	task, err := createTask(ctx, CreateTaskRequest{
		SessionID:  strPtr(sess),
		SenderID:   "test",
		Type:       "goal",
		Payload:    "{}",
		TTLSeconds: intPtr(0),
	})
	if err != nil {
		t.Fatalf("createTask: %v", err)
	}

	runTTLSweep(ctx)

	got, _ := getTask(ctx, task.ID)
	if got.State != "expired" {
		t.Errorf("expected expired after TTL sweep, got %s", got.State)
	}
}

func TestTTLSweeper_RequeuesDeferred(t *testing.T) {
	defer taskTestDB(t)()
	ctx := context.Background()
	sess := "sess-requeue"

	task, _ := createTask(ctx, CreateTaskRequest{SessionID: strPtr(sess), SenderID: "test", Type: "goal", Payload: "{}"})
	// Defer to the past so the sweeper re-queues immediately
	past := time.Now().Add(-1 * time.Minute)
	deferTask(ctx, task.ID, sess, "agent-1", past) //nolint:errcheck

	runTTLSweep(ctx)

	got, _ := getTask(ctx, task.ID)
	if got.State != "pending" {
		t.Errorf("expected requeued to pending, got %s", got.State)
	}
	if got.DeferUntil != nil {
		t.Error("expected defer_until cleared after requeue")
	}
}

// ─── task_events audit trail ──────────────────────────────────────────────────

func TestTaskEvents_FullLifecycle(t *testing.T) {
	defer taskTestDB(t)()
	ctx := context.Background()
	sess := "sess-events"

	task, _ := createTask(ctx, CreateTaskRequest{SessionID: strPtr(sess), SenderID: "test", Type: "goal", Payload: "{}"})
	acceptTask(ctx, task.ID, sess, "agent-1")               //nolint:errcheck
	completeTask(ctx, task.ID, sess, "agent-1", "finished") //nolint:errcheck

	events, err := listTaskEvents(ctx, task.ID)
	if err != nil {
		t.Fatalf("listTaskEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	// Check all three event types are present (order may vary within the same second)
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.Event] = true
	}
	for _, want := range []string{"created", "accepted", "completed"} {
		if !seen[want] {
			t.Errorf("missing event %q in audit trail; got: %v %v %v", want, events[0].Event, events[1].Event, events[2].Event)
		}
	}
}
