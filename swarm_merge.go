package main

// ─── Task-branch merge pipeline (SWM-14) ─────────────────────────────────────
//
// Architecture:
//   - Each accepted task gets a per-task branch: swarm/task/<shortTaskID>
//   - The branch is created from current integrator HEAD (= main) and checked
//     out in the agent's worktree via checkoutTaskBranch (called from AcceptTask).
//   - At CompleteTask time, mergeTaskBranch serialises the integration via a
//     per-repo mutex, runs git merge --no-commit --no-ff in the integrator
//     worktree, and either commits or aborts cleanly.
//   - A dedicated integrator worktree (.worktrees/integrator) is used for all
//     merge operations so the base repo's HEAD is never disturbed.
//
// Why a dedicated integrator worktree?
//   - Avoids merging into whatever branch the base repo happens to have checked
//     out (could be anything).
//   - Allows atomic abort (git merge --abort) without affecting agent worktrees.
//   - The integrator is shared across all agents for a given repo and persists
//     across spawn/despawn cycles (not cleaned up on agent despawn).

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// ─── Per-repo merge mutex ─────────────────────────────────────────────────────

// repoMergeMus serialises concurrent mergeTaskBranch calls for the same repo.
var repoMergeMus sync.Map // map[string]*sync.Mutex

func getRepoMergeMu(repoPath string) *sync.Mutex {
	v, _ := repoMergeMus.LoadOrStore(repoPath, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// ─── Integrator worktree ──────────────────────────────────────────────────────

func integratorWorktreePath(repoPath string) string {
	return filepath.Join(repoPath, ".worktrees", "integrator")
}

// repoMainBranch returns "main" or "master" depending on what exists in the repo.
func repoMainBranch(repoPath string) string {
	for _, branch := range []string{"main", "master"} {
		out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", branch).CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			return branch
		}
	}
	return "main"
}

// ensureIntegratorWorktree creates the shared integrator worktree if it does not
// already exist. The integrator is always checked out on the repo's main branch
// and is used exclusively for merge operations.
func ensureIntegratorWorktree(repoPath string) error {
	intPath := integratorWorktreePath(repoPath)
	if _, err := os.Stat(intPath); err == nil {
		return nil // already exists
	}

	mainBranch := repoMainBranch(repoPath)

	if err := os.MkdirAll(filepath.Join(repoPath, ".worktrees"), 0755); err != nil {
		return fmt.Errorf("ensureIntegratorWorktree: mkdir: %w", err)
	}

	out, err := exec.Command("git", "-C", repoPath, "worktree", "add", intPath, mainBranch).CombinedOutput()
	if err != nil {
		// If another goroutine created it concurrently, treat as success.
		if _, statErr := os.Stat(intPath); statErr == nil {
			return nil
		}
		return fmt.Errorf("ensureIntegratorWorktree: git worktree add: %v: %s", err, strings.TrimSpace(string(out)))
	}

	log.Printf("swarm/merge: created integrator worktree at %s (branch: %s)", intPath, mainBranch)
	return nil
}

// ─── Task branch names ────────────────────────────────────────────────────────

func swarmTaskBranchName(taskID string) string {
	return "swarm/task/" + swarmShortID(taskID)
}

// ─── Task branch checkout (AcceptTask) ───────────────────────────────────────

// checkoutTaskBranch creates a per-task git branch from the current integrator
// HEAD (main) and checks it out in the agent's worktree. Called at AcceptTask
// time so the agent always starts each task on a clean, isolated branch.
//
// Non-fatal: failures are logged and the agent continues on its home branch.
// The subsequent mergeTaskBranch call handles the case where the task branch
// is absent or empty.
func checkoutTaskBranch(repoPath, agentWorktreePath, taskID string) error {
	if repoPath == "" || agentWorktreePath == "" {
		return nil // scratch agent — no task branch
	}

	if err := ensureIntegratorWorktree(repoPath); err != nil {
		log.Printf("swarm/merge: ensureIntegrator failed (task %s): %v — skipping task branch", swarmShortID(taskID), err)
		return nil // non-fatal
	}

	branchName := swarmTaskBranchName(taskID)
	intPath := integratorWorktreePath(repoPath)

	// Create task branch from current integrator HEAD (= current main).
	// Use `git -C <integrator> branch <name>` so the new branch points at
	// the integrator's current commit without switching anything.
	if out, err := exec.Command("git", "-C", intPath, "branch", branchName).CombinedOutput(); err != nil {
		s := string(out)
		if strings.Contains(s, "already exists") {
			log.Printf("swarm/merge: task branch %s already exists — reusing", branchName)
		} else {
			log.Printf("swarm/merge: create task branch %s: %v: %s", branchName, err, strings.TrimSpace(s))
			return nil // non-fatal
		}
	}

	// Check out the task branch inside the agent's worktree.
	if out, err := exec.Command("git", "-C", agentWorktreePath, "checkout", branchName).CombinedOutput(); err != nil {
		log.Printf("swarm/merge: checkout %s in %s: %v: %s", branchName, agentWorktreePath, err, strings.TrimSpace(string(out)))
		// Clean up the branch we just created to avoid stale refs.
		exec.Command("git", "-C", repoPath, "branch", "-D", branchName).Run() //nolint:errcheck
		return nil // non-fatal
	}

	log.Printf("swarm/merge: checked out task branch %s for task %s", branchName, swarmShortID(taskID))
	return nil
}

// ─── Merge pipeline (CompleteTask) ───────────────────────────────────────────

// ErrMergeConflict is returned by mergeTaskBranch when git detects a conflict.
// The caller should transition the task to needs_human.
type ErrMergeConflict struct {
	TaskID       string
	ConflictFiles string
}

func (e ErrMergeConflict) Error() string {
	return fmt.Sprintf("merge conflict for task %s — conflicting files: %s",
		shortID(e.TaskID), e.ConflictFiles)
}

// autoCommitIfDirty stages and commits any uncommitted changes in worktreePath.
// Returns nil if the worktree is already clean.
func autoCommitIfDirty(worktreePath, taskID string) error {
	out, err := exec.Command("git", "-C", worktreePath, "status", "--porcelain").CombinedOutput()
	if err != nil {
		return fmt.Errorf("autoCommitIfDirty: git status: %w", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		return nil // clean
	}

	if out, err := exec.Command("git", "-C", worktreePath, "add", "-A").CombinedOutput(); err != nil {
		return fmt.Errorf("autoCommitIfDirty: git add: %v: %s", err, strings.TrimSpace(string(out)))
	}

	msg := fmt.Sprintf("swarm: task %s auto-committed at completion", swarmShortID(taskID))
	if out, err := exec.Command("git", "-C", worktreePath, "commit", "-m", msg).CombinedOutput(); err != nil {
		return fmt.Errorf("autoCommitIfDirty: git commit: %v: %s", err, strings.TrimSpace(string(out)))
	}

	log.Printf("swarm/merge: auto-committed dirty worktree for task %s", swarmShortID(taskID))
	return nil
}

// taskBranchHasCommits returns true if branchName has at least one commit not
// reachable from the repo's main branch.
func taskBranchHasCommits(repoPath, branchName string) bool {
	mainBranch := repoMainBranch(repoPath)
	out, err := exec.Command("git", "-C", repoPath, "log", "--oneline",
		branchName, "^"+mainBranch).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// resetAgentBranch moves the agent's home branch (swarm/<agentID>) to the
// current integrator HEAD so the next task starts from an up-to-date base,
// then checks it out in the agent worktree.
// Called after a successful merge (before deleting the task branch).
func resetAgentBranch(repoPath, agentWorktreePath, agentBranchName string) {
	intPath := integratorWorktreePath(repoPath)

	// Move the agent home branch pointer to current integrator HEAD.
	// `git branch -f` works because the home branch is not checked out
	// in the integrator (only the task branch was).
	if out, err := exec.Command("git", "-C", intPath, "branch", "-f", agentBranchName, "HEAD").CombinedOutput(); err != nil {
		log.Printf("swarm/merge: branch -f %s: %v: %s", agentBranchName, err, strings.TrimSpace(string(out)))
		// Continue even if this fails — the checkout below may still work.
	}

	// Switch the agent worktree back to its home branch.
	if out, err := exec.Command("git", "-C", agentWorktreePath, "checkout", agentBranchName).CombinedOutput(); err != nil {
		log.Printf("swarm/merge: checkout home branch %s: %v: %s", agentBranchName, err, strings.TrimSpace(string(out)))
	}
}

// mergeTaskBranch integrates a completed task's branch into the repo's main
// branch via the dedicated integrator worktree. The full pipeline:
//
//  1. Ensure the integrator worktree exists.
//  2. Auto-commit any dirty state in the agent worktree.
//  3. Bail early if the task branch has no new commits (informational task).
//  4. Acquire the per-repo merge mutex (serialises concurrent completions).
//  5. Run git merge --no-commit --no-ff <taskBranch> in the integrator.
//  6. On conflict: run git merge --abort, return ErrMergeConflict.
//  7. On clean merge: commit with a standard swarm message.
//  8. Switch the agent worktree to its home branch (before deleting task branch).
//  9. Delete the per-task branch.
func mergeTaskBranch(_ context.Context, repoPath, agentWorktreePath, agentBranchName, taskID, taskTitle string) error {
	if repoPath == "" {
		return nil // scratch agent
	}

	intPath := integratorWorktreePath(repoPath)
	if _, err := os.Stat(intPath); err != nil {
		// Integrator missing — legacy agent spawned before SWM-14.  Skip merge.
		log.Printf("swarm/merge: integrator absent for %s — skipping merge (task %s)", repoPath, swarmShortID(taskID))
		return nil
	}

	// Auto-commit any uncommitted work in the agent's worktree.
	if agentWorktreePath != "" {
		if err := autoCommitIfDirty(agentWorktreePath, taskID); err != nil {
			log.Printf("swarm/merge: auto-commit failed task %s: %v", swarmShortID(taskID), err)
			// Continue — merge whatever was committed.
		}
	}

	branchName := swarmTaskBranchName(taskID)

	// Skip if there are no new commits (e.g. planning/analysis tasks).
	if !taskBranchHasCommits(repoPath, branchName) {
		log.Printf("swarm/merge: task %s has no new commits — skipping merge", swarmShortID(taskID))
		// Clean up the empty task branch if it exists.
		exec.Command("git", "-C", repoPath, "branch", "-D", branchName).Run() //nolint:errcheck
		if agentWorktreePath != "" && agentBranchName != "" {
			resetAgentBranch(repoPath, agentWorktreePath, agentBranchName)
		}
		return nil
	}

	// Serialise concurrent merges for the same repo.
	mu := getRepoMergeMu(repoPath)
	mu.Lock()
	defer mu.Unlock()

	// Attempt the merge inside the integrator worktree.
	mergeOut, mergeErr := exec.Command("git", "-C", intPath,
		"merge", "--no-commit", "--no-ff", branchName).CombinedOutput()

	if mergeErr != nil {
		// Identify conflicting files before aborting.
		conflictOut, _ := exec.Command("git", "-C", intPath,
			"diff", "--name-only", "--diff-filter=U").CombinedOutput()
		conflictFiles := strings.TrimSpace(string(conflictOut))

		// Always abort to keep the integrator worktree clean.
		if out, err := exec.Command("git", "-C", intPath, "merge", "--abort").CombinedOutput(); err != nil {
			log.Printf("swarm/merge: merge --abort failed: %v: %s", err, strings.TrimSpace(string(out)))
		}

		if conflictFiles != "" {
			return ErrMergeConflict{TaskID: taskID, ConflictFiles: conflictFiles}
		}
		return fmt.Errorf("mergeTaskBranch: git merge error: %v: %s", mergeErr, strings.TrimSpace(string(mergeOut)))
	}

	// Check if there's anything staged (merge may be a no-op for an already-ff branch).
	statusOut, _ := exec.Command("git", "-C", intPath, "status", "--porcelain").CombinedOutput()
	if strings.TrimSpace(string(statusOut)) != "" {
		commitMsg := fmt.Sprintf("swarm: merge task %s — %s", swarmShortID(taskID), taskTitle)
		if out, err := exec.Command("git", "-C", intPath, "commit", "-m", commitMsg).CombinedOutput(); err != nil {
			exec.Command("git", "-C", intPath, "merge", "--abort").Run() //nolint:errcheck
			return fmt.Errorf("mergeTaskBranch: git commit failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}

	// Switch agent worktree back to its home branch BEFORE deleting the task
	// branch — git refuses to delete a branch checked out in any worktree.
	if agentWorktreePath != "" && agentBranchName != "" {
		resetAgentBranch(repoPath, agentWorktreePath, agentBranchName)
	}

	// Delete the per-task branch.
	if out, err := exec.Command("git", "-C", repoPath, "branch", "-D", branchName).CombinedOutput(); err != nil {
		log.Printf("swarm/merge: delete task branch %s: %v: %s", branchName, err, strings.TrimSpace(string(out)))
	}

	log.Printf("swarm/merge: merged task %s (%q) into %s", swarmShortID(taskID), taskTitle, repoMainBranch(repoPath))
	return nil
}
