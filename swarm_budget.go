package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

// ─── Goal Token Budget ─────────────────────────────────────────────────────────
//
// Per-goal token budget. When token_budget > 0 on a goal:
//   - tokens_used is atomically incremented when a task completes (with tokens_used set)
//   - At 80%: warning injected to SiBot once (budget_warning_sent guard)
//   - At 100%: new tasks cannot be accepted; SiBot is briefed to decide next steps
//
// Enforcement is at AcceptTask() time, not by mass-cancellation, so in-flight tasks
// complete and the orchestrator retains agency over what to do next.

const budgetWarnThreshold = 0.80

// rollupGoalBudget atomically increments goal.tokens_used by the tokens consumed
// by task taskID, then fires warnings/enforcement if thresholds are crossed.
// Called synchronously from CompleteTask — SQLite serialises the write.
func rollupGoalBudget(ctx context.Context, taskID string) {
	// Look up the task's goal and token count
	var goalID string
	var tokensUsed int64
	err := database.QueryRowContext(ctx,
		`SELECT COALESCE(goal_id,''), COALESCE(tokens_used,0) FROM swarm_tasks WHERE id=?`, taskID,
	).Scan(&goalID, &tokensUsed)
	if err != nil || goalID == "" || tokensUsed == 0 {
		return // no goal or no tokens recorded
	}

	// Atomic increment
	if _, err := database.ExecContext(ctx,
		"UPDATE swarm_goals SET tokens_used=tokens_used+?, updated_at=? WHERE id=?",
		tokensUsed, time.Now().Unix(), goalID,
	); err != nil {
		log.Printf("swarm/budget: rollup failed goal=%s: %v", goalID[:8], err)
		return
	}

	// Read back new total and budget
	var budget, total int64
	var warningSent int
	var sessionID string
	if err := database.QueryRowContext(ctx,
		"SELECT session_id, token_budget, tokens_used, budget_warning_sent FROM swarm_goals WHERE id=?", goalID,
	).Scan(&sessionID, &budget, &total, &warningSent); err != nil {
		return
	}

	if budget <= 0 {
		return // unlimited
	}

	pct := float64(total) / float64(budget)
	log.Printf("swarm/budget: goal=%s tokens=%d/%d (%.0f%%)", goalID[:8], total, budget, pct*100)

	if pct >= 1.0 {
		// Over budget — brief orchestrator; enforcement is in AcceptTask.
		// Try auto-raise if SWARM_BUDGET_AUTORAISE_PCT > 0 and ceiling allows.
		if raised := tryAutoRaiseGoalBudget(ctx, goalID, budget); raised > 0 {
			log.Printf("swarm/budget: goal=%s auto-raised budget %d→%d", goalID[:8], budget, raised)
			return
		}
		writeSwarmEvent(ctx, sessionID, "", "", "budget_exceeded",
			fmt.Sprintf("goal=%s used=%d budget=%d", goalID[:8], total, budget))
		swarmBroadcaster.schedule(sessionID)
		go injectBudgetNotice(ctx, sessionID, goalID, total, budget, true)
		sendLocalNotification("⛔ SwarmOps: budget exhausted", fmt.Sprintf("Goal %s — %d/%d tokens", goalID[:8], total, budget))
	} else if pct >= budgetWarnThreshold && warningSent == 0 {
		// 80% warning — once only; checkpoint all running workers, not just orchestrator
		database.ExecContext(ctx, //nolint:errcheck
			"UPDATE swarm_goals SET budget_warning_sent=1 WHERE id=?", goalID)
		writeSwarmEvent(ctx, sessionID, "", "", "budget_warning",
			fmt.Sprintf("goal=%s at %.0f%% of budget", goalID[:8], pct*100))
		go injectBudgetNotice(ctx, sessionID, goalID, total, budget, false)
		go injectBudgetCheckpoint(ctx, sessionID, goalID, pct)
	}
}

