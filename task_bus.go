package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// ─── Sentinel errors ─────────────────────────────────────────────────────────

var (
	ErrTaskNotFound  = errors.New("task not found")
	ErrTaskConflict  = errors.New("task state conflict — concurrent update or wrong current state")
	ErrTaskDuplicate = errors.New("task with this external_id already exists")
)

// ─── Types ───────────────────────────────────────────────────────────────────

// Task is a unit of work in the task bus.
type Task struct {
	ID         string  `json:"id"`
	SessionID  *string `json:"session_id,omitempty"`
	AgentID    *string `json:"agent_id,omitempty"`
	SenderID   string  `json:"sender_id"`
	Source     *string `json:"source,omitempty"`
	Type       string  `json:"type"`
	Payload    string  `json:"payload"`
	State      string  `json:"state"`
	Priority   int     `json:"priority"`
	ExternalID *string `json:"external_id,omitempty"`
	CreatedAt  string  `json:"created_at"`
	AcceptedAt *string `json:"accepted_at,omitempty"`
	ResolvedAt *string `json:"resolved_at,omitempty"`
	DeferUntil *string `json:"defer_until,omitempty"`
	TTLSeconds *int    `json:"ttl_seconds,omitempty"`
}

// TaskEvent is an audit record for a task state transition.
type TaskEvent struct {
	ID      string  `json:"id"`
	TaskID  string  `json:"task_id"`
	AgentID *string `json:"agent_id,omitempty"`
	Event   string  `json:"event"`
	Reason  *string `json:"reason,omitempty"`
	TS      string  `json:"ts"`
}

// CreateTaskRequest is the input to createTask.
type CreateTaskRequest struct {
	SessionID  *string `json:"session_id"`
	SenderID   string  `json:"sender_id"`
	Source     *string `json:"source"`
	Type       string  `json:"type"`
	Payload    string  `json:"payload"`
	Priority   int     `json:"priority"`
	ExternalID *string `json:"external_id"`
	TTLSeconds *int    `json:"ttl_seconds"`
}

// ─── CRUD ────────────────────────────────────────────────────────────────────

