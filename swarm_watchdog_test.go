package main

import (
	"testing"
)

// ─── Watchdog Unit Tests ──────────────────────────────────────────────────────
// watchdogCheckTaskWithAlive is pure when alive map is non-nil: no DB or tmux needed.

func makeWatchdogTask(startedAt, heartbeatAt int64, tmux string) watchdogTask {
	return watchdogTask{
		id:          "test-task-id",
		sessionID:   "test-session-id",
		agentID:     "test-agent-id",
		tmuxSession: tmux,
		startedAt:   startedAt,
		heartbeatAt: heartbeatAt,
	}
}

func TestWatchdog_Healthy(t *testing.T) {
	now := int64(10000)
	heartbeatCutoff := now - int64(watchdogHeartbeatTimeout.Seconds())
	absoluteCutoff := now - int64(watchdogAbsoluteTimeout.Seconds())

	// Recent heartbeat, tmux alive
	task := makeWatchdogTask(now-60, now-60, "alive-session")
	alive := map[string]bool{"alive-session": true}

	reason := watchdogCheckTaskWithAlive(task, now, heartbeatCutoff, absoluteCutoff, alive)
	if reason != "" {
		t.Errorf("expected healthy task, got reason %q", reason)
	}
}

func TestWatchdog_AbsoluteTimeout(t *testing.T) {
	now := int64(10000)
	heartbeatCutoff := now - int64(watchdogHeartbeatTimeout.Seconds())
	absoluteCutoff := now - int64(watchdogAbsoluteTimeout.Seconds())

	// startedAt is before absoluteCutoff
	task := makeWatchdogTask(absoluteCutoff-1, now-60, "alive-session")
	alive := map[string]bool{"alive-session": true}

	reason := watchdogCheckTaskWithAlive(task, now, heartbeatCutoff, absoluteCutoff, alive)
	if reason == "" {
		t.Error("expected absolute timeout reason, got empty")
	}
}

func TestWatchdog_HeartbeatStale_TmuxDead(t *testing.T) {
	now := int64(10000)
	heartbeatCutoff := now - int64(watchdogHeartbeatTimeout.Seconds())
	absoluteCutoff := now - int64(watchdogAbsoluteTimeout.Seconds())

	// Heartbeat is stale, tmux is dead
	task := makeWatchdogTask(absoluteCutoff+100, heartbeatCutoff-1, "dead-session")
	alive := map[string]bool{} // dead-session not in alive

	reason := watchdogCheckTaskWithAlive(task, now, heartbeatCutoff, absoluteCutoff, alive)
	if reason == "" {
		t.Error("expected timeout reason for stale heartbeat + dead tmux, got empty")
	}
}

func TestWatchdog_HeartbeatStale_TmuxAlive(t *testing.T) {
	now := int64(10000)
	heartbeatCutoff := now - int64(watchdogHeartbeatTimeout.Seconds())
	absoluteCutoff := now - int64(watchdogAbsoluteTimeout.Seconds())

	// Heartbeat is stale but tmux is still alive (long LLM call)
	task := makeWatchdogTask(absoluteCutoff+100, heartbeatCutoff-1, "alive-session")
	alive := map[string]bool{"alive-session": true}

	reason := watchdogCheckTaskWithAlive(task, now, heartbeatCutoff, absoluteCutoff, alive)
	if reason != "" {
		t.Errorf("expected healthy (tmux alive), got reason %q", reason)
	}
}

func TestWatchdog_NoHeartbeat_TmuxDead(t *testing.T) {
	now := int64(10000)
	heartbeatCutoff := now - int64(watchdogHeartbeatTimeout.Seconds())
	absoluteCutoff := now - int64(watchdogAbsoluteTimeout.Seconds())

	// No heartbeat ever, started long ago, tmux dead
	task := makeWatchdogTask(heartbeatCutoff-1, 0, "dead-session")
	alive := map[string]bool{}

	reason := watchdogCheckTaskWithAlive(task, now, heartbeatCutoff, absoluteCutoff, alive)
	if reason == "" {
		t.Error("expected timeout reason for no heartbeat + dead tmux, got empty")
	}
}

func TestWatchdog_NoHeartbeat_TmuxAlive(t *testing.T) {
	now := int64(10000)
	heartbeatCutoff := now - int64(watchdogHeartbeatTimeout.Seconds())
	absoluteCutoff := now - int64(watchdogAbsoluteTimeout.Seconds())

	// No heartbeat ever, started long ago, but tmux still alive
	task := makeWatchdogTask(heartbeatCutoff-1, 0, "alive-session")
	alive := map[string]bool{"alive-session": true}

	reason := watchdogCheckTaskWithAlive(task, now, heartbeatCutoff, absoluteCutoff, alive)
	if reason != "" {
		t.Errorf("expected healthy (tmux alive), got reason %q", reason)
	}
}

func TestWatchdog_BoundaryAtHeartbeatCutoff(t *testing.T) {
	now := int64(10000)
	heartbeatCutoff := now - int64(watchdogHeartbeatTimeout.Seconds())
	absoluteCutoff := now - int64(watchdogAbsoluteTimeout.Seconds())

	// heartbeatAt == heartbeatCutoff exactly: not stale yet (strictly less than cutoff triggers)
	task := makeWatchdogTask(absoluteCutoff+100, heartbeatCutoff, "dead-session")
	alive := map[string]bool{}

	reason := watchdogCheckTaskWithAlive(task, now, heartbeatCutoff, absoluteCutoff, alive)
	if reason != "" {
		t.Errorf("heartbeatAt == cutoff should not trigger timeout, got %q", reason)
	}
}

func TestWatchdog_BoundaryAtAbsoluteCutoff(t *testing.T) {
	now := int64(10000)
	heartbeatCutoff := now - int64(watchdogHeartbeatTimeout.Seconds())
	absoluteCutoff := now - int64(watchdogAbsoluteTimeout.Seconds())

	// startedAt == absoluteCutoff exactly: not timed out yet
	task := makeWatchdogTask(absoluteCutoff, now-60, "alive-session")
	alive := map[string]bool{"alive-session": true}

	reason := watchdogCheckTaskWithAlive(task, now, heartbeatCutoff, absoluteCutoff, alive)
	if reason != "" {
		t.Errorf("startedAt == absoluteCutoff should not trigger timeout, got %q", reason)
	}
}
