package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ─── Status/stage helpers ─────────────────────────────────────────────────────

// setAgentStatus updates an agent's status and records the timestamp.
// Used wherever agent status changes to keep status_changed_at in sync.
func setAgentStatus(ctx context.Context, agentID, status string) {
	now := time.Now().Unix()
	database.ExecContext(ctx, //nolint:errcheck
		"UPDATE swarm_agents SET status = ?, status_changed_at = ? WHERE id = ?",
		status, now, agentID)
}

// ─── Valid state transitions ──────────────────────────────────────────────────

var validTransitions = map[string][]string{
	"queued":       {"assigned"},
	"assigned":     {"accepted", "queued"},
	"accepted":     {"running", "blocked", "failed"},
	"running":      {"complete", "blocked", "needs_review", "needs_human", "failed", "timed_out"},
	"blocked":      {"queued", "running", "failed"},
	"needs_review": {"running", "complete", "failed"},
	"needs_human":  {"running", "complete", "failed"},
	"complete":     {},
	"timed_out":    {},
	"failed":       {},
}

func isValidTransition(from, to string) bool {
	allowed, ok := validTransitions[from]
	if !ok {
		return false
	}
	for _, a := range allowed {
		if a == to {
			return true
		}
	}
	return false
}

// transitionTask moves a task to newStage if the transition is valid.
// Idempotent: already-in-state is a no-op (not an error).
func transitionTask(ctx context.Context, taskID, newStage string) error {
	var cur string
	err := database.QueryRowContext(ctx, "SELECT stage FROM swarm_tasks WHERE id=?", taskID).Scan(&cur)
	if err != nil {
		return fmt.Errorf("task %s not found: %w", taskID[:8], err)
	}
	if cur == newStage {
		return nil // idempotent
	}
	if !isValidTransition(cur, newStage) {
		return fmt.Errorf("invalid transition %s→%s for task %s", cur, newStage, taskID[:8])
	}
	now := time.Now().Unix()
	_, err = database.ExecContext(ctx,
		"UPDATE swarm_tasks SET stage=?, updated_at=?, stage_changed_at=? WHERE id=?",
		newStage, now, now, taskID,
	)
	return err
}

// ─── State-specific setters ───────────────────────────────────────────────────

