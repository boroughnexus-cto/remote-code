package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// -----------------
// ChannelsTransport
// -----------------

// agentQueue holds the in-memory SSE message queue for one active agent.
// The done channel is closed (via closeOnce) to signal shutdown; q.ch is
// never closed, eliminating the Send-on-closed-channel panic risk.
type agentQueue struct {
	ch        chan ControlMessage // buffered; never closed directly
	runID     string
	done      chan struct{} // closed exactly once by closeOnce on shutdown
	closeOnce sync.Once
}

// ChannelsTransport delivers messages to Claude agents via SSE (Claude --channels).
// One agentQueue is created per agent spawn and closed on despawn.
type ChannelsTransport struct {
	queues sync.Map // agentID -> *agentQueue
}

func (ct *ChannelsTransport) Mode() TransportMode { return TransportChannels }

// IsReady returns true if there is an active (not shut down) queue for the agent.
func (ct *ChannelsTransport) IsReady(agentID string) bool {
	v, ok := ct.queues.Load(agentID)
	if !ok {
		return false
	}
	q := v.(*agentQueue)
	select {
	case <-q.done:
		return false
	default:
		return true
	}
}

// Send enqueues a message for SSE delivery.
// Non-blocking: returns an error (for fallback) if the queue is done/full.
// Uses a done channel — q.ch is never closed, preventing send-on-closed-channel panics.
func (ct *ChannelsTransport) Send(_ context.Context, agentID string, msg ControlMessage) error {
	v, ok := ct.queues.Load(agentID)
	if !ok {
		return fmt.Errorf("channels: no queue for agent %s", agentID)
	}
	q := v.(*agentQueue)
	select {
	case q.ch <- msg:
		return nil
	case <-q.done:
		return fmt.Errorf("channels: queue closed for agent %s", agentID)
	case <-time.After(100 * time.Millisecond):
		return fmt.Errorf("channels: queue full for agent %s (slow consumer)", agentID)
	}
}

// CreateQueue opens a new SSE queue for an agent run.
// If a queue already exists for the agent (e.g. restart), it is closed first.
// Called during agent spawn, before the tmux session is started.
func (ct *ChannelsTransport) CreateQueue(agentID, runID string) {
	newQ := &agentQueue{
		ch:    make(chan ControlMessage, 64),
		runID: runID,
		done:  make(chan struct{}),
	}
	if old, ok := ct.queues.LoadAndDelete(agentID); ok {
		old.(*agentQueue).closeOnce.Do(func() { close(old.(*agentQueue).done) })
	}
	ct.queues.Store(agentID, newQ)
}

// CloseQueue shuts down an agent's queue by closing its done channel.
// Safe to call multiple times (sync.Once). q.ch is never closed.
func (ct *ChannelsTransport) CloseQueue(agentID string) {
	v, ok := ct.queues.LoadAndDelete(agentID)
	if !ok {
		return
	}
	q := v.(*agentQueue)
	q.closeOnce.Do(func() { close(q.done) })
}

