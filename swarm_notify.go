package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
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

// notifyAgentStuck sends a debounced Telegram notification when an agent is stuck.
func notifyAgentStuck(sessionName, agentName, agentID string) {
	if !swarmNotifier.shouldNotify(agentID, "stuck") {
		return
	}
	go sendTelegramNotification(fmt.Sprintf(
		"⚠️ *Swarm agent stuck*\n\nSession: `%s`\nAgent: *%s*\n\nThe agent may need input or has hit an error. Check the terminal.",
		sessionName, agentName,
	))
}

// notifyAgentWaiting sends a debounced Telegram notification when an agent is waiting.
func notifyAgentWaiting(sessionName, agentName, agentID string) {
	if !swarmNotifier.shouldNotify(agentID, "waiting") {
		return
	}
	go sendTelegramNotification(fmt.Sprintf(
		"⏸️ *Swarm agent waiting*\n\nSession: `%s`\nAgent: *%s*\n\nThe agent is waiting for user input.",
		sessionName, agentName,
	))
}
