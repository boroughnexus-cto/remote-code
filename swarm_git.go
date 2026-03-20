package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
)

// ─── Agent git status ──────────────────────────────────────────────────────────
//
// GET /api/swarm/sessions/:sid/agents/:aid/git
//
// Runs lightweight git commands in the agent's worktree (or repo_path if no
// worktree). Returns branch, dirty flag, commits ahead of upstream, and last
// commit subject. All fields degrade gracefully when git is unavailable or the
// path is not a repository.

type agentGitStatus struct {
	Branch  string `json:"branch"`
	Dirty   bool   `json:"dirty"`
	Ahead   int    `json:"ahead"`
	Subject string `json:"subject"` // last commit subject line
}

// handleSwarmAgentGitAPI implements GET /api/swarm/sessions/:sid/agents/:aid/git.
func handleSwarmAgentGitAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, sessionID, agentID string) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	// Look up worktree_path, falling back to repo_path.
	var worktreePath, repoPath string
	database.QueryRowContext(ctx,
		`SELECT COALESCE(worktree_path,''), COALESCE(repo_path,'')
		 FROM swarm_agents WHERE id = ? AND session_id = ?`,
		agentID, sessionID,
	).Scan(&worktreePath, &repoPath) //nolint:errcheck

	dir := worktreePath
	if dir == "" {
		dir = repoPath
	}
	if dir == "" {
		json.NewEncoder(w).Encode(agentGitStatus{}) //nolint:errcheck
		return
	}

	gs := gitStatusForDir(dir)
	json.NewEncoder(w).Encode(gs) //nolint:errcheck
}

// gitStatusForDir runs git commands in dir and returns an agentGitStatus.
// Any individual failure degrades gracefully (zero value for that field).
func gitStatusForDir(dir string) agentGitStatus {
	var gs agentGitStatus

	// Branch name
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		gs.Branch = strings.TrimSpace(string(out))
	}

	// Dirty working tree: any output from `git status --porcelain`
	if out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output(); err == nil {
		gs.Dirty = strings.TrimSpace(string(out)) != ""
	}

	// Commits ahead of upstream: rev-list HEAD...@{upstream} --count
	if out, err := exec.Command("git", "-C", dir, "rev-list", "--count", "HEAD...@{upstream}").Output(); err == nil {
		if n, err2 := strconv.Atoi(strings.TrimSpace(string(out))); err2 == nil {
			gs.Ahead = n
		}
	}

	// Last commit subject
	if out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%s").Output(); err == nil {
		gs.Subject = strings.TrimSpace(string(out))
	}

	return gs
}