// validateChannelsToken checks the run_token query param against agent_runs.
// Returns (agentID, runID, queue, ok). Writes HTTP error on failure.
// Distinguishes DB errors (5xx) from auth/not-found failures (4xx).
func (ct *ChannelsTransport) validateChannelsToken(w http.ResponseWriter, r *http.Request) (agentID, runID string, q *agentQueue, ok bool) {
	agentID = r.PathValue("agentID")
	runID = r.PathValue("runID")
	token := r.URL.Query().Get("token")

	if agentID == "" || runID == "" || token == "" {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	var storedToken string
	err := database.QueryRowContext(r.Context(),
		"SELECT COALESCE(run_token, '') FROM agent_runs WHERE run_id = ? AND agent_id = ? AND ended_at IS NULL",
		runID, agentID,
	).Scan(&storedToken)
	if err == sql.ErrNoRows || storedToken == "" || storedToken != token {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if err != nil {
		log.Printf("channels: DB error validating token for agent %s: %v", agentID, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	v, exists := ct.queues.Load(agentID)
	if !exists {
		http.Error(w, "No active queue for agent", http.StatusNotFound)
		return
	}
	q = v.(*agentQueue)
	// Verify the in-memory queue belongs to this specific run (guards against rapid restarts).
	if q.runID != runID {
		http.Error(w, "No active queue for this run", http.StatusNotFound)
		return
	}
	ok = true
	return
}

// ServeSSE handles GET /mcp/channels/{agentID}/{runID}?token={runToken}.
// Implements the MCP SSE server transport:
//  1. Responds with text/event-stream
//  2. Sends "event: endpoint" pointing to the POST messages URL
//  3. Streams ControlMessages as MCP notifications/message events
func (ct *ChannelsTransport) ServeSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	agentID, runID, q, ok := ct.validateChannelsToken(w, r)
	if !ok {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Resume counter from Last-Event-ID if reconnecting.
	var lastEventID int64
	if s := r.Header.Get("Last-Event-ID"); s != "" {
		lastEventID, _ = strconv.ParseInt(s, 10, 64)
		log.Printf("channels: agent %s reconnected from event %d", agentID[:8], lastEventID)
	}

	// MCP SSE transport: send the POST endpoint URL as the first event.
	// Claude Code reads this and directs JSON-RPC POSTs there.
	token := r.URL.Query().Get("token")
	messagesURL := fmt.Sprintf("%s/mcp/channels/%s/%s/messages?token=%s",
		swarmAPIBase(), agentID, runID, token)
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", messagesURL)
	flusher.Flush()

	log.Printf("channels: agent %s SSE connected (run %s)", agentID[:8], runID[:8])

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case msg := <-q.ch:
			lastEventID++
			// Push as MCP channel notification so Claude Code injects it into the session.
			notification := mcpNotification("notifications/claude/channel", map[string]any{
				"content": msg.Content,
				"meta":    map[string]string{"source": "swarmops", "priority": notificationLevel(msg.Priority)},
			})
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", lastEventID, notification)
			flusher.Flush()
			// Record ack time for observability. Use background context so this
			// survives request cancellation; log errors rather than silently dropping.
			go func(rID string) {
				if _, err := database.ExecContext(context.Background(),
					"UPDATE agent_runs SET acked_at = unixepoch() WHERE run_id = ?", rID,
				); err != nil {
					log.Printf("channels: acked_at update failed for run %s: %v", rID, err)
				}
			}(runID)
		case <-q.done:
			// Queue shut down (agent despawned) — end SSE stream.
			return
		case <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			// Client disconnected — queue stays open for reconnect.
			log.Printf("channels: agent %s disconnected (queue preserved)", agentID[:8])
			return
		}
	}
}

// ServeMessages handles POST /mcp/channels/{agentID}/{runID}/messages?token={runToken}.
// Processes MCP JSON-RPC requests from Claude Code (initialize, tools/list, etc.).
func (ct *ChannelsTransport) ServeMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	_, _, _, ok := ct.validateChannelsToken(w, r)
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// Notifications (no id) require no response.
	if req.ID == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	var result any
	switch req.Method {
	case "initialize":
		result = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"experimental": map[string]any{"claude/channel": map[string]any{}},
			},
			"serverInfo": map[string]any{"name": "swarmops", "version": "1.0.0"},
		}
	case "tools/list":
		result = map[string]any{"tools": []any{}}
	case "resources/list":
		result = map[string]any{"resources": []any{}}
	case "prompts/list":
		result = map[string]any{"prompts": []any{}}
	default:
		result = map[string]any{}
	}

	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result":  result,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

// mcpNotification encodes a JSON-RPC 2.0 notification as a compact JSON string.
func mcpNotification(method string, params map[string]any) string {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	b, _ := json.Marshal(msg)
	return string(b)
}

// notificationLevel maps ControlMessage priority to MCP log level strings.
func notificationLevel(priority int) string {
	switch priority {
	case 0:
		return "debug" // heartbeat
	case 1:
		return "warning" // budget-warn
	case 2, 3:
		return "info" // hitl-response, task-brief
	default:
		return "info"
	}
}

// -----------------
// generateRunToken creates a unique run ID and a cryptographically random
// per-run authentication token for the SSE endpoint.
// Returns (runID, runToken).
// -----------------

func generateRunToken() (runID, runToken string) {
	idBytes := make([]byte, 16)
	tokenBytes := make([]byte, 32)
	rand.Read(idBytes)  //nolint:errcheck
	rand.Read(tokenBytes) //nolint:errcheck
	runID = base64.RawURLEncoding.EncodeToString(idBytes)
	runToken = base64.RawURLEncoding.EncodeToString(tokenBytes)
	return
}

// AgentLaunchConfig bundles the parameters needed to build a Claude launch command.
// Using a struct avoids parameter creep as new options are added (e.g. model selection).
type AgentLaunchConfig struct {
	AgentID                    string
	RunID                      string
	RunToken                   string
	ModelName                  string // optional; empty means use Claude's default
	AllowedTools               string // comma-separated tool names; empty = no restriction
	DisallowedTools            string // comma-separated tool names; empty = no restriction
	DangerouslySkipPermissions bool   // when false, agent runs without --dangerously-skip-permissions
}

// -----------------
// agentLaunchArgs returns the Claude launch command as a string slice.
// When channels transport is active, appends --channels <url>.
// Using a slice (not string concat) prevents shell injection from runID/token.
// ModelName is validated before storage (see isValidModelName) so it is safe to
// pass directly as a flag value here.
// -----------------