// checkGoalBudget returns an error if the goal associated with taskID is over budget.
// Called from AcceptTask to block new work when budget is exhausted.
func checkGoalBudget(ctx context.Context, taskID string) error {
	var goalID string
	if err := database.QueryRowContext(ctx,
		"SELECT COALESCE(goal_id,'') FROM swarm_tasks WHERE id=?", taskID,
	).Scan(&goalID); err != nil || goalID == "" {
		return nil // no goal — unlimited
	}

	var budget, used int64
	if err := database.QueryRowContext(ctx,
		"SELECT token_budget, tokens_used FROM swarm_goals WHERE id=?", goalID,
	).Scan(&budget, &used); err != nil || budget <= 0 {
		return nil // goal not found or unlimited
	}

	if used >= budget {
		return fmt.Errorf("goal token budget exhausted (%d/%d tokens used)", used, budget)
	}
	return nil
}

// injectBudgetNotice briefs the orchestrator about budget status.
func injectBudgetNotice(ctx context.Context, sessionID, goalID string, used, budget int64, exceeded bool) {
	var agentID string
	if err := database.QueryRowContext(ctx,
		`SELECT id FROM swarm_agents WHERE session_id=? AND role='orchestrator' AND tmux_session IS NOT NULL LIMIT 1`,
		sessionID,
	).Scan(&agentID); err != nil {
		return
	}

	pct := float64(used) / float64(budget) * 100

	var prompt string
	if exceeded {
		prompt = fmt.Sprintf(`## ⛔ Budget Exceeded — goal %s

Token budget **exhausted** (used=%d, budget=%d, %.0f%%).

No new tasks for this goal can be accepted until the budget is raised.

**Your options:**
1. Raise the budget via PATCH /api/swarm/sessions/{sessionID}/goals/%s/budget {"token_budget": <new_limit>}
2. Accept the partial work: mark remaining queued tasks cancelled
3. Let the in-progress tasks finish, then evaluate

Queued tasks are NOT cancelled automatically — you retain full agency.`,
			goalID[:8], used, budget, pct, goalID)
	} else {
		prompt = fmt.Sprintf(`## ⚠️ Budget Warning — goal %s

Token budget at **%.0f%%** (used=%d of budget=%d).

Remaining budget may not be sufficient to complete all phases. Consider:
- Noting this in decisions.md
- Keeping implementation tight and focused
- Raising the budget if needed via PATCH .../goals/%s/budget`,
			goalID[:8], pct, used, budget, goalID)
	}

	if err := injectToSwarmAgent(ctx, agentID, prompt); err != nil {
		log.Printf("swarm/budget: inject notice failed: %v", err)
	}
}

