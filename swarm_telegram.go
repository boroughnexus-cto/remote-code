package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// -----------------
// TelegramRouter
// -----------------

// TelegramRouter routes Telegram replies to waiting agent escalations.
// It holds a bounded work queue so a burst of webhook calls cannot OOM.
// Architecture:
//   - SubmitEscalation: sends question via Bot API, records in agent_escalations
//   - HandleWebhook:    validates optional secret, enqueues incoming Update
//   - runWorker:        drains the queue, correlates replies → swarmTransport.Send
//   - runReaper:        every minute, expires unanswered escalations + notifies agents
type TelegramRouter struct {
	botToken string
	chatID   int64
	updates  chan tgUpdate // bounded incoming queue; worker drains it
	stopc    chan struct{}
	stopOnce sync.Once
}

// tgUpdate is a minimal Telegram Bot API Update payload.
// Only fields needed for reply routing are decoded.
type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message,omitempty"`
}

type tgMessage struct {
	MessageID int64      `json:"message_id"`
	Chat      tgChat     `json:"chat"`
	Text      string     `json:"text,omitempty"`
	ReplyTo   *tgMessage `json:"reply_to_message,omitempty"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

// telegramRouter is the package-level singleton, initialised in main().
// Nil when TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID are not set.
var telegramRouter *TelegramRouter

// initTelegramRouter creates and starts a TelegramRouter from env.
// Returns nil (with a log line) when the required env vars are absent.
func initTelegramRouter() *TelegramRouter {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatIDStr := os.Getenv("TELEGRAM_CHAT_ID")
	if token == "" || chatIDStr == "" {
		log.Printf("telegram: TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID not set — escalation router disabled")
		return nil
	}
	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		log.Printf("telegram: invalid TELEGRAM_CHAT_ID %q: %v — escalation router disabled", chatIDStr, err)
		return nil
	}
	tr := &TelegramRouter{
		botToken: token,
		chatID:   chatID,
		updates:  make(chan tgUpdate, 256),
		stopc:    make(chan struct{}),
	}
	go tr.runWorker()
	go tr.runReaper()
	log.Printf("telegram: escalation router started (chat_id=%d)", chatID)
	return tr
}

// Stop shuts down the worker and reaper goroutines. Safe to call multiple times.
func (tr *TelegramRouter) Stop() {
	tr.stopOnce.Do(func() { close(tr.stopc) })
}

// -----------------
// Webhook handler
// -----------------

// HandleWebhook handles POST /api/telegram/webhook.
// Auth: if TELEGRAM_WEBHOOK_SECRET is set, validates X-Telegram-Bot-Api-Secret-Token.
// Always returns 200 to Telegram (Telegram re-delivers on non-2xx responses).
func (tr *TelegramRouter) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// Optional webhook secret validation.
	if secret := os.Getenv("TELEGRAM_WEBHOOK_SECRET"); secret != "" {
		if r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != secret {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		w.WriteHeader(http.StatusOK) // don't let Telegram retry on read errors
		return
	}

	var update tgUpdate
	if err := json.Unmarshal(body, &update); err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Non-blocking enqueue: drop if the worker is saturated.
	select {
	case tr.updates <- update:
	default:
		log.Printf("telegram: webhook queue full, dropping update %d", update.UpdateID)
	}
	w.WriteHeader(http.StatusOK)
}

// -----------------
// Worker
// -----------------

func (tr *TelegramRouter) runWorker() {
	for {
		select {
		case update := <-tr.updates:
			tr.processUpdate(context.Background(), update)
		case <-tr.stopc:
			return
		}
	}
}

// processUpdate handles one incoming Telegram update.
// If the message is a reply to an open escalation, it routes the answer to the agent.
func (tr *TelegramRouter) processUpdate(ctx context.Context, update tgUpdate) {
	msg := update.Message
	if msg == nil || msg.ReplyTo == nil {
		return // not a reply — nothing to route
	}

	answer := strings.TrimSpace(msg.Text)
	if answer == "" {
		return // no text content
	}

	replyToMsgID := msg.ReplyTo.MessageID
	chatID := msg.Chat.ID

	// Look up the open escalation keyed by (tg_chat_id, tg_message_id).
	var escID, agentID, taskID, sessionID string
	err := database.QueryRowContext(ctx,
		`SELECT e.id, e.agent_id, COALESCE(e.task_id,''), COALESCE(a.session_id,'')
		 FROM agent_escalations e
		 JOIN swarm_agents a ON a.id = e.agent_id
		 WHERE e.tg_chat_id = ? AND e.tg_message_id = ? AND e.answered_at IS NULL`,
		chatID, replyToMsgID,
	).Scan(&escID, &agentID, &taskID, &sessionID)
	if err != nil {
		// sql.ErrNoRows is normal (reply to a non-escalation message).
		return
	}

	// Atomic claim: UPDATE WHERE answered_at IS NULL — only the first responder wins.
	res, err := database.ExecContext(ctx,
		`UPDATE agent_escalations SET answered_at = unixepoch(), answer = ?
		 WHERE id = ? AND answered_at IS NULL`,
		answer, escID,
	)
	if err != nil {
		log.Printf("telegram: mark escalation %s answered: %v", escID, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		log.Printf("telegram: escalation %s already answered (concurrent responder)", escID)
		return
	}

	// Route the answer to the waiting agent.
	content := fmt.Sprintf("## Human Response\n\n%s", answer)
	if err := swarmTransport.Send(ctx, agentID, ControlMessage{
		Content:  content,
		Priority: 2, // hitl-response — never dropped by TTL
	}); err != nil {
		log.Printf("telegram: inject answer to agent %s: %v", truncateID(agentID), err)
		// Answer is persisted in DB even if delivery fails; agent can be re-injected manually.
	}

	// Transition task back to running if it was blocked waiting for human input.
	if taskID != "" {
		var curStage string
		database.QueryRowContext(ctx, "SELECT COALESCE(stage,'') FROM swarm_tasks WHERE id=?", taskID).Scan(&curStage) //nolint:errcheck
		if curStage == "blocked" || curStage == "needs_human" {
			transitionTask(ctx, taskID, "running") //nolint:errcheck
		}
	}

	// Update agent event timestamp so the TUI refreshes promptly.
	database.ExecContext(ctx, //nolint:errcheck
		"UPDATE swarm_agents SET last_event_ts = ? WHERE id = ?",
		time.Now().Unix(), agentID,
	)
	if sessionID != "" {
		swarmBroadcaster.schedule(sessionID)
	}

	// Acknowledge delivery back to the human via Telegram.
	tr.sendAck(chatID, "✅ Response delivered to agent.")

	log.Printf("telegram: escalation %s answered by human → agent %s", escID, truncateID(agentID))
}

// -----------------
// Reaper
// -----------------

func (tr *TelegramRouter) runReaper() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			tr.reapExpired(context.Background())
		case <-tr.stopc:
			return
		}
	}
}

// reapExpired finds escalations past their deadline, injects a timeout notice
// to each agent, and marks them as answered (answer = "__timeout__").
func (tr *TelegramRouter) reapExpired(ctx context.Context) {
	rows, err := database.QueryContext(ctx,
		`SELECT id, agent_id, COALESCE(task_id,'')
		 FROM agent_escalations
		 WHERE answered_at IS NULL AND expires_at <= unixepoch()`,
	)
	if err != nil {
		log.Printf("telegram: reaper query: %v", err)
		return
	}
	defer rows.Close()

	type expiredRow struct{ id, agentID, taskID string }
	var expired []expiredRow
	for rows.Next() {
		var e expiredRow
		if err := rows.Scan(&e.id, &e.agentID, &e.taskID); err != nil {
			log.Printf("telegram: reaper scan: %v", err)
			continue
		}
		expired = append(expired, e)
	}
	if err := rows.Err(); err != nil {
		log.Printf("telegram: reaper rows error: %v", err)
		return
	}
	rows.Close() // close before further DB writes

	for _, e := range expired {
		// Atomic claim (same guard as processUpdate).
		res, err := database.ExecContext(ctx,
			`UPDATE agent_escalations SET answered_at = unixepoch(), answer = '__timeout__'
			 WHERE id = ? AND answered_at IS NULL`,
			e.id,
		)
		if err != nil {
			log.Printf("telegram: reaper mark expired %s: %v", e.id, err)
			continue
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // concurrently answered
		}

		// Notify the agent that the window has passed.
		swarmTransport.Send(ctx, e.agentID, ControlMessage{ //nolint:errcheck
			Content:  "## Escalation Timed Out\n\nYour question was not answered within the allotted time. Please proceed with your best judgement or try a different approach.",
			Priority: 2,
		})
		log.Printf("telegram: reaper expired escalation %s for agent %s", e.id, truncateID(e.agentID))
	}
}

// -----------------
// SubmitEscalation
// -----------------

// SubmitEscalation sends the question to the configured Telegram chat,
// records the escalation in agent_escalations (keyed by chat_id + message_id),
// and returns the new escalation ID.
// The TTL controls when the reaper marks the escalation as expired.
func (tr *TelegramRouter) SubmitEscalation(ctx context.Context, agentID, taskID, question string, ttl time.Duration) (string, error) {
	// Build the message text for the human.
	text := formatEscalationText(agentID, taskID, question)

	msgID, err := tr.sendMessage(tr.chatID, text)
	if err != nil {
		return "", fmt.Errorf("telegram send: %w", err)
	}

	escID := newEscalationID()
	expiresAt := time.Now().Add(ttl).Unix()

	var taskIDArg interface{} = taskID
	if taskID == "" {
		taskIDArg = nil // store NULL for absent task
	}

	_, err = database.ExecContext(ctx,
		`INSERT INTO agent_escalations (id, agent_id, task_id, question, tg_chat_id, tg_message_id, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		escID, agentID, taskIDArg, question, tr.chatID, msgID, expiresAt,
	)
	if err != nil {
		return "", fmt.Errorf("insert escalation: %w", err)
	}

	log.Printf("telegram: escalation %s submitted for agent %s (msg_id=%d, expires_in=%v)",
		escID, truncateID(agentID), msgID, ttl)
	return escID, nil
}

