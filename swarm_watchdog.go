package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

// ─── Task Watchdog ─────────────────────────────────────────────────────────────
//
// The watchdog detects tasks stuck in 'running' or 'accepted' without progress
// and transitions them to 'timed_out'. Two independent timeout criteria:
//
//  1. Heartbeat timeout: last_heartbeat_at is stale AND the agent's tmux session
//     is confirmed dead. (Two-factor: avoids false positives from long LLM calls.)
//
//  2. Absolute timeout: task has been running for > watchdogAbsoluteTimeout
//     regardless of heartbeat. (Catches infinite loops with heartbeat.)
//
// On timeout: transition task → timed_out, write event, brief SiBot.

const (
	watchdogInterval         = 60 * time.Second
	watchdogHeartbeatTimeout = 45 * time.Minute // stale if no heartbeat this long
	watchdogAbsoluteTimeout  = 2 * time.Hour    // hard cap regardless of heartbeat
)

func startTaskWatchdog() {
	go func() {
		// Run once at startup to catch tasks stale from before a server restart.
		watchdogTick()
		ticker := time.NewTicker(watchdogInterval)
		defer ticker.Stop()
		for range ticker.C {
			watchdogTick()
		}
	}()
	log.Printf("swarm/watchdog: started (heartbeat_timeout=%s, absolute=%s)", watchdogHeartbeatTimeout, watchdogAbsoluteTimeout)
}

type watchdogTask struct {
	id          string
	sessionID   string
	agentID     string
	tmuxSession string
	startedAt   int64
	heartbeatAt int64 // 0 if no heartbeat yet
}

func watchdogTick() {
	ctx := context.Background()
	now := time.Now().Unix()

	// Find tasks in active states
	rows, err := database.QueryContext(ctx,
		`SELECT t.id, t.session_id,
		        COALESCE(t.agent_id,''),
		        COALESCE(a.tmux_session,''),
		        COALESCE(t.started_at, t.created_at),
		        COALESCE(t.last_heartbeat_at, 0)
		 FROM swarm_tasks t
		 LEFT JOIN swarm_agents a ON a.id = t.agent_id
		 WHERE t.stage IN ('running','accepted')`,
	)
	if err != nil {
		log.Printf("swarm/watchdog: query error: %v", err)
		return
	}
	defer rows.Close()

	var tasks []watchdogTask
	for rows.Next() {
		var wt watchdogTask
		rows.Scan(&wt.id, &wt.sessionID, &wt.agentID, &wt.tmuxSession, &wt.startedAt, &wt.heartbeatAt)
		tasks = append(tasks, wt)
	}

	heartbeatCutoff := now - int64(watchdogHeartbeatTimeout.Seconds())
	absoluteCutoff := now - int64(watchdogAbsoluteTimeout.Seconds())

	// Build alive-session set once to avoid N subprocess calls per task
	aliveSessions := listAliveTmuxSessions()

	for _, t := range tasks {
		reason := watchdogCheckTaskWithAlive(t, now, heartbeatCutoff, absoluteCutoff, aliveSessions)
		if reason == "" {
			continue
		}
		timeoutTask(ctx, t, reason)
	}
}

// listAliveTmuxSessions runs tmux once and returns a set of live session names.
// Returns nil if tmux is unavailable or has no sessions (caller treats all as dead).
func listAliveTmuxSessions() map[string]bool {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return nil // no sessions or tmux not running
	}
	result := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			result[line] = true
		}
	}
	return result
}

// watchdogCheckTaskWithAlive is like watchdogCheckTask but uses a pre-built
// alive-session set instead of calling isTmuxSessionAlive per task.
func watchdogCheckTaskWithAlive(t watchdogTask, now, heartbeatCutoff, absoluteCutoff int64, alive map[string]bool) string {
	isAlive := func(session string) bool {
		if alive == nil {
			return isTmuxSessionAlive(session) // fallback if listing failed
		}
		return alive[session]
	}

	if t.startedAt > 0 && t.startedAt < absoluteCutoff {
		return fmt.Sprintf("absolute timeout after %s", watchdogAbsoluteTimeout)
	}
	if t.heartbeatAt > 0 && t.heartbeatAt < heartbeatCutoff {
		if t.tmuxSession == "" || !isAlive(t.tmuxSession) {
			return fmt.Sprintf("heartbeat stale for %.0fm and agent offline",
				float64(now-t.heartbeatAt)/60)
		}
	}
	if t.heartbeatAt == 0 && t.startedAt > 0 && t.startedAt < heartbeatCutoff {
		if t.tmuxSession == "" || !isAlive(t.tmuxSession) {
			return fmt.Sprintf("no heartbeat received and agent offline after %.0fm",
				float64(now-t.startedAt)/60)
		}
	}
	return ""
}

// watchdogCheckTask returns a timeout reason string if the task should be timed
// out, or "" if it is still healthy.
func watchdogCheckTask(t watchdogTask, now, heartbeatCutoff, absoluteCutoff int64) string {
	// Absolute timeout: running too long regardless of heartbeat
	if t.startedAt > 0 && t.startedAt < absoluteCutoff {
		return fmt.Sprintf("absolute timeout after %s", watchdogAbsoluteTimeout)
	}

	// Heartbeat timeout: stale heartbeat — but only if the agent tmux is also dead.
	// If tmux is still alive the agent is likely in a long LLM call.
	if t.heartbeatAt > 0 && t.heartbeatAt < heartbeatCutoff {
		if t.tmuxSession == "" || !isTmuxSessionAlive(t.tmuxSession) {
			return fmt.Sprintf("heartbeat stale for %.0fm and agent offline",
				float64(now-t.heartbeatAt)/60)
		}
	}

	// No heartbeat ever but task has been running a long time and tmux is dead
	if t.heartbeatAt == 0 && t.startedAt > 0 && t.startedAt < heartbeatCutoff {
		if t.tmuxSession == "" || !isTmuxSessionAlive(t.tmuxSession) {
			return fmt.Sprintf("no heartbeat received and agent offline after %.0fm",
				float64(now-t.startedAt)/60)
		}
	}

	return ""
}

func timeoutTask(ctx context.Context, t watchdogTask, reason string) {
	if err := transitionTask(ctx, t.id, "timed_out"); err != nil {
		// Task likely already in terminal state — not an error
		return
	}
	database.ExecContext(ctx, //nolint:errcheck
		"UPDATE swarm_tasks SET blocked_reason=?, updated_at=? WHERE id=?",
		reason, time.Now().Unix(), t.id,
	)
	writeSwarmEvent(ctx, t.sessionID, t.agentID, t.id, "task_timed_out", reason)
	swarmBroadcaster.schedule(t.sessionID)
	log.Printf("swarm/watchdog: timed out task=%s reason=%q", t.id[:8], reason)

	// Brief the orchestrator so it can reassign
	go briefSiBotImmediate(t.sessionID)
}