// checkPhaseOrderConstraint returns an error if predecessor phases (lower
// phase_order in the same goal) have not yet reached a terminal stage.
func checkPhaseOrderConstraint(ctx context.Context, taskID string) error {
	var goalID sql.NullString
	var phaseOrder sql.NullInt64
	if err := database.QueryRowContext(ctx,
		"SELECT goal_id, phase_order FROM swarm_tasks WHERE id=?", taskID,
	).Scan(&goalID, &phaseOrder); err != nil {
		return nil // task not found — allow (will fail at transition)
	}
	if !goalID.Valid || !phaseOrder.Valid {
		return nil // no Talos phase ordering on this task
	}
	var pending int
	if err := database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM swarm_tasks
		 WHERE goal_id=? AND phase_order < ?
		   AND stage NOT IN ('complete','failed','cancelled','timed_out')`,
		goalID.String, phaseOrder.Int64,
	).Scan(&pending); err != nil {
		return fmt.Errorf("phase order check: %w", err)
	}
	if pending > 0 {
		return fmt.Errorf("phase ordering: %d predecessor phase(s) not yet complete", pending)
	}
	return nil
}

// checkDependencies returns an error if any of the task's depends_on tasks have
// not yet reached a terminal stage. Failed/cancelled/timed_out deps count as
// resolved — the orchestrator retains agency over what happens next.
func checkDependencies(ctx context.Context, taskID string) error {
	var depsJSON string
	if err := database.QueryRowContext(ctx,
		"SELECT COALESCE(depends_on,'') FROM swarm_tasks WHERE id=?", taskID,
	).Scan(&depsJSON); err != nil || depsJSON == "" || depsJSON == "[]" {
		return nil
	}

	var deps []string
	if err := json.Unmarshal([]byte(depsJSON), &deps); err != nil || len(deps) == 0 {
		return nil
	}

	// Build dynamic IN(?,?,?) placeholders — database/sql does not expand slices.
	placeholders := make([]string, len(deps))
	args := make([]interface{}, len(deps))
	for i, d := range deps {
		placeholders[i] = "?"
		args[i] = d
	}
	query := fmt.Sprintf(
		"SELECT COUNT(*) FROM swarm_tasks WHERE id IN (%s) AND stage NOT IN ('complete','failed','cancelled','timed_out')",
		strings.Join(placeholders, ","),
	)
	var pending int
	if err := database.QueryRowContext(ctx, query, args...).Scan(&pending); err != nil {
		return fmt.Errorf("dependency check: %w", err)
	}
	if pending > 0 {
		return fmt.Errorf("task has %d unresolved dependenc%s", pending, map[bool]string{true: "y", false: "ies"}[pending == 1])
	}
	return nil
}

func AcceptTask(ctx context.Context, taskID, messageID string) error {
	if err := checkPhaseOrderConstraint(ctx, taskID); err != nil {
		return err
	}
	if err := checkDependencies(ctx, taskID); err != nil {
		return err
	}
	if err := checkGoalBudget(ctx, taskID); err != nil {
		return err
	}
	if err := checkSessionBudget(ctx, taskID); err != nil {
		return err
	}
	if err := transitionTask(ctx, taskID, "accepted"); err != nil {
		return err
	}
	_, err := database.ExecContext(ctx,
		"UPDATE swarm_tasks SET message_id=?, accepted_at=? WHERE id=?",
		swarmNullStr(messageID), time.Now().Unix(), taskID,
	)
	return err
}

func StartTask(ctx context.Context, taskID string) error {
	if err := transitionTask(ctx, taskID, "running"); err != nil {
		return err
	}
	now := time.Now().Unix()
	_, err := database.ExecContext(ctx,
		"UPDATE swarm_tasks SET started_at=?, last_heartbeat_at=? WHERE id=? AND started_at IS NULL",
		now, now, taskID,
	)
	// Side-effects: per-phase injections
	go maybeInjectPeerReview(context.Background(), taskID)
	go maybeInjectJudge(context.Background(), taskID)
	go maybeInjectRalph(context.Background(), taskID)
	return err
}

func CompleteTask(ctx context.Context, sessionID, agentID, taskID string, h IPCHandoff) error {
	newStage := "complete"
	if h.Confidence < 0.6 {
		newStage = "needs_review"
	}
	if !h.TestsPassed {
		// Tests didn't pass — still complete but flag for review
		if newStage == "complete" {
			newStage = "needs_review"
		}
	}

	if err := transitionTask(ctx, taskID, newStage); err != nil {
		return err
	}

	now := time.Now().Unix()
	database.ExecContext(ctx, //nolint:errcheck
		`UPDATE swarm_tasks SET completed_at=?, confidence=?, tokens_used=?, updated_at=? WHERE id=?`,
		now, h.Confidence, h.TokensUsed, now, taskID,
	)
	// Roll up token spend to goal and session budget trackers (synchronous, O(1) atomic each).
	// Both are called at the same level — neither nests the other.
	rollupGoalBudget(ctx, taskID)
	rollupSessionBudget(ctx, taskID)
	if newStage == "needs_review" {
		database.ExecContext(ctx, //nolint:errcheck
			"UPDATE swarm_tasks SET needs_review_count=needs_review_count+1 WHERE id=?", taskID)
	}

	// Register artifacts
	for _, art := range h.ArtifactsProduced {
		appendArtifact(ctx, sessionID, taskID, agentID, art.Type, art.Path, art.Summary) //nolint:errcheck
	}

	// Create recommended follow-up tasks (queued, unassigned)
	for _, rec := range h.NextRecommendedTasks {
		recID := generateSwarmID()
		database.ExecContext(ctx, //nolint:errcheck
			"INSERT INTO swarm_tasks (id,session_id,title,description,stage,created_at,updated_at) VALUES (?,?,?,?,?,?,?)",
			recID, sessionID, rec.Title, swarmNullStr(rec.Description), "queued", now, now,
		)
	}

	// Escalate if needs_human flag set
	if newStage == "needs_human" || len(h.OpenQuestions) > 0 {
		writeEscalation(sessionID, agentID, taskID, fmt.Sprintf("Open questions: %v", h.OpenQuestions))
	}

	// Trigger goal reconciliation
	go reconcileGoalsForTask(context.Background(), sessionID, taskID)
	// Fire broadcast
	swarmBroadcaster.schedule(sessionID)
	return nil
}

func BlockTask(ctx context.Context, sessionID, agentID, taskID, reason string) error {
	if err := transitionTask(ctx, taskID, "blocked"); err != nil {
		return err
	}
	_, err := database.ExecContext(ctx,
		"UPDATE swarm_tasks SET blocked_reason=?, updated_at=? WHERE id=?",
		reason, time.Now().Unix(), taskID,
	)
	if err == nil {
		writeEscalation(sessionID, agentID, taskID, reason)
		// Also submit a DB-backed escalation to the Telegram hub if the router is active.
		if tr := telegramRouter; tr != nil {
			go func() {
				if _, err := tr.SubmitEscalation(context.Background(), agentID, taskID, reason, 24*time.Hour); err != nil {
					log.Printf("telegram: submit escalation for task %s: %v", truncateID(taskID), err)
				}
			}()
		}
	}
	return err
}

// ─── Escalation ───────────────────────────────────────────────────────────────

func writeEscalation(sessionID, agentID, taskID, reason string) {
	dir := swarmEscalationsDir(sessionID)
	os.MkdirAll(dir, 0755) //nolint:errcheck
	path := filepath.Join(dir, fmt.Sprintf("esc_%d_%s.json", time.Now().UnixNano(), taskID[:8]))
	data, _ := json.Marshal(map[string]string{
		"session_id": sessionID,
		"agent_id":   agentID,
		"task_id":    taskID,
		"reason":     reason,
		"ts":         fmt.Sprintf("%d", time.Now().Unix()),
	})
	os.WriteFile(path, data, 0644) //nolint:errcheck
	log.Printf("swarm: escalation written: %s", path)
}