// createTask inserts a new task. Returns ErrTaskDuplicate if external_id collides.
func createTask(ctx context.Context, req CreateTaskRequest) (*Task, error) {
	if req.Priority == 0 {
		req.Priority = 5
	}
	id := generateID()

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("task_bus: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.ExecContext(ctx,
		`INSERT INTO bus_tasks
		   (id, session_id, sender_id, source, type, payload, state, priority, external_id, ttl_seconds)
		 VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?)`,
		id, req.SessionID, req.SenderID, req.Source,
		req.Type, req.Payload, req.Priority, req.ExternalID, req.TTLSeconds,
	)
	if err != nil {
		if strings.Contains(err.Error(), "bus_tasks.external_id") {
			return nil, ErrTaskDuplicate
		}
		return nil, fmt.Errorf("task_bus: insert task: %w", err)
	}

	if _, err = tx.ExecContext(ctx,
		`INSERT INTO bus_task_events (id, task_id, agent_id, event) VALUES (?, ?, NULL, 'created')`,
		generateID(), id,
	); err != nil {
		return nil, fmt.Errorf("task_bus: insert event: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("task_bus: commit: %w", err)
	}

	return getTask(ctx, id)
}

// getTask returns a single task by ID.
func getTask(ctx context.Context, id string) (*Task, error) {
	row := database.QueryRowContext(ctx,
		`SELECT id, session_id, agent_id, sender_id, source, type, payload, state, priority,
		        external_id, created_at, accepted_at, resolved_at, defer_until, ttl_seconds
		 FROM bus_tasks WHERE id = ?`, id)
	return scanTask(row)
}

// getTaskInbox returns pending tasks for a session ordered by priority ASC (1=highest), created_at ASC.
// Results are capped at limit (default 50, max 200).
func getTaskInbox(ctx context.Context, sessionID string, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := database.QueryContext(ctx,
		`SELECT id, session_id, agent_id, sender_id, source, type, payload, state, priority,
		        external_id, created_at, accepted_at, resolved_at, defer_until, ttl_seconds
		 FROM bus_tasks
		 WHERE session_id = ? AND state = 'pending'
		 ORDER BY priority ASC, created_at ASC
		 LIMIT ?`,
		sessionID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

// listTaskEvents returns audit events for a task, newest first.
func listTaskEvents(ctx context.Context, taskID string) ([]TaskEvent, error) {
	rows, err := database.QueryContext(ctx,
		`SELECT id, task_id, agent_id, event, reason, ts
		 FROM bus_task_events WHERE task_id = ? ORDER BY ts DESC`,
		taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []TaskEvent
	for rows.Next() {
		var e TaskEvent
		var agentID, reason sql.NullString
		if err := rows.Scan(&e.ID, &e.TaskID, &agentID, &e.Event, &reason, &e.TS); err != nil {
			return nil, err
		}
		if agentID.Valid {
			e.AgentID = &agentID.String
		}
		if reason.Valid {
			e.Reason = &reason.String
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// ─── State machine transitions ────────────────────────────────────────────────
// Every transition atomically combines a conditional UPDATE with a task_event INSERT.

// acceptTask transitions pending → accepted. Returns ErrTaskConflict if already taken.
func acceptTask(ctx context.Context, taskID, sessionID, agentID string) error {
	return taskTransition(ctx, taskID, agentID, "", "accepted",
		`UPDATE bus_tasks SET state='accepted', agent_id=?, accepted_at=CURRENT_TIMESTAMP
		 WHERE id=? AND session_id=? AND state='pending'`,
		agentID, taskID, sessionID,
	)
}

// rejectTask transitions pending → rejected.
func rejectTask(ctx context.Context, taskID, sessionID, agentID, reason string) error {
	return taskTransition(ctx, taskID, agentID, reason, "rejected",
		`UPDATE bus_tasks SET state='rejected', agent_id=?, resolved_at=CURRENT_TIMESTAMP
		 WHERE id=? AND session_id=? AND state='pending'`,
		agentID, taskID, sessionID,
	)
}

// deferTask transitions pending → deferred with a re-queue timestamp.
func deferTask(ctx context.Context, taskID, sessionID, agentID string, until time.Time) error {
	return taskTransition(ctx, taskID, agentID, "", "deferred",
		`UPDATE bus_tasks SET state='deferred', agent_id=?, defer_until=?
		 WHERE id=? AND session_id=? AND state='pending'`,
		agentID, until.UTC().Format("2006-01-02 15:04:05"), taskID, sessionID,
	)
}

// completeTask transitions accepted → completed. Only the accepting agent may complete.
func completeTask(ctx context.Context, taskID, sessionID, agentID, result string) error {
	return taskTransition(ctx, taskID, agentID, result, "completed",
		`UPDATE bus_tasks SET state='completed', resolved_at=CURRENT_TIMESTAMP
		 WHERE id=? AND session_id=? AND agent_id=? AND state='accepted'`,
		taskID, sessionID, agentID,
	)
}

// failTask transitions accepted → failed. Only the accepting agent may fail.
func failTask(ctx context.Context, taskID, sessionID, agentID, reason string) error {
	return taskTransition(ctx, taskID, agentID, reason, "failed",
		`UPDATE bus_tasks SET state='failed', resolved_at=CURRENT_TIMESTAMP
		 WHERE id=? AND session_id=? AND agent_id=? AND state='accepted'`,
		taskID, sessionID, agentID,
	)
}

// taskTransition executes a conditional UPDATE and, on success, inserts a task_event — all in one transaction.
// updateSQL must affect exactly 1 row; 0 rows → ErrTaskConflict.
func taskTransition(ctx context.Context, taskID, agentID, reason, eventName, updateSQL string, updateArgs ...interface{}) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("task_bus: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	res, err := tx.ExecContext(ctx, updateSQL, updateArgs...)
	if err != nil {
		return fmt.Errorf("task_bus: update %s: %w", eventName, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("task_bus: rows affected: %w", err)
	}
	if n == 0 {
		return ErrTaskConflict
	}

	agentParam := sql.NullString{String: agentID, Valid: agentID != ""}
	reasonParam := sql.NullString{String: reason, Valid: reason != ""}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO bus_task_events (id, task_id, agent_id, event, reason) VALUES (?, ?, ?, ?, ?)`,
		generateID(), taskID, agentParam, eventName, reasonParam,
	); err != nil {
		return fmt.Errorf("task_bus: insert event: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("task_bus: commit: %w", err)
	}
	log.Printf("task_bus: task %s → %s (agent=%s)", taskID, eventName, agentID)
	return nil
}

// ─── TTL sweeper ─────────────────────────────────────────────────────────────

// startTTLSweeper runs a background goroutine that:
//  1. Expires pending tasks past their TTL.
//  2. Re-queues deferred tasks whose defer_until has passed.
func startTTLSweeper(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				runTTLSweep(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func runTTLSweep(ctx context.Context) {
	sweepTransition(ctx, "expired",
		`UPDATE bus_tasks SET state='expired', resolved_at=CURRENT_TIMESTAMP
		 WHERE state='pending'
		   AND ttl_seconds IS NOT NULL
		   AND CAST((julianday('now') - julianday(created_at)) * 86400 AS INTEGER) >= ttl_seconds
		 RETURNING id`,
	)
	sweepTransition(ctx, "requeued",
		`UPDATE bus_tasks SET state='pending', defer_until=NULL
		 WHERE state='deferred' AND strftime('%s', defer_until) <= strftime('%s', 'now')
		 RETURNING id`,
	)
}

// sweepTransition atomically updates tasks matching updateSQL (which must use RETURNING id)
// and inserts a task_event for each affected task — all in one transaction.
// This ensures the event record is always paired with its state change.
func sweepTransition(ctx context.Context, eventName, updateSQL string) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("task_bus: sweeper %s begin tx: %v", eventName, err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	rows, err := tx.QueryContext(ctx, updateSQL)
	if err != nil {
		log.Printf("task_bus: sweeper %s update: %v", eventName, err)
		return
	}

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			log.Printf("task_bus: sweeper %s scan: %v", eventName, err)
			return
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("task_bus: sweeper %s rows: %v", eventName, err)
		return
	}

	if len(ids) == 0 {
		return // nothing to do; rollback is harmless
	}

	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO bus_task_events (id, task_id, event) VALUES (?, ?, ?)`,
			generateID(), id, eventName,
		); err != nil {
			log.Printf("task_bus: sweeper %s event insert %s: %v", eventName, id, err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("task_bus: sweeper %s commit: %v", eventName, err)
		return
	}
	log.Printf("task_bus: sweeper %s %d task(s)", eventName, len(ids))
}

// ─── Scan helpers ─────────────────────────────────────────────────────────────

func scanTask(row *sql.Row) (*Task, error) {
	var t Task
	var sessionID, agentID, source, externalID, acceptedAt, resolvedAt, deferUntil sql.NullString
	var ttlSeconds sql.NullInt64
	err := row.Scan(
		&t.ID, &sessionID, &agentID, &t.SenderID, &source,
		&t.Type, &t.Payload, &t.State, &t.Priority, &externalID,
		&t.CreatedAt, &acceptedAt, &resolvedAt, &deferUntil, &ttlSeconds,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	applyTaskNulls(&t, sessionID, agentID, source, externalID, acceptedAt, resolvedAt, deferUntil, ttlSeconds)
	return &t, nil
}

func scanTasks(rows *sql.Rows) ([]Task, error) {
	var tasks []Task
	for rows.Next() {
		var t Task
		var sessionID, agentID, source, externalID, acceptedAt, resolvedAt, deferUntil sql.NullString
		var ttlSeconds sql.NullInt64
		if err := rows.Scan(
			&t.ID, &sessionID, &agentID, &t.SenderID, &source,
			&t.Type, &t.Payload, &t.State, &t.Priority, &externalID,
			&t.CreatedAt, &acceptedAt, &resolvedAt, &deferUntil, &ttlSeconds,
		); err != nil {
			return nil, err
		}
		applyTaskNulls(&t, sessionID, agentID, source, externalID, acceptedAt, resolvedAt, deferUntil, ttlSeconds)
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func applyTaskNulls(t *Task, sessionID, agentID, source, externalID, acceptedAt, resolvedAt, deferUntil sql.NullString, ttlSeconds sql.NullInt64) {
	if sessionID.Valid {
		t.SessionID = &sessionID.String
	}
	if agentID.Valid {
		t.AgentID = &agentID.String
	}
	if source.Valid {
		t.Source = &source.String
	}
	if externalID.Valid {
		t.ExternalID = &externalID.String
	}
	if acceptedAt.Valid {
		t.AcceptedAt = &acceptedAt.String
	}
	if resolvedAt.Valid {
		t.ResolvedAt = &resolvedAt.String
	}
	if deferUntil.Valid {
		t.DeferUntil = &deferUntil.String
	}
	if ttlSeconds.Valid {
		v := int(ttlSeconds.Int64)
		t.TTLSeconds = &v
	}
}
