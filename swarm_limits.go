package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// swarmLimits holds configurable resource ceilings for the swarm.
type swarmLimits struct {
	MaxAgents         int   // max live agents per session (0 = disabled)
	MaxTasksPerSession int   // max active tasks per session (0 = disabled)
	MaxDiskMB         int64 // max disk usage of ~/.swarmops in MB (0 = disabled)
}

func loadSwarmLimits() swarmLimits {
	l := swarmLimits{
		MaxAgents:          10,
		MaxTasksPerSession: 50,
		MaxDiskMB:          5000,
	}
	if v := os.Getenv("SWARM_MAX_AGENTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			l.MaxAgents = n
		}
	}
	if v := os.Getenv("SWARM_MAX_TASKS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			l.MaxTasksPerSession = n
		}
	}
	if v := os.Getenv("SWARM_MAX_DISK_MB"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			l.MaxDiskMB = n
		}
	}
	return l
}

// spawnMu serializes agent spawn calls so that the limit check and the
// subsequent tmux_session DB update are atomic from a concurrency perspective.
// Held only within spawnSwarmAgent — the goroutines started inside (e.g.
// waitForClaudeReady) run after the mutex is released.
var spawnMu sync.Mutex

// checkAgentLimit returns an error if this session already has MaxAgents live agents.
// Must be called with spawnMu held.
func checkAgentLimit(ctx context.Context, sessionID string) error {
	limit := loadSwarmLimits().MaxAgents
	if limit <= 0 {
		return nil
	}
	var count int
	database.QueryRowContext(ctx, //nolint:errcheck
		"SELECT COUNT(*) FROM swarm_agents WHERE session_id = ? AND tmux_session IS NOT NULL",
		sessionID,
	).Scan(&count)
	if count >= limit {
		return fmt.Errorf("agent limit reached (%d/%d) — despawn idle agents or raise SWARM_MAX_AGENTS", count, limit)
	}
	return nil
}

// ─── Async disk usage cache ───────────────────────────────────────────────────

var (
	diskCacheMu  sync.RWMutex
	diskCachedMB int64
	diskCachedAt time.Time
	diskCacheTTL = 60 * time.Second
)

// updateDiskUsageCache runs du on the swarmops directory and caches the result.
// Called by the background poller — never blocks spawn.
func updateDiskUsageCache() {
	dir := swarmBaseDir()
	out, err := exec.Command("du", "-sm", dir).Output()
	if err != nil {
		return
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return
	}
	mb, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return
	}
	diskCacheMu.Lock()
	diskCachedMB = mb
	diskCachedAt = time.Now()
	diskCacheMu.Unlock()
}

// startDiskUsagePoller runs disk usage checks asynchronously on a 60-second ticker.
// Called from main() so spawns do not block on du.
func startDiskUsagePoller() {
	go func() {
		updateDiskUsageCache()
		ticker := time.NewTicker(diskCacheTTL)
		defer ticker.Stop()
		for range ticker.C {
			updateDiskUsageCache()
		}
	}()
}

// checkDiskLimit returns an error if the cached disk usage exceeds MaxDiskMB.
// Skips the check if the cache is stale (avoids blocking on an unavailable du).
func checkDiskLimit() error {
	limit := loadSwarmLimits().MaxDiskMB
	if limit <= 0 {
		return nil
	}
	diskCacheMu.RLock()
	mb := diskCachedMB
	age := time.Since(diskCachedAt)
	diskCacheMu.RUnlock()

	if age > diskCacheTTL*2 {
		// Cache is stale — skip rather than blocking spawn.
		log.Printf("swarm/limits: disk usage cache stale (%v), skipping disk check", age.Round(time.Second))
		return nil
	}
	if mb >= limit {
		return fmt.Errorf("disk limit reached (%d MB / %d MB) — clean up old worktrees or raise SWARM_MAX_DISK_MB", mb, limit)
	}
	return nil
}

// checkAllSpawnLimits combines agent count and disk limits.
// Cost limit is checked separately via checkSessionCostLimit.
// Must be called with spawnMu held.
func checkAllSpawnLimits(ctx context.Context, sessionID string) error {
	if err := checkAgentLimit(ctx, sessionID); err != nil {
		return err
	}
	if err := checkDiskLimit(); err != nil {
		return err
	}
	return nil
}
