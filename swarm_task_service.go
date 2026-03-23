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
	"queued":            {"assigned"},
	"assigned":          {"accepted", "queued"},
	"accepted":          {"running", "blocked", "failed"},
	"running":           {"complete", "blocked", "needs_review", "needs_human", "failed", "timed_out"},
	"blocked":           {"queued", "running", "failed"},
	"needs_review":      {"running", "complete", "failed", "queued", "failed_ralph_loop"},
	"needs_human":       {"running", "complete", "failed"},
	"complete":          {},
	"timed_out":         {},
	"failed":            {},
	"failed_ralph_loop": {}, // terminal — no outbound transitions
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

// AcceptTaskParams carries optional parameters for AcceptTask.
// scope_paths is the list of file/directory prefixes the agent will modify.
// If nil or empty, a broad "/" lease is acquired (safe default for v1).
type AcceptTaskParams struct {
	ScopePaths []string
}

func AcceptTask(ctx context.Context, taskID, messageID string) error {
	return AcceptTaskWithParams(ctx, taskID, messageID, AcceptTaskParams{})
}

func AcceptTaskWithParams(ctx context.Context, taskID, messageID string, params AcceptTaskParams) error {
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

	// H1: Acquire repo lease atomically at AcceptTask time (PR-6: eliminates TOCTOU
	// that would arise from a separate /scope endpoint + AcceptTask round-trip).
	// Scope is declared by the agent in the accept request; empty → broad "/" lease.
	if err := acquireRepoLeaseForTask(ctx, taskID, params.ScopePaths); err != nil {
		return fmt.Errorf("repo lease: %w", err)
	}

	// SWM-14: create a per-task branch from current integrator HEAD and check it
	// out in the agent worktree so each task starts on a clean isolated branch.
	checkoutTaskBranchForTask(ctx, taskID)

	if err := transitionTask(ctx, taskID, "accepted"); err != nil {
		return err
	}
	_, err := database.ExecContext(ctx,
		"UPDATE swarm_tasks SET message_id=?, accepted_at=? WHERE id=?",
		swarmNullStr(messageID), time.Now().Unix(), taskID,
	)
	return err
}

// checkoutTaskBranchForTask looks up the agent's repo/worktree paths and calls
// checkoutTaskBranch. Non-fatal: errors are logged and silently skipped.
func checkoutTaskBranchForTask(ctx context.Context, taskID string) {
	var agentID sql.NullString
	if err := database.QueryRowContext(ctx,
		`SELECT agent_id FROM swarm_tasks WHERE id=?`, taskID,
	).Scan(&agentID); err != nil || !agentID.Valid || agentID.String == "" {
		return
	}

	var repoPath, worktreePath string
	database.QueryRowContext(ctx,
		`SELECT COALESCE(repo_path,''), COALESCE(worktree_path,'') FROM swarm_agents WHERE id=?`,
		agentID.String,
	).Scan(&repoPath, &worktreePath) //nolint:errcheck

	if repoPath == "" || worktreePath == "" {
		return
	}

	if err := checkoutTaskBranch(repoPath, worktreePath, taskID); err != nil {
		log.Printf("swarm: checkoutTaskBranch task %s: %v", shortID(taskID), err)
	}
}

// acquireRepoLeaseForTask looks up the task's session/goal/agent worktree and
// calls acquireRepoLease. No-ops if the task has no associated worktree.
func acquireRepoLeaseForTask(ctx context.Context, taskID string, scopePaths []string) error {
	var sessionID, goalID string
	var agentID sql.NullString
	if err := database.QueryRowContext(ctx,
		`SELECT session_id, COALESCE(goal_id,''), agent_id FROM swarm_tasks WHERE id=?`, taskID,
	).Scan(&sessionID, &goalID, &agentID); err != nil {
		return nil // task not found — let AcceptTask surface the error
	}

	if !agentID.Valid || agentID.String == "" {
		return nil // no agent — scratch task, no worktree lease needed
	}

	// Look up the agent's repo path from its worktree.
	var repoPath string
	database.QueryRowContext(ctx,
		`SELECT COALESCE(repo_path,'') FROM swarm_agents WHERE id=?`, agentID.String,
	).Scan(&repoPath) //nolint:errcheck

	if repoPath == "" {
		return nil // scratch agent — no repo lease needed
	}

	return acquireRepoLease(ctx, database, sessionID, goalID, taskID, repoPath, scopePaths)
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

	// SWM-14: merge the per-task branch into the integrator (main) before
	// transitioning the task state. A merge conflict upgrades the stage to
	// needs_human so the operator can resolve it.
	if mergeErr := mergeTaskBranchForTask(ctx, agentID, taskID); mergeErr != nil {
		if _, isConflict := mergeErr.(ErrMergeConflict); isConflict {
			newStage = "needs_human"
			h.OpenQuestions = append(h.OpenQuestions, "merge conflict: "+mergeErr.Error())
		} else {
			// Non-conflict git error — log and continue; work stays on task branch.
			log.Printf("swarm: mergeTaskBranch task %s: %v", shortID(taskID), mergeErr)
		}
	}

	if err := transitionTask(ctx, taskID, newStage); err != nil {
		return err
	}

	// H1: Release repo lease on terminal/review transition.
	// needs_review is included so the lease is freed for the next retry attempt.
	releaseRepoLease(ctx, database, taskID)

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
	// H1: Release repo lease so other tasks can use the repo while this one is blocked.
	releaseRepoLease(ctx, database, taskID)
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

// ─── SWM-14: merge helper ─────────────────────────────────────────────────────

// mergeTaskBranchForTask looks up the agent's repo/worktree paths and the task
// title, then calls mergeTaskBranch. Used by CompleteTask.
func mergeTaskBranchForTask(ctx context.Context, agentID, taskID string) error {
	var repoPath, worktreePath string
	database.QueryRowContext(ctx,
		`SELECT COALESCE(repo_path,''), COALESCE(worktree_path,'') FROM swarm_agents WHERE id=?`,
		agentID,
	).Scan(&repoPath, &worktreePath) //nolint:errcheck

	if repoPath == "" {
		return nil // scratch agent
	}

	var taskTitle string
	database.QueryRowContext(ctx,
		`SELECT COALESCE(title,'') FROM swarm_tasks WHERE id=?`, taskID,
	).Scan(&taskTitle) //nolint:errcheck

	agentBranchName := swarmBranchName(agentID)
	return mergeTaskBranch(ctx, repoPath, worktreePath, agentBranchName, taskID, taskTitle)
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

