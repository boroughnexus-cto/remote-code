package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
)

// ─── Acceptance Criteria Judge ────────────────────────────────────────────────
//
// The judge phase (phase_order=6 in 8-phase Talos) verifies that the
// implementation meets the acceptance criteria written in the spec phase.
//
// When the judge task starts, we inject a structured prompt instructing the
// agent to:
//   1. Read acceptance criteria from the blackboard (goals.md, context.md)
//   2. Get the implementation diff (git diff main...HEAD)
//   3. Run mcp-aipeer peer_review with review_type="test"
//   4. Gate: CRITICAL/HIGH failures → complete with low confidence (→ needs_review)
//      All pass → complete with confidence ≥ 0.85
//
// If the judge task goes to needs_review (confidence < 0.6), SiBot is briefed
// and will re-assign the implement task for rework. The phase ordering constraint
// ensures deploy cannot start until judge reaches a terminal pass.
//
// Idempotent: judge_injected column is set to 1 on first injection.

// maybeInjectJudge is called (as a goroutine) from StartTask.
func maybeInjectJudge(ctx context.Context, taskID string) {
	var phase sql.NullString
	var phaseOrder sql.NullInt64
	var goalID sql.NullString
	var agentID sql.NullString
	var sessionID string
	var alreadyInjected int

	err := database.QueryRowContext(ctx,
		`SELECT session_id, COALESCE(phase,''), phase_order, COALESCE(goal_id,''),
		        COALESCE(agent_id,''), COALESCE(judge_injected,0)
		 FROM swarm_tasks WHERE id=?`, taskID,
	).Scan(&sessionID, &phase, &phaseOrder, &goalID, &agentID, &alreadyInjected)
	if err != nil || agentID.String == "" {
		return
	}
	if phase.String != "judge" {
		return
	}
	if alreadyInjected != 0 {
		return // idempotent
	}

	// Mark injected before doing IO so a concurrent call doesn't double-inject.
	if _, err := database.ExecContext(ctx,
		"UPDATE swarm_tasks SET judge_injected=1 WHERE id=? AND judge_injected=0", taskID,
	); err != nil {
		return
	}

	// Get goal description.
	var goalDesc string
	database.QueryRowContext(ctx, //nolint:errcheck
		"SELECT description FROM swarm_goals WHERE id=?", goalID.String,
	).Scan(&goalDesc)

	// Get the spec task description (phase_order=1) — contains the acceptance criteria.
	var specDesc string
	database.QueryRowContext(ctx, //nolint:errcheck
		`SELECT COALESCE(description,'') FROM swarm_tasks
		 WHERE goal_id=? AND phase_order=1 ORDER BY created_at ASC LIMIT 1`,
		goalID.String,
	).Scan(&specDesc)

	bbDir := swarmBlackboardDir(sessionID)
	apiBase := swarmAPIBase()

	prompt := fmt.Sprintf(`## Acceptance Criteria Judge — goal %s

Goal: %s

**You are in the judge phase.** Your sole job is to verify the implementation meets
the acceptance criteria written during the spec phase. You do NOT write any code here.

**Step 1 — Read the acceptance criteria:**
Check the blackboard for the spec written in the spec phase.
Primary locations:
  - %s/goals.md
  - %s/context.md

Spec phase task description (for context):
~~~
%s
~~~

**Step 2 — Get the implementation diff:**
Run: git diff main...HEAD
(Or the feature branch diff — whatever contains the work from the implement phase.)

**Step 3 — Deterministic checks (do these first):**
- Does the code compile? (run the build command for this repo)
- Do existing tests pass? (run the test suite)
- Record results.

**Step 4 — Run mcp-aipeer acceptance review:**
Use the peer_review tool (via MCP) with:
  content: <full diff from Step 2, prefixed with the acceptance criteria>
  review_type: "test"

Prefix the content with the criteria so the reviewer knows what to check:

    ## Acceptance Criteria to verify:
    <criteria from Step 1>

    ## Implementation diff:
    <git diff output>

**Step 5 — Evaluate each criterion individually:**
For each acceptance criterion from the spec, determine:
  - PASS: the diff clearly implements it
  - PARTIAL: partially implemented, missing edge cases
  - FAIL: not implemented or contradicts the criterion

Produce a structured result (write this to the blackboard in Step 6):

    {
      "verdict": "PASS" or "FAIL",
      "build_ok": true/false,
      "tests_ok": true/false,
      "criteria": [
        {"criterion": "...", "status": "pass|partial|fail", "reason": "..."}
      ],
      "blocking_issues": ["..."],
      "reasoning": "..."
    }

**Step 6 — Write judge notes to blackboard:**
Append to: %s/decisions.md

Head the section: "## Acceptance Criteria Judge [task %s]"
Include the structured JSON result from Step 5 plus your overall reasoning.

**Step 7 — Gate (REQUIRED):**

If ALL criteria PASS (or only PARTIAL with no blocking issues) AND build+tests ok:
  POST %s/api/swarm/sessions/%s/tasks/%s/complete
  {"confidence": 0.90, "tests_passed": true,
   "summary": "Judge PASSED. All acceptance criteria verified. <brief evidence summary>"}

If ANY criterion FAILS, OR build/tests fail, OR blocking issues exist:
  Write the specific failures to the blackboard first.
  Complete with LOW confidence so it goes to needs_review:
  POST %s/api/swarm/sessions/%s/tasks/%s/complete
  {"confidence": 0.40, "tests_passed": false,
   "summary": "Judge FAILED. Criteria not met: <list specifically>. Build ok: <y/n>. Tests ok: <y/n>."}

SiBot will see the needs_review state and direct the implement phase to be reworked.
Do not attempt to fix the code yourself — that is the implement phase's job.
`,
		goalID.String[:8],
		goalDesc,
		bbDir, bbDir,
		specDesc,
		bbDir, taskID[:8],
		apiBase, sessionID, taskID,
		apiBase, sessionID, taskID,
	)

	if err := injectToSwarmAgent(ctx, agentID.String, prompt); err != nil {
		log.Printf("swarm/judge: inject failed task=%s: %v", taskID[:8], err)
	}
}