func agentLaunchArgs(cfg AgentLaunchConfig) []string {
	args := []string{"claude"}
	if cfg.DangerouslySkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	if cfg.ModelName != "" {
		args = append(args, "--model", cfg.ModelName)
	}
	if cfg.AllowedTools != "" {
		args = append(args, "--allowedTools", cfg.AllowedTools)
	}
	if cfg.DisallowedTools != "" {
		args = append(args, "--disallowedTools", cfg.DisallowedTools)
	}
	switch TransportMode(getEnvOrDefault("SWARMOPS_TRANSPORT", string(TransportTmux))) {
	case TransportChannels, TransportShadow, TransportCanary:
		// Write a per-agent MCP config so Claude can find the swarmops SSE endpoint.
		// --dangerously-load-development-channels is required because SwarmOps is not on
		// Claude's built-in channels allowlist; --channels only works for allowlisted servers.
		if cfgPath := writeMCPConfig(cfg.AgentID, cfg.RunID, cfg.RunToken); cfgPath != "" {
			args = append(args, "--mcp-config", cfgPath, "--dangerously-load-development-channels", "server:swarmops")
		}
	}
	return args
}

// writeMCPConfig writes a per-agent MCP config file and returns its path.
// The file is cleaned up by closeAgentRun via cleanupMCPConfig.
func writeMCPConfig(agentID, runID, runToken string) string {
	sseURL := fmt.Sprintf("%s/mcp/channels/%s/%s?token=%s",
		swarmAPIBase(), agentID, runID, runToken)
	cfg := fmt.Sprintf(`{"mcpServers":{"swarmops":{"url":%s,"type":"sse"}}}`,
		jsonStringLiteral(sseURL))
	path := fmt.Sprintf("/tmp/swarmops-%s-%s.json", agentID, runID)
	if err := os.WriteFile(path, []byte(cfg), 0600); err != nil {
		log.Printf("channels: failed to write MCP config for %s: %v", agentID, err)
		return ""
	}
	return path
}

// cleanupMCPConfig removes the per-agent MCP config file written by writeMCPConfig.
func cleanupMCPConfig(agentID, runID string) {
	path := fmt.Sprintf("/tmp/swarmops-%s-%s.json", agentID, runID)
	os.Remove(path) //nolint:errcheck
}

// jsonStringLiteral returns a JSON-encoded string literal (with double quotes).
func jsonStringLiteral(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// agentLaunchCmd joins agentLaunchArgs for tmux send-keys (single string arg).
// tmux takes the command as a single string; we join with spaces.
// All components are generated internally (no user-controlled shell input).
func agentLaunchCmd(cfg AgentLaunchConfig) string {
	args := agentLaunchArgs(cfg)
	cmd := ""
	for i, a := range args {
		if i > 0 {
			cmd += " "
		}
		cmd += a
	}
	return cmd
}

// recordAgentRun records the current agent spawn in agent_runs.
// Uses a transaction (update prior active run, insert new) because SQLite's
// ON CONFLICT cannot target partial unique indexes.
// Logs and continues on DB error (non-fatal).
func recordAgentRun(ctx context.Context, agentID, runID, runToken string) {
	mode := getEnvOrDefault("SWARMOPS_TRANSPORT", string(TransportTmux))
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("swarm: warning — could not begin agent_run tx: %v", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck
	// End any prior active run for this agent before recording the new one.
	if _, err = tx.ExecContext(ctx,
		"UPDATE agent_runs SET ended_at = unixepoch() WHERE agent_id = ? AND ended_at IS NULL",
		agentID,
	); err != nil {
		log.Printf("swarm: warning — could not end prior agent_run: %v", err)
		return
	}
	if _, err = tx.ExecContext(ctx,
		"INSERT INTO agent_runs (run_id, agent_id, transport_mode, run_token) VALUES (?, ?, ?, ?)",
		runID, agentID, mode, runToken,
	); err != nil {
		log.Printf("swarm: warning — could not insert agent_run: %v", err)
		return
	}
	if err = tx.Commit(); err != nil {
		log.Printf("swarm: warning — could not commit agent_run: %v", err)
	}
}

// closeAgentRun marks the agent_runs row as ended and closes its SSE queue.
func closeAgentRun(ctx context.Context, agentID string) {
	// Fetch runID first so we can clean up the temp MCP config file.
	var runID string
	if err := database.QueryRowContext(ctx,
		"SELECT COALESCE(run_id, '') FROM agent_runs WHERE agent_id = ? AND ended_at IS NULL",
		agentID,
	).Scan(&runID); err != nil && err != sql.ErrNoRows {
		log.Printf("channels: warning — could not fetch run_id for agent %s cleanup: %v", agentID, err)
	}
	database.ExecContext(ctx, //nolint:errcheck
		"UPDATE agent_runs SET ended_at = unixepoch() WHERE agent_id = ? AND ended_at IS NULL",
		agentID,
	)
	if ct := getChannelsTransport(); ct != nil {
		ct.CloseQueue(agentID)
	}
	if runID != "" {
		cleanupMCPConfig(agentID, runID)
	}
}

// getChannelsTransport returns the ChannelsTransport if one is active, else nil.
func getChannelsTransport() *ChannelsTransport {
	switch t := swarmTransport.(type) {
	case *MessageDispatcher:
		if ct, ok := t.primary.(*ChannelsTransport); ok {
			return ct
		}
	case *ChannelsTransport:
		return t
	}
	return nil
}