// -----------------
// Telegram Bot API helpers
// -----------------

// sendMessage posts a plain-text message to chatID and returns the message_id.
func (tr *TelegramRouter) sendMessage(chatID int64, text string) (int64, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", tr.botToken)
	payload, _ := json.Marshal(map[string]any{
		"chat_id": chatID,
		"text":    text,
	})
	resp, err := http.Post(url, "application/json", bytes.NewReader(payload)) //nolint:noctx
	if err != nil {
		return 0, fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
		Description string `json:"description"` // populated on ok=false
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}
	if !result.OK {
		return 0, fmt.Errorf("telegram API: %s", result.Description)
	}
	return result.Result.MessageID, nil
}

// sendAck sends a brief acknowledgement to the given chat. Errors are logged but not fatal.
func (tr *TelegramRouter) sendAck(chatID int64, text string) {
	if _, err := tr.sendMessage(chatID, text); err != nil {
		log.Printf("telegram: send ack: %v", err)
	}
}

// -----------------
// Helpers
// -----------------

// newEscalationID generates a unique ID for an agent_escalations row.
func newEscalationID() string {
	b := make([]byte, 8)
	rand.Read(b) //nolint:errcheck
	return "esc-" + base64.RawURLEncoding.EncodeToString(b)
}

// formatEscalationText builds the human-readable Telegram message for an escalation.
func formatEscalationText(agentID, taskID, question string) string {
	var sb strings.Builder
	sb.WriteString("🤔 Agent needs help\n\n")
	fmt.Fprintf(&sb, "Agent: %s\n", truncateID(agentID))
	if taskID != "" {
		fmt.Fprintf(&sb, "Task:  %s\n", truncateID(taskID))
	}
	sb.WriteString("\n")
	sb.WriteString(question)
	sb.WriteString("\n\n")
	sb.WriteString("Reply to this message to respond.")
	return sb.String()
}

// truncateID returns the first 8 chars of an ID (safe for logging/display).
func truncateID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