// injectBudgetCheckpoint sends a WIP checkpoint instruction to ALL running worker
// agents on a goal (not just the orchestrator). Called at 80% budget.
// Each agent is told to commit any in-progress work so a continuation agent can
// pick up from a known state if the budget exhausts mid-task.
func injectBudgetCheckpoint(ctx context.Context, sessionID, goalID string, pct float64) {
	rows, err := database.QueryContext(ctx,
		`SELECT DISTINCT a.id
		 FROM swarm_agents a
		 JOIN swarm_tasks t ON t.agent_id = a.id
		 WHERE a.session_id = ?
		   AND t.goal_id = ?
		   AND t.stage IN ('running','accepted')
		   AND a.tmux_session IS NOT NULL`,
		sessionID, goalID,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	checkpoint := fmt.Sprintf(`## ⚠️ Budget Checkpoint Required (%.0f%%)

Goal token budget is at **%.0f%%**. Commit any in-progress work now, even if incomplete:

`+"```"+`
git add -A
git commit -m "WIP: checkpoint at budget %.0f%% — [brief state description]"
`+"```"+`

Continue working. If the budget exhausts, a continuation agent will pick up from this commit.
Do NOT stop work — just save your progress first.`, pct*100, pct*100, pct*100)

	for rows.Next() {
		var agentID string
		if rows.Scan(&agentID) == nil {
			if err := injectToSwarmAgent(ctx, agentID, checkpoint); err != nil {
				log.Printf("swarm/budget: checkpoint inject failed agent=%s: %v", agentID[:8], err)
			}
		}
	}
}

// tryAutoRaiseGoalBudget checks SWARM_BUDGET_AUTORAISE_PCT and SWARM_BUDGET_MAX_TOTAL.
// If configured, raises goal budget by the configured percentage (subject to ceiling).
// Returns the new budget value, or 0 if auto-raise is disabled or ceiling is exceeded.
func tryAutoRaiseGoalBudget(ctx context.Context, goalID string, currentBudget int64) int64 {
	pctStr := os.Getenv("SWARM_BUDGET_AUTORAISE_PCT")
	if pctStr == "" || pctStr == "0" {
		return 0
	}
	raisePct, err := strconv.ParseFloat(pctStr, 64)
	if err != nil || raisePct <= 0 {
		return 0
	}

	maxTotal := int64(0)
	if maxStr := os.Getenv("SWARM_BUDGET_MAX_TOTAL"); maxStr != "" {
		if v, err := strconv.ParseInt(maxStr, 10, 64); err == nil {
			maxTotal = v
		}
	}

	newBudget := int64(float64(currentBudget) * (1 + raisePct/100))
	if maxTotal > 0 && newBudget > maxTotal {
		log.Printf("swarm/budget: goal=%s auto-raise would exceed SWARM_BUDGET_MAX_TOTAL (%d) — blocked", goalID[:8], maxTotal)
		return 0
	}

	if _, err := database.ExecContext(ctx,
		"UPDATE swarm_goals SET token_budget=?, budget_warning_sent=0, updated_at=? WHERE id=?",
		newBudget, time.Now().Unix(), goalID,
	); err != nil {
		log.Printf("swarm/budget: auto-raise failed goal=%s: %v", goalID[:8], err)
		return 0
	}
	return newBudget
}

// ─── Session Token Budget (SWM-8) ─────────────────────────────────────────────
//
// Session-level budget mirrors the per-goal budget pattern.  When token_budget>0:
//   - tokens_used incremented on task completion
//   - At 80%: one-shot warning to orchestrator
//   - At 100%: new tasks cannot be accepted; orchestrator is briefed
//
// Both rollups (goal + session) are called from CompleteTask at the same level
// so neither nests the other — avoids double-rollup or ordering surprises.

// rollupSessionBudget atomically increments session.tokens_used by the tokens
// consumed by task taskID, then fires warnings/enforcement if thresholds are crossed.
func rollupSessionBudget(ctx context.Context, taskID string) {
	var sessionID string
	var tokensUsed int64
	if err := database.QueryRowContext(ctx,
		"SELECT COALESCE(session_id,''), COALESCE(tokens_used,0) FROM swarm_tasks WHERE id=?", taskID,
	).Scan(&sessionID, &tokensUsed); err != nil || sessionID == "" || tokensUsed == 0 {
		return
	}

	if _, err := database.ExecContext(ctx,
		"UPDATE swarm_sessions SET tokens_used=tokens_used+?, updated_at=? WHERE id=?",
		tokensUsed, time.Now().Unix(), sessionID,
	); err != nil {
		log.Printf("swarm/budget: session rollup failed session=%s: %v", sessionID[:8], err)
		return
	}

	var budget, total int64
	var warningSent int
	if err := database.QueryRowContext(ctx,
		"SELECT token_budget, tokens_used, budget_warning_sent FROM swarm_sessions WHERE id=?", sessionID,
	).Scan(&budget, &total, &warningSent); err != nil {
		return
	}
	if budget <= 0 {
		return
	}

	pct := float64(total) / float64(budget)
	log.Printf("swarm/budget: session=%s tokens=%d/%d (%.0f%%)", sessionID[:8], total, budget, pct*100)

	if pct >= 1.0 {
		writeSwarmEvent(ctx, sessionID, "", "", "session_budget_exceeded",
			fmt.Sprintf("session=%s used=%d budget=%d", sessionID[:8], total, budget))
		swarmBroadcaster.schedule(sessionID)
		go injectSessionBudgetNotice(ctx, sessionID, total, budget, true)
	} else if pct >= budgetWarnThreshold && warningSent == 0 {
		database.ExecContext(ctx, //nolint:errcheck
			"UPDATE swarm_sessions SET budget_warning_sent=1 WHERE id=?", sessionID)
		writeSwarmEvent(ctx, sessionID, "", "", "session_budget_warning",
			fmt.Sprintf("session=%s at %.0f%% of budget", sessionID[:8], pct*100))
		go injectSessionBudgetNotice(ctx, sessionID, total, budget, false)
	}
}

// checkSessionBudget returns an error if the session token budget is exhausted.
// Called from AcceptTask to block new work when session budget is exhausted.
func checkSessionBudget(ctx context.Context, taskID string) error {
	var sessionID string
	if err := database.QueryRowContext(ctx,
		"SELECT COALESCE(session_id,'') FROM swarm_tasks WHERE id=?", taskID,
	).Scan(&sessionID); err != nil || sessionID == "" {
		return nil
	}

	var budget, used int64
	if err := database.QueryRowContext(ctx,
		"SELECT token_budget, tokens_used FROM swarm_sessions WHERE id=?", sessionID,
	).Scan(&budget, &used); err != nil || budget <= 0 {
		return nil
	}

	if used >= budget {
		return fmt.Errorf("session token budget exhausted (%d/%d tokens used)", used, budget)
	}
	return nil
}

// injectSessionBudgetNotice briefs the orchestrator about session budget status.
func injectSessionBudgetNotice(ctx context.Context, sessionID string, used, budget int64, exceeded bool) {
	var agentID string
	if err := database.QueryRowContext(ctx,
		`SELECT id FROM swarm_agents WHERE session_id=? AND role='orchestrator' AND tmux_session IS NOT NULL LIMIT 1`,
		sessionID,
	).Scan(&agentID); err != nil {
		return
	}

	pct := float64(used) / float64(budget) * 100

	var prompt string
	if exceeded {
		prompt = fmt.Sprintf(`## ⛔ Session Budget Exceeded

Session token budget **exhausted** (used=%d, budget=%d, %.0f%%).

No new tasks in this session can be accepted until the budget is raised.

**Your options:**
1. Raise the budget via PATCH /api/swarm/sessions/%s/budget {"token_budget": <new_limit>}
2. Accept the partial work: mark remaining queued tasks cancelled
3. Let in-progress tasks finish, then evaluate

Queued tasks are NOT cancelled automatically — you retain full agency.`,
			used, budget, pct, sessionID)
	} else {
		prompt = fmt.Sprintf(`## ⚠️ Session Budget Warning

Session token budget at **%.0f%%** (used=%d of budget=%d).

Remaining budget may not be sufficient to complete all tasks. Consider:
- Keeping implementations tight and focused
- Raising the budget if needed via PATCH /api/swarm/sessions/%s/budget`,
			pct, used, budget, sessionID)
	}

	if err := injectToSwarmAgent(ctx, agentID, prompt); err != nil {
		log.Printf("swarm/budget: inject session notice failed: %v", err)
	}
}

// ─── Budget API ───────────────────────────────────────────────────────────────

// handleSwarmGoalBudgetAPI handles PATCH /api/swarm/sessions/:id/goals/:gid/budget
func handleSwarmGoalBudgetAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, sessionID, goalID string) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPatch {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		TokenBudget int64 `json:"token_budget"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TokenBudget < 0 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "token_budget must be a non-negative integer"}) //nolint:errcheck
		return
	}

	if _, err := database.ExecContext(ctx,
		"UPDATE swarm_goals SET token_budget=?, updated_at=? WHERE id=? AND session_id=?",
		req.TokenBudget, time.Now().Unix(), goalID, sessionID,
	); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
		return
	}

	writeSwarmEvent(ctx, sessionID, "", "", "budget_set",
		fmt.Sprintf("goal=%s budget=%d", goalID[:8], req.TokenBudget))
	swarmBroadcaster.schedule(sessionID)
	log.Printf("swarm/budget: set goal=%s budget=%d", goalID[:8], req.TokenBudget)

	json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
		"goal_id":      goalID,
		"token_budget": req.TokenBudget,
	})
}

// handleSwarmSessionBudgetAPI handles PATCH /api/swarm/sessions/:id/budget
func handleSwarmSessionBudgetAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, sessionID string) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPatch {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		TokenBudget int64 `json:"token_budget"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TokenBudget < 0 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "token_budget must be a non-negative integer"}) //nolint:errcheck
		return
	}

	if _, err := database.ExecContext(ctx,
		"UPDATE swarm_sessions SET token_budget=?, budget_warning_sent=0, updated_at=? WHERE id=?",
		req.TokenBudget, time.Now().Unix(), sessionID,
	); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
		return
	}

	writeSwarmEvent(ctx, sessionID, "", "", "session_budget_set",
		fmt.Sprintf("session=%s budget=%d", sessionID[:8], req.TokenBudget))
	swarmBroadcaster.schedule(sessionID)
	log.Printf("swarm/budget: set session=%s budget=%d", sessionID[:8], req.TokenBudget)

	json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
		"session_id":   sessionID,
		"token_budget": req.TokenBudget,
	})
}
