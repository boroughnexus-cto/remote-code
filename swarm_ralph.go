package main

import (
	"context"
	"fmt"
	"log"
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
	var sessionID, agentID, phase string
	var needsReviewCount int

	err := database.QueryRowContext(ctx,
		`SELECT session_id, COALESCE(agent_id,''), COALESCE(phase,''), needs_review_count
		 FROM swarm_tasks WHERE id=?`, taskID,
	).Scan(&sessionID, &agentID, &phase, &needsReviewCount)
	if err != nil || agentID == "" {
		return
	}

	// Only run for implement / deploy / document phases
	if !phasesRequiringRalph[phase] {
		return
	}

	var prompt string

	switch {
	case needsReviewCount >= ralphMaxRetries:
		// Hard stop — too many failures
		prompt = fmt.Sprintf(`## ⛔ Ralph Escalation — %s phase (%d attempts exhausted)

This task has failed review **%d times**. Do NOT attempt further work.

**Action required:**
1. Block this task immediately:
   `+"`task_block`"+` with reason: "Ralph loop exhausted after %d attempts — human review required"
2. Write a summary of what was attempted and why it kept failing to the blackboard.

A human will review and decide next steps.`,
			phase, needsReviewCount, needsReviewCount, ralphMaxRetries)

	case needsReviewCount > 0:
		// Retry brief — task was returned for review
		attemptNum := needsReviewCount + 1
		prompt = fmt.Sprintf(`## 🔄 Ralph Retry Check-In — %s phase (attempt %d/%d)

This task was returned for review **%d time(s)**. Before resuming work:

1. **Re-read the acceptance criteria** — check the blackboard (goals.md / context.md)
2. **Identify exactly which criteria failed** in your previous attempt
3. **Make a targeted plan** to address only the gaps — don't start from scratch

⚠️ Warning: after %d total failed review cycles this task will be escalated to a human.

Complete this task only when ALL acceptance criteria pass and confidence ≥ 0.8.`,
			phase, attemptNum, ralphMaxRetries+1, needsReviewCount, ralphMaxRetries)

	default:
		// First start — inject acceptance criteria reminder
		prompt = fmt.Sprintf(`## ✅ Ralph Check-In — %s phase

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
	}

	if err := injectToSwarmAgent(ctx, agentID, prompt); err != nil {
		log.Printf("swarm/ralph: inject failed task=%s phase=%s: %v", taskID[:8], phase, err)
	}
}
