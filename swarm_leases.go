package main

// ─── Repo Leases (H1 — Multi-agent worktree locking) ─────────────────────────
//
// Prevents two agents from simultaneously modifying the same repository.
// Lease lifecycle:
//   1. acquireRepoLease — called from AcceptTask with optional scope_paths
//   2. releaseRepoLease — called from CompleteTask, BlockTask, timeoutTask, emergencyRotateAgent
//   3. watchdog periodic cleanup — releases leases for tasks in terminal states
//
// Design:
//   - Repo-level locking for v1 (pessimistic, simple, predictable)
//   - Scope paths stored in swarm_task_scopes for future file-level granularity
//   - Serializable transaction + UNIQUE partial index prevents check-then-insert races
//   - Empty scope_paths → broad "/" lease (safe default)

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

// ErrScopeConflict is returned by acquireRepoLease when an active lease exists.
type ErrScopeConflict struct {
	ConflictingTaskID  string
	ConflictingGoalID  string
	ConflictingPaths   []string
}

func (e ErrScopeConflict) Error() string {
	return fmt.Sprintf("scope conflict: task %s (goal %s) already holds lease on %v",
		shortID(e.ConflictingTaskID), shortID(e.ConflictingGoalID), e.ConflictingPaths)
}

// acquireRepoLease atomically acquires a repo-level lease for a task.
// scopePaths is the list of directory prefixes the agent claims to modify.
// Empty scopePaths results in a broad "/" lease.
//
// Uses a SERIALIZABLE transaction + UNIQUE partial index so concurrent AcceptTask
// calls cannot both succeed for the same repo (PR-3 + PR-6 peer review corrections).
func acquireRepoLease(ctx context.Context, db *sql.DB, sessionID, goalID, taskID, repoPath string, scopePaths []string) error {
	if repoPath == "" {
		return nil // no repo — scratch agents don't hold leases
	}
	if len(scopePaths) == 0 {
		scopePaths = []string{"/"}
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("acquireRepoLease: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// 1. Idempotency: if this task already holds a lease, return success.
	var existing string
	_ = tx.QueryRowContext(ctx,
		`SELECT id FROM swarm_repo_leases WHERE task_id=? AND released_at IS NULL`,
		taskID,
	).Scan(&existing)
	if existing != "" {
		return tx.Commit() // already leased — idempotent success
	}

	// 2. Conflict detection: find any active lease on the same repo.
	//    For v1 we use repo-level locking (all active leases on this repo conflict).
	rows, err := tx.QueryContext(ctx,
		`SELECT task_id, goal_id FROM swarm_repo_leases
		 WHERE repo_path=? AND released_at IS NULL AND expires_at > unixepoch()`,
		repoPath,
	)
	if err != nil {
		return fmt.Errorf("acquireRepoLease: query conflicts: %w", err)
	}
	var conflictTask, conflictGoal string
	if rows.Next() {
		rows.Scan(&conflictTask, &conflictGoal) //nolint:errcheck
	}
	rows.Close()

	if conflictTask != "" && conflictTask != taskID {
		// Look up scope paths of the conflicting task for the error message.
		scopeRows, _ := tx.QueryContext(ctx,
			`SELECT path_prefix FROM swarm_task_scopes WHERE task_id=?`, conflictTask)
		var conflictPaths []string
		if scopeRows != nil {
			for scopeRows.Next() {
				var p string
				scopeRows.Scan(&p) //nolint:errcheck
				conflictPaths = append(conflictPaths, p)
			}
			scopeRows.Close()
		}
		return ErrScopeConflict{
			ConflictingTaskID: conflictTask,
			ConflictingGoalID: conflictGoal,
			ConflictingPaths:  conflictPaths,
		}
	}

	// 3. INSERT lease — UNIQUE partial index on (task_id) WHERE released_at IS NULL
	//    prevents duplicate leases even under concurrent serializable transactions.
	leaseID := generateSwarmID()
	expiresAt := time.Now().Unix() + 7200 // 2h TTL; watchdog cleans up
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO swarm_repo_leases (id, session_id, goal_id, task_id, repo_path, acquired_at, expires_at)
		 VALUES (?,?,?,?,?,unixepoch(),?)`,
		leaseID, sessionID, goalID, taskID, repoPath, expiresAt,
	); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			// Race: another goroutine inserted first — treat as conflict.
			return ErrScopeConflict{ConflictingTaskID: "(concurrent)", ConflictingPaths: scopePaths}
		}
		return fmt.Errorf("acquireRepoLease: insert lease: %w", err)
	}

	// 4. INSERT scope rows (normalised, non-blocking — duplicates ignored).
	for _, p := range scopePaths {
		scopeID := generateSwarmID()
		tx.ExecContext(ctx, //nolint:errcheck
			`INSERT OR IGNORE INTO swarm_task_scopes (id, task_id, path_prefix) VALUES (?,?,?)`,
			scopeID, taskID, p,
		)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("acquireRepoLease: commit: %w", err)
	}

	log.Printf("swarm/leases: acquired lease=%s task=%s repo=%s scope=%v",
		leaseID[:8], taskID[:8], repoPath, scopePaths)
	return nil
}

// releaseRepoLease marks the active lease for taskID as released.
// Safe to call multiple times (idempotent).
func releaseRepoLease(ctx context.Context, db *sql.DB, taskID string) {
	res, err := db.ExecContext(ctx,
		`UPDATE swarm_repo_leases SET released_at=unixepoch() WHERE task_id=? AND released_at IS NULL`,
		taskID,
	)
	if err != nil {
		log.Printf("swarm/leases: release failed task=%s: %v", shortID(taskID), err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("swarm/leases: released lease for task=%s", shortID(taskID))
	}
}

// cleanupExpiredLeases releases leases whose TTL has elapsed AND whose owning
// task is in a terminal state. Called from the watchdog tick.
func cleanupExpiredLeases(ctx context.Context, db *sql.DB) {
	res, err := db.ExecContext(ctx,
		`UPDATE swarm_repo_leases SET released_at=unixepoch()
		 WHERE released_at IS NULL
		   AND (
		     expires_at < unixepoch()
		     OR task_id IN (
		       SELECT id FROM swarm_tasks
		       WHERE stage IN ('complete','failed','cancelled','timed_out','failed_ralph_loop')
		     )
		   )`,
	)
	if err != nil {
		log.Printf("swarm/leases: cleanup error: %v", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("swarm/leases: cleaned up %d expired/orphan leases", n)
	}
}
