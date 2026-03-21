package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

// ─── Ralph loop ────────────────────────────────────────────────────────────────
//
// Ralph injects:
//   - A criteria reminder on the FIRST start of implement / deploy / document tasks
//   - A retry brief on subsequent starts (after needs_review)
//   - An escalation notice and hard stop after 3 failed review cycles
//
// Called as a goroutine from StartTask.

const ralphMaxRetries = 3

// phasesRequiringRalph are the phases where Ralph injects criteria reminders.
var phasesRequiringRalph = map[string]bool{
	"implement": true,
	"deploy":    true,
	"document":  true,
}

func maybeInjectRalph(ctx context.Context, taskID string) {
	var sessionID, agentID, phase, goalID string
	var needsReviewCount int

	err := database.QueryRowContext(ctx,
		`SELECT session_id, COALESCE(agent_id,''), COALESCE(phase,''), needs_review_count,
		        COALESCE(goal_id,'')
		 FROM swarm_tasks WHERE id=?`, taskID,
	).Scan(&sessionID, &agentID, &phase, &needsReviewCount, &goalID)
	if err != nil || agentID == "" {
		return
	}

	// Only run for implement / deploy / document phases
	if !phasesRequiringRalph[phase] {
		return
	}

	switch {
	case needsReviewCount >= ralphMaxRetries:
		// Server-enforced terminal transition — no longer relies on agent cooperation.
		// Both task and goal are updated in a single transaction (PR-4 correction).
		termReason := fmt.Sprintf("Ralph loop exhausted after %d attempts on %s phase", ralphMaxRetries, phase)
		if err := ralphTerminateTask(ctx, sessionID, taskID, goalID, phase, termReason, needsReviewCount); err != nil {
			log.Printf("swarm/ralph: terminate failed task=%s: %v", taskID[:8], err)
		}

	case needsReviewCount > 0:
		// Retry brief — task was returned for review
		attemptNum := needsReviewCount + 1
		prompt := fmt.Sprintf(`## 🔄 Ralph Retry Check-In — %s phase (attempt %d/%d)

This task was returned for review **%d time(s)**. Before resuming work:

1. **Re-read the acceptance criteria** — check the blackboard (goals.md / context.md)
2. **Identify exactly which criteria failed** in your previous attempt
3. **Make a targeted plan** to address only the gaps — don't start from scratch

⚠️ Warning: after %d total failed review cycles this task will be escalated to a human.

Complete this task only when ALL acceptance criteria pass and confidence ≥ 0.8.`,
			phase, attemptNum, ralphMaxRetries+1, needsReviewCount, ralphMaxRetries)
		if err := injectToSwarmAgent(ctx, agentID, prompt); err != nil {
			log.Printf("swarm/ralph: inject retry failed task=%s: %v", taskID[:8], err)
		}

	default:
		// First start — inject acceptance criteria reminder
		prompt := fmt.Sprintf(`## ✅ Ralph Check-In — %s phase

You are starting the **%s phase**. Before you begin, confirm your approach against the acceptance criteria.

**Step 1 — Read the spec:**
Check the blackboard for the acceptance criteria written in the spec phase.
Primary locations: `+"`{blackboard}/goals.md`"+`, `+"`{blackboard}/context.md`"+`

**Step 2 — Check the plan:**
Read `+"`{blackboard}/decisions.md`"+` for the reviewed implementation plan.

**Step 3 — Trace coverage:**
Confirm every acceptance criterion has a corresponding step in your plan.

Do not mark this task complete until ALL acceptance criteria pass.
Use confidence ≥ 0.8 only when every criterion is demonstrably satisfied.`,
			phase, phase)
		if err := injectToSwarmAgent(ctx, agentID, prompt); err != nil {
			log.Printf("swarm/ralph: inject failed task=%s phase=%s: %v", taskID[:8], phase, err)
		}
	}
}

