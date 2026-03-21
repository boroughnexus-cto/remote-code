package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

// maybeAutoMergeGoalPRs checks the session merge policy and, if configured, issues
// a merge command to the agent for any eligible PRs associated with the goal.
//
// The server does NOT exec gh directly — it sends a structured InboxMessage to the
// agent's inbox so the agent executes the merge from within its worktree. This
// preserves the agent transport boundary (PR-5 peer review correction).
//
// The last_merge_attempt_at idempotency guard prevents the reconcile loop from
// re-dispatching an already-attempted merge (PR-H2 peer review correction).
func maybeAutoMergeGoalPRs(ctx context.Context, sessionID, goalID string) {
	// Read session merge policy
	var policy string
	var threshold float64
	if err := database.QueryRowContext(ctx,
		"SELECT COALESCE(merge_policy,'manual'), COALESCE(merge_confidence_threshold,0.85) FROM swarm_sessions WHERE id=?",
		sessionID,
	).Scan(&policy, &threshold); err != nil {
		return
	}
	if policy == "manual" {
		return
	}

	// Find tasks for this goal with an open PR, not yet merge-attempted
	rows, err := database.QueryContext(ctx,
		`SELECT t.id, t.pr_url, COALESCE(t.confidence,0), COALESCE(t.ci_status,''),
		        COALESCE(a.id,'')
		 FROM swarm_tasks t
		 LEFT JOIN swarm_agents a ON a.id = t.agent_id
		 WHERE t.goal_id = ?
		   AND t.pr_url IS NOT NULL AND t.pr_url != ''
		   AND t.last_merge_attempt_at IS NULL`,
		goalID,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	type mergeCandidate struct {
		taskID, prURL, ciStatus, agentID string
		confidence                        float64
	}
	var candidates []mergeCandidate
	for rows.Next() {
		var c mergeCandidate
		rows.Scan(&c.taskID, &c.prURL, &c.confidence, &c.ciStatus, &c.agentID) //nolint:errcheck
		candidates = append(candidates, c)
	}
	rows.Close()

	for _, c := range candidates {
		eligible := false
		switch policy {
		case "confidence":
			eligible = c.confidence >= threshold && c.ciStatus == "passed"
		case "always":
			eligible = c.ciStatus == "passed"
		}
		if !eligible {
			continue
		}

		// Set idempotency guard BEFORE dispatching to prevent races.
		res, err := database.ExecContext(ctx,
			"UPDATE swarm_tasks SET last_merge_attempt_at=?, updated_at=? WHERE id=? AND last_merge_attempt_at IS NULL",
			time.Now().Unix(), time.Now().Unix(), c.taskID,
		)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // already claimed by concurrent call
		}

		if c.agentID == "" {
			// No agent — send Telegram for manual merge
			go sendTelegramNotification(fmt.Sprintf(
				"🔀 *PR ready for merge* (no agent available)\n\nGoal: `%s`\nPR: %s\n\nPlease merge manually.",
				goalID[:8], c.prURL,
			))
			database.ExecContext(ctx, //nolint:errcheck
				"UPDATE swarm_tasks SET pr_status='ready_for_review', updated_at=? WHERE id=?",
				time.Now().Unix(), c.taskID,
			)
			continue
		}

		// Issue merge command to agent via transport (not server-side gh exec).
		mergePayload, _ := json.Marshal(map[string]string{
			"pr_url": c.prURL,
			"method": "squash",
		})
		msg := InboxMessage{
			SchemaVersion: "1",
			MessageID:     generateSwarmID(),
			Type:          "merge_pr",
			TaskID:        c.taskID,
			Action: fmt.Sprintf("Merge the pull request via: gh pr merge --squash %s\n\nAfter merging, emit task_complete with summary='pr merged'.\nIf merge fails (branch protection / CODEOWNERS), do NOT retry — emit task_complete with confidence=0.3 and reason='merge blocked by branch protection'.",
				c.prURL),
			WriteTo: string(mergePayload),
			SentAt:  time.Now().Unix(),
		}
		if err := writeAgentInboxMsg(ctx, sessionID, c.agentID, msg); err != nil {
			log.Printf("swarm/pr: merge_pr dispatch failed task=%s: %v", c.taskID[:8], err)
			continue
		}
		log.Printf("swarm/pr: merge_pr dispatched task=%s agent=%s pr=%s", c.taskID[:8], c.agentID[:8], c.prURL)
		writeSwarmEvent(ctx, sessionID, c.agentID, c.taskID, "merge_dispatched", c.prURL)
		swarmBroadcaster.schedule(sessionID)
	}
}

