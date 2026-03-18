package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	orphanSweepInterval = 10 * time.Minute
	// orphanGracePeriod is the minimum session age before it is eligible for
	// orphan cleanup. Prevents TOCTOU races where we kill a session that was
	// just created but whose DB record has not yet been committed.
	orphanGracePeriod = 2 * time.Minute
)

// startOrphanSweeper runs runOrphanSweep on startup (after the grace period)
// and then every orphanSweepInterval.
func startOrphanSweeper() {
	go func() {
		// Initial delay equal to the grace period prevents false-positives
		// at startup before the status monitor has reconciled DB state.
		time.Sleep(orphanGracePeriod)
		runOrphanSweep(context.Background())

		ticker := time.NewTicker(orphanSweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			runOrphanSweep(context.Background())
		}
	}()
}

// runOrphanSweep finds:
//  1. tmux sessions named sw-* that no longer have a matching DB agent.
//  2. git worktrees in *.worktrees/sw-* that no longer have a matching DB agent.
//
// Sessions/worktrees younger than orphanGracePeriod are skipped to avoid
// racing fresh spawns whose DB records may not be written yet.
func runOrphanSweep(ctx context.Context) {
	swept := 0

	// ── Orphan tmux sessions ───────────────────────────────────────────────
	// tmux ls returns exit code 1 when no sessions exist — treat as empty.
	out, err := exec.Command("tmux", "ls", "-F", "#{session_name} #{session_created}").Output()
	if err == nil {
		now := time.Now().Unix()
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			name := fields[0]
			if !strings.HasPrefix(name, "sw-") {
				continue
			}

			// Parse creation timestamp.
			var createdAt int64
			fmt.Sscanf(fields[1], "%d", &createdAt)
			if now-createdAt < int64(orphanGracePeriod.Seconds()) {
				continue // too young — skip
			}

			// Check DB.
			var count int
			database.QueryRowContext(ctx, //nolint:errcheck
				"SELECT COUNT(*) FROM swarm_agents WHERE tmux_session = ?", name,
			).Scan(&count)
			if count > 0 {
				continue // live agent owns this session
			}

			// Orphan — kill it.
			if err := exec.Command("tmux", "kill-session", "-t", name).Run(); err == nil {
				log.Printf("swarm/cleanup: killed orphan tmux session %s", name)
				swept++
			}
		}
	}

	// ── Orphan git worktrees ───────────────────────────────────────────────
	// Scan all repo paths known in the DB for .worktrees/sw-* directories.
	repoRows, err := database.QueryContext(ctx,
		"SELECT DISTINCT repo_path FROM swarm_agents WHERE repo_path IS NOT NULL",
	)
	if err != nil {
		if swept > 0 {
			log.Printf("swarm/cleanup: sweep done — %d resource(s) cleaned", swept)
		}
		return
	}
	defer repoRows.Close()

	for repoRows.Next() {
		var repoPath string
		if err := repoRows.Scan(&repoPath); err != nil {
			continue
		}

		// git worktree list --porcelain gives blocks separated by blank lines.
		wtOut, err := exec.Command("git", "-C", repoPath, "worktree", "list", "--porcelain").Output()
		if err != nil {
			continue
		}

		for _, block := range strings.Split(string(wtOut), "\n\n") {
			var wtPath string
			for _, l := range strings.Split(block, "\n") {
				if strings.HasPrefix(l, "worktree ") {
					wtPath = strings.TrimPrefix(l, "worktree ")
				}
			}
			if wtPath == "" {
				continue
			}

			dir := filepath.Base(wtPath)
			if !strings.HasPrefix(dir, "sw-") {
				continue
			}

			// Check DB by worktree_path.
			var count int
			database.QueryRowContext(ctx, //nolint:errcheck
				"SELECT COUNT(*) FROM swarm_agents WHERE worktree_path = ?", wtPath,
			).Scan(&count)
			if count > 0 {
				continue // live agent owns this worktree
			}

			// Orphan — remove the worktree. Preserve the branch so uncommitted
			// work can be recovered if this was a false positive.
			if err := exec.Command("git", "-C", repoPath, "worktree", "remove", wtPath, "--force").Run(); err == nil {
				log.Printf("swarm/cleanup: removed orphan worktree %s (branch preserved)", wtPath)
				swept++
			}
		}
	}

	if swept > 0 {
		log.Printf("swarm/cleanup: sweep complete — %d resource(s) cleaned", swept)
	}
}

// handleSwarmCleanupAPI handles POST /api/swarm/cleanup.
// Schedules an orphan sweep in the background and returns immediately.
func handleSwarmCleanupAPI(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	go runOrphanSweep(ctx)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "sweep scheduled"}) //nolint:errcheck
}
