package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// -----------------
// Swarm Notifications (Telegram HITL)
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

// sendTelegramNotification fires a message to the Telegram bot configured in env vars.
// Requires TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID. Silent no-op if not configured.
func sendTelegramNotification(message string) {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")
	if token == "" || chatID == "" {
		return
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]string{
		"chat_id":    chatID,
		"text":       message,
		"parse_mode": "Markdown",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}

	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("swarm notify: telegram send error: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("swarm notify: telegram returned %d", resp.StatusCode)
	}
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

// notifyAgentStuck sends a debounced local + Telegram notification when an agent is stuck.
func notifyAgentStuck(sessionName, agentName, agentID string) {
	if !swarmNotifier.shouldNotify(agentID, "stuck") {
		return
	}
	sendLocalNotification("⚠ SwarmOps: agent stuck", fmt.Sprintf("%s / %s", sessionName, agentName))
	go sendTelegramNotification(fmt.Sprintf(
		"⚠️ *Swarm agent stuck*\n\nSession: `%s`\nAgent: *%s*\n\nThe agent may need input or has hit an error. Check the terminal.",
		sessionName, agentName,
	))
}

// notifyAgentWaiting sends a debounced local + Telegram notification when an agent is waiting.
func notifyAgentWaiting(sessionName, agentName, agentID string) {
	if !swarmNotifier.shouldNotify(agentID, "waiting") {
		return
	}
	sendLocalNotification("⏸ SwarmOps: agent waiting", fmt.Sprintf("%s / %s", sessionName, agentName))
	go sendTelegramNotification(fmt.Sprintf(
		"⏸️ *Swarm agent waiting*\n\nSession: `%s`\nAgent: *%s*\n\nThe agent is waiting for user input.",
		sessionName, agentName,
	))
}

// notifyTaskDone sends a debounced local + Telegram notification when a task reaches 'done'.
func notifyTaskDone(sessionName, taskTitle, taskID string) {
	if !swarmNotifier.shouldNotify(taskID, "done") {
		return
	}
	sendLocalNotification("✓ SwarmOps: task done", fmt.Sprintf("%s / %s", sessionName, taskTitle))
	go sendTelegramNotification(fmt.Sprintf(
		"✅ *Swarm task done*\n\nSession: `%s`\nTask: *%s*",
		sessionName, taskTitle,
	))
}