// tryCreatePR is a fire-and-forget wrapper — errors are logged, not surfaced to callers.
func tryCreatePR(ctx context.Context, sessionID, taskID string) {
	prUrl, err := createPRForTask(ctx, sessionID, taskID)
	if err != nil {
		log.Printf("swarm: auto PR creation failed for task %s: %v", taskID, err)
		return
	}
	database.ExecContext(ctx,
		"UPDATE swarm_tasks SET pr_url = ?, updated_at = ? WHERE id = ? AND session_id = ?",
		prUrl, time.Now().Unix(), taskID, sessionID)
	writeSwarmEvent(ctx, sessionID, "", taskID, "pr_created", prUrl)
	swarmBroadcaster.schedule(sessionID)
	log.Printf("swarm: PR created for task %s: %s", taskID, prUrl)
}

// createPRForTask looks up the task's assigned agent, pushes the branch, and
// runs `gh pr create` from the agent's worktree. Returns the PR URL.
func createPRForTask(ctx context.Context, sessionID, taskID string) (string, error) {
	// Load task
	var title string
	var description, agentID sql.NullString
	err := database.QueryRowContext(ctx,
		"SELECT title, description, agent_id FROM swarm_tasks WHERE id = ? AND session_id = ?",
		taskID, sessionID,
	).Scan(&title, &description, &agentID)
	if err != nil {
		return "", fmt.Errorf("task not found: %v", err)
	}
	if !agentID.Valid || agentID.String == "" {
		return "", fmt.Errorf("task has no assigned agent")
	}

	// Load agent's worktree path
	var worktreePath string
	err = database.QueryRowContext(ctx,
		"SELECT COALESCE(worktree_path, '') FROM swarm_agents WHERE id = ?",
		agentID.String,
	).Scan(&worktreePath)
	if err != nil || worktreePath == "" {
		return "", fmt.Errorf("agent has no active worktree")
	}

	branchName := swarmBranchName(agentID.String)

	// Check there's at least one commit on this branch vs HEAD of the default branch.
	// `git log origin/HEAD..HEAD` lists commits unique to this branch; empty = nothing to PR.
	checkCmd := exec.CommandContext(ctx, "git", "log", "origin/HEAD..HEAD", "--oneline")
	checkCmd.Dir = worktreePath
	checkOut, err := checkCmd.Output()
	if err != nil || strings.TrimSpace(string(checkOut)) == "" {
		return "", fmt.Errorf("branch has no commits ahead of origin/HEAD — nothing to PR")
	}

	// Check a PR doesn't already exist for this branch
	prCheckCmd := exec.CommandContext(ctx, "gh", "pr", "view", "--json", "url", "-q", ".url")
	prCheckCmd.Dir = worktreePath
	if prURL, err := prCheckCmd.Output(); err == nil {
		if u := strings.TrimSpace(string(prURL)); u != "" {
			return u, nil // PR already exists — return its URL
		}
	}

	// Push the branch to remote
	pushCmd := exec.CommandContext(ctx, "git", "push", "--set-upstream", "origin", branchName)
	pushCmd.Dir = worktreePath
	if out, err := pushCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git push: %v: %s", err, strings.TrimSpace(string(out)))
	}

	// Build PR body
	body := "Auto-created by RC Swarm."
	if description.Valid && description.String != "" {
		body = description.String
	}

	// Create PR via gh CLI
	prCmd := exec.CommandContext(ctx, "gh", "pr", "create",
		"--title", title,
		"--body", body,
	)
	prCmd.Dir = worktreePath
	out, err := prCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh pr create: %v: %s", err, strings.TrimSpace(string(out)))
	}

	// Parse URL from last non-empty output line
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line, nil
		}
	}
	return strings.TrimSpace(string(out)), nil
}
