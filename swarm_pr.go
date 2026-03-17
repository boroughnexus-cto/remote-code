package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

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