// ralphTerminateTask transitions the task to failed_ralph_loop and the goal to needs_human
// in a single transaction. Sends a structured Telegram escalation package.
func ralphTerminateTask(ctx context.Context, sessionID, taskID, goalID, phase, reason string, attempts int) error {
	now := time.Now().Unix()

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx,
		`UPDATE swarm_tasks SET stage='failed_ralph_loop', terminal_reason=?, updated_at=?, stage_changed_at=?
		 WHERE id=? AND stage NOT IN ('complete','cancelled','failed','timed_out','failed_ralph_loop')`,
		reason, now, now, taskID,
	); err != nil {
		return fmt.Errorf("update task: %w", err)
	}

	if goalID != "" {
		goalReason := fmt.Sprintf("Ralph loop exhausted on task %s (phase %s)", taskID[:8], phase)
		if _, err := tx.ExecContext(ctx,
			`UPDATE swarm_goals SET status='needs_human', terminal_reason=?, updated_at=?
			 WHERE id=? AND status='active'`,
			goalReason, now, goalID,
		); err != nil {
			return fmt.Errorf("update goal: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	writeSwarmEvent(ctx, sessionID, "", taskID, "ralph_loop_exhausted", reason)
	swarmBroadcaster.schedule(sessionID)
	log.Printf("swarm/ralph: task=%s goal=%s terminated — %s", taskID[:8], goalID[:8], reason)

	// Build and send escalation package asynchronously.
	go sendRalphEscalation(context.Background(), sessionID, taskID, goalID, phase, attempts)
	return nil
}

// ralphEscalationPackage holds the context assembled for a ralph_loop Telegram escalation.
type ralphEscalationPackage struct {
	sessionID  string
	taskID     string
	goalID     string
	phase      string
	attempts   int
	attemptLog []string // blocked_reason history
	branchName string
	judgeNotes string
	goalDesc   string
}

// buildRalphEscalationPackage queries the DB for context to include in the escalation message.
func buildRalphEscalationPackage(ctx context.Context, sessionID, taskID, goalID, phase string, attempts int) ralphEscalationPackage {
	pkg := ralphEscalationPackage{
		sessionID: sessionID,
		taskID:    taskID,
		goalID:    goalID,
		phase:     phase,
		attempts:  attempts,
	}

	// Collect blocked_reason history from the task (up to 3 most recent).
	rows, _ := database.QueryContext(ctx,
		`SELECT COALESCE(blocked_reason,'(no reason recorded)')
		 FROM swarm_tasks WHERE id=?`, taskID)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var reason string
			rows.Scan(&reason) //nolint:errcheck
			pkg.attemptLog = append(pkg.attemptLog, reason)
		}
	}

	// Branch name from agent worktree.
	var agentID string
	database.QueryRowContext(ctx, "SELECT COALESCE(agent_id,'') FROM swarm_tasks WHERE id=?", taskID).Scan(&agentID) //nolint:errcheck
	if agentID != "" {
		pkg.branchName = swarmBranchName(agentID)
	}

	// Judge notes from most recent judge task in same goal.
	if goalID != "" {
		var notes sql.NullString
		database.QueryRowContext(ctx,
			`SELECT judge_notes FROM swarm_goals WHERE id=?`, goalID,
		).Scan(&notes) //nolint:errcheck
		if notes.Valid {
			pkg.judgeNotes = notes.String
		}

		// Goal description
		database.QueryRowContext(ctx,
			`SELECT COALESCE(description,'') FROM swarm_goals WHERE id=?`, goalID,
		).Scan(&pkg.goalDesc) //nolint:errcheck
	}

	return pkg
}

// sendRalphEscalation assembles the escalation package and sends it to Telegram.
// Uses SubmitEscalation so the reply can be routed back to the goal.
func sendRalphEscalation(ctx context.Context, sessionID, taskID, goalID, phase string, attempts int) {
	pkg := buildRalphEscalationPackage(ctx, sessionID, taskID, goalID, phase, attempts)

	var attemptLines strings.Builder
	for i, line := range pkg.attemptLog {
		fmt.Fprintf(&attemptLines, "  %d. %s\n", i+1, line)
	}

	hypothesis := "(no judge notes)"
	if pkg.judgeNotes != "" {
		hypothesis = pkg.judgeNotes
	}

	msg := fmt.Sprintf(`⛔ *[SWARM ESCALATION]* Goal: %s
Phase: *%s* — Ralph loop exhausted after %d attempts

%s
*Attempts:*
%s
*Branch:* `+"`%s`"+` (run: `+"`git diff main...HEAD`"+`)
*Hypothesis:* %s

*Reply with one of:*
  `+"`1 <guidance>`"+` — Retry with your guidance
  `+"`2`"+` — Reassign to different agent
  `+"`3`"+` — Cancel this goal
  `+"`4`"+` — Defer (leave as needs_human)`,
		goalID[:8],
		phase, attempts,
		func() string {
			if pkg.goalDesc != "" {
				return "_" + pkg.goalDesc + "_\n\n"
			}
			return ""
		}(),
		attemptLines.String(),
		pkg.branchName,
		hypothesis,
	)

	tr := telegramRouter
	if tr == nil {
		// No router — fall back to simple notification
		sendTelegramNotification(msg)
		return
	}

	// Use SubmitEscalation so a reply can be correlated back to this goal.
	// We use a synthetic agentID of the orchestrator for routing.
	var orchID string
	database.QueryRowContext(ctx,
		`SELECT id FROM swarm_agents WHERE session_id=? AND role='orchestrator' AND tmux_session IS NOT NULL LIMIT 1`,
		sessionID,
	).Scan(&orchID) //nolint:errcheck
	if orchID == "" {
		sendTelegramNotification(msg)
		return
	}

	escID, err := tr.SubmitRalphEscalation(ctx, orchID, taskID, goalID, msg, 72*time.Hour)
	if err != nil {
		log.Printf("swarm/ralph: submit escalation failed: %v", err)
		sendTelegramNotification(msg) // fallback
		return
	}
	log.Printf("swarm/ralph: escalation sent esc=%s goal=%s", escID[:8], goalID[:8])
}
