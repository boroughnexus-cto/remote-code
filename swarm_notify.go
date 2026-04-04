package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// -----------------
// Swarm Notifications
// -----------------

type notifyDebouncer struct {
	mu       sync.Mutex
	lastSent map[string]time.Time // key: agentID+type
}

var swarmNotifier = &notifyDebouncer{
	lastSent: make(map[string]time.Time),
}

// shouldNotify returns true if a notification for this agent+type hasn't been sent
// within the debounce window (5 minutes), then records the time.
func (n *notifyDebouncer) shouldNotify(agentID, eventType string) bool {
	key := agentID + ":" + eventType
	n.mu.Lock()
	defer n.mu.Unlock()
	last, ok := n.lastSent[key]
	if ok && time.Since(last) < 5*time.Minute {
		return false
	}
	n.lastSent[key] = time.Now()
	return true
}

// sendLocalNotification sends a short message to local sinks:
//  1. All non-agent tmux sessions via display-message (5 s display, skip sw-* agent sessions)
//  2. notify-send if a D-Bus session is available (desktop/GUI environments)
//
// Runs in its own goroutine to avoid blocking callers.
func sendLocalNotification(title, body string) {
	go func() {
		msg := title
		if body != "" {
			msg = title + ": " + body
		}

		// tmux: broadcast to user-facing sessions (skip agent sessions named sw-*)
		out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
		if err == nil {
			for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if name == "" || strings.HasPrefix(name, "sw-") {
					continue
				}
				exec.Command("tmux", "display-message", "-t", name, "-d", "5000", msg).Run() //nolint:errcheck
			}
		}

		// notify-send: only if a D-Bus session or display is available
		if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" || os.Getenv("DISPLAY") != "" {
			args := []string{"-u", "normal", "-t", "8000", title}
			if body != "" {
				args = append(args, body)
			}
			exec.Command("notify-send", args...).Run() //nolint:errcheck
		}
	}()
}

// notifyAgentStuck sends a debounced local notification when an agent is stuck.
func notifyAgentStuck(sessionName, agentName, agentID string) {
	if !swarmNotifier.shouldNotify(agentID, "stuck") {
		return
	}
	sendLocalNotification("⚠ SwarmOps: agent stuck", fmt.Sprintf("%s / %s", sessionName, agentName))
}

// notifyAgentWaiting sends a debounced local notification when an agent is waiting.
func notifyAgentWaiting(sessionName, agentName, agentID string) {
	if !swarmNotifier.shouldNotify(agentID, "waiting") {
		return
	}
	sendLocalNotification("⏸ SwarmOps: agent waiting", fmt.Sprintf("%s / %s", sessionName, agentName))
}

// notifyTaskDone sends a debounced local notification when a task reaches 'done'.
func notifyTaskDone(sessionName, taskTitle, taskID string) {
	if !swarmNotifier.shouldNotify(taskID, "done") {
		return
	}
	sendLocalNotification("✓ SwarmOps: task done", fmt.Sprintf("%s / %s", sessionName, taskTitle))
}
