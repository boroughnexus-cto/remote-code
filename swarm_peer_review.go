package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
)

// ─── Peer-review injection ────────────────────────────────────────────────────
//
// When a plan_review (phase_order=3) or impl_review (phase_order=5) task starts
// running, we inject a structured brief to the assigned agent instructing them
// to run a formal mcp-aipeer review and gate on the findings.
//
// Idempotent: peer_review_injected column is set to 1 on first injection;
// subsequent calls to StartTask for the same task are no-ops.

// maybeInjectPeerReview is called (as a goroutine) from StartTask.
func maybeInjectPeerReview(ctx context.Context, taskID string) {
	var phase sql.NullString
	var phaseOrder sql.NullInt64
	var goalID sql.NullString
	var agentID sql.NullString
	var sessionID string
	var alreadyInjected int
	err := database.QueryRowContext(ctx,
		`SELECT session_id, COALESCE(phase,''), phase_order, COALESCE(goal_id,''),
		        COALESCE(agent_id,''), COALESCE(peer_review_injected,0)
		 FROM swarm_tasks WHERE id=?`, taskID,
	).Scan(&sessionID, &phase, &phaseOrder, &goalID, &agentID, &alreadyInjected)
	if err != nil || agentID.String == "" {
		return
	}
	if phase.String != "plan_review" && phase.String != "impl_review" {
		return
	}
	if alreadyInjected != 0 {
		return // idempotent
	}

	// Mark injected before doing IO so a concurrent call doesn't double-inject.
	if _, err := database.ExecContext(ctx,
		"UPDATE swarm_tasks SET peer_review_injected=1 WHERE id=? AND peer_review_injected=0",
		taskID,
	); err != nil {
		return
	}

	// Get goal description.
	var goalDesc string
	database.QueryRowContext(ctx, //nolint:errcheck
		"SELECT description FROM swarm_goals WHERE id=?", goalID.String,
	).Scan(&goalDesc)

	// Get the previous phase task's description (the artifact being reviewed).
	// plan_review (phase_order=3) reviews the plan (phase_order=2).
	// impl_review (phase_order=5) reviews the implementation (phase_order=4).
	var prevDesc string
	if phaseOrder.Valid {
		database.QueryRowContext(ctx, //nolint:errcheck
			`SELECT COALESCE(description,'') FROM swarm_tasks
			 WHERE goal_id=? AND phase_order=? ORDER BY created_at ASC LIMIT 1`,
			goalID.String, phaseOrder.Int64-1,
		).Scan(&prevDesc)
	}

	bbDir := swarmBlackboardDir(sessionID)
	reviewType := "architecture"
	artifactInstructions := fmt.Sprintf("Read the plan written in the plan phase. Primary location: `%s/decisions.md` — look for the plan section. Also check `%s/context.md`.", bbDir, bbDir)
	if phase.String == "impl_review" {
		reviewType = "general"
		artifactInstructions = "Get the code changes: run `git diff main...HEAD` (or the feature branch diff). Also note any relevant files listed in the implement task description below."
	}

	apiBase := swarmAPIBase()
	prompt := fmt.Sprintf(`## Peer Review Gate — %s (goal %s)

Goal: %s

**You are in the peer review phase.** Your job is to run a formal review and gate this phase on the findings.

**Step 1 — Get the artifact to review:**
%s

Previous phase task description (for context):
~~~
%s
~~~

**Step 2 — Run mcp-aipeer:**
Use the peer_review tool (via MCP) with:
  content: <the full artifact text — plan document or git diff>
  review_type: "%s"

**Step 3 — Record findings:**
Append a new section to: %s/decisions.md

Head the section: "## Peer Review: %s [task %s]"
Include: severity of findings, list of issues, your gate decision.

**Step 4 — Gate (REQUIRED before marking complete):**
- **CRITICAL or HIGH findings** found:
  1. Do NOT complete this task yet.
  2. Update the artifact to address the findings.
  3. Move this task back to needs_review so it can be re-triggered:
     PATCH %s/api/swarm/sessions/%s/tasks/%s
     {"stage": "needs_review"}
- **Only MEDIUM/LOW findings (or clean):**
  Mark this task complete:
  POST %s/api/swarm/sessions/%s/tasks/%s/complete
  {"confidence": 0.85, "tests_passed": true,
   "summary": "Peer review passed. Findings: <brief summary>"}
`,
		phase.String, goalID.String[:8],
		goalDesc,
		artifactInstructions,
		prevDesc,
		reviewType,
		bbDir, phase.String, taskID[:8],
		apiBase, sessionID, taskID,
		apiBase, sessionID, taskID,
	)

	if err := injectToSwarmAgent(ctx, agentID.String, prompt); err != nil {
		log.Printf("swarm/peer-review: inject failed task=%s: %v", taskID[:8], err)
	}
}
