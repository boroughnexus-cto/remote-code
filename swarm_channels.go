package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// -----------------
// ChannelsTransport
// -----------------

// agentQueue holds the in-memory SSE message queue for one active agent.
type agentQueue struct {
	ch    chan ControlMessage // buffered; bounded send avoids blocking callers
	runID string
	mu    sync.Mutex
	closed bool
}

// ChannelsTransport delivers messages to Claude agents via SSE (Claude --channels).
// One agentQueue is created per agent spawn and closed on despawn.
type ChannelsTransport struct {
	queues sync.Map // agentID -> *agentQueue
}

func (ct *ChannelsTransport) Mode() TransportMode { return TransportChannels }

// IsReady returns true if there is an active (not closed) queue for the agent.
func (ct *ChannelsTransport) IsReady(agentID string) bool {
	v, ok := ct.queues.Load(agentID)
	if !ok {
		return false
	}
	q := v.(*agentQueue)
	q.mu.Lock()
	defer q.mu.Unlock()
	return !q.closed
}

// Send enqueues a message for SSE delivery.
// Non-blocking: returns an error (for fallback) if the queue is full or the
// agent has disconnected.
func (ct *ChannelsTransport) Send(_ context.Context, agentID string, msg ControlMessage) error {
	v, ok := ct.queues.Load(agentID)
	if !ok {
		return fmt.Errorf("channels: no queue for agent %s", agentID)
	}
	q := v.(*agentQueue)
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return fmt.Errorf("channels: queue closed for agent %s", agentID)
	}
	q.mu.Unlock()

	select {
	case q.ch <- msg:
		return nil
	case <-time.After(100 * time.Millisecond):
		return fmt.Errorf("channels: queue full for agent %s (slow consumer)", agentID)
	}
}

// CreateQueue opens a new SSE queue for an agent run.
// Called during agent spawn, before the tmux session is started.
func (ct *ChannelsTransport) CreateQueue(agentID, runID string) {
	q := &agentQueue{
		ch:    make(chan ControlMessage, 64),
		runID: runID,
	}
	ct.queues.Store(agentID, q)
}

// CloseQueue drains and removes the agent's queue.
// Called during agent despawn.
func (ct *ChannelsTransport) CloseQueue(agentID string) {
	v, ok := ct.queues.LoadAndDelete(agentID)
	if !ok {
		return
	}
	q := v.(*agentQueue)
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.ch)
	}
}

// ServeSSE handles GET /mcp/channels/{agentID}/{runID}?token={runToken}.
// Claude Code connects here when launched with --channels <url>.
// Auth: run_token query param validated against agent_runs.run_token in DB.
func (ct *ChannelsTransport) ServeSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	agentID := r.PathValue("agentID")
	runID := r.PathValue("runID")
	token := r.URL.Query().Get("token")

	if agentID == "" || runID == "" || token == "" {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// Validate run_token against DB (loopback assumption + token provides auth).
	var storedToken string
	err := database.QueryRowContext(r.Context(),
		"SELECT COALESCE(run_token, '') FROM agent_runs WHERE run_id = ? AND agent_id = ? AND ended_at IS NULL",
		runID, agentID,
	).Scan(&storedToken)
	if err != nil || storedToken == "" || storedToken != token {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	v, ok := ct.queues.Load(agentID)
	if !ok {
		http.Error(w, "No active queue for agent", http.StatusNotFound)
		return
	}
	q := v.(*agentQueue)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Resume from Last-Event-ID if reconnecting.
	var lastEventID int64
	if s := r.Header.Get("Last-Event-ID"); s != "" {
		lastEventID, _ = strconv.ParseInt(s, 10, 64)
		// Note: simple replay not implemented yet; acknowledge reconnect.
		log.Printf("channels: agent %s reconnected from event %d", agentID[:8], lastEventID)
	}

	log.Printf("channels: agent %s connected (run %s)", agentID[:8], runID[:8])

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case msg, open := <-q.ch:
			if !open {
				// Queue closed (agent despawned) — end SSE stream.
				return
			}
			lastEventID++
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", lastEventID, msg.Content)
			flusher.Flush()
			// Record ack time for observability (fire-and-forget).
			go database.Exec( //nolint:errcheck
				"UPDATE agent_runs SET acked_at = unixepoch() WHERE run_id = ?", runID,
			)
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

// -----------------
// agentLaunchArgs returns the Claude launch command as a string slice.
// When channels transport is active, appends --channels <url>.
// Using a slice (not string concat) prevents shell injection from runID/token.
// -----------------

func agentLaunchArgs(agentID, runID, runToken string) []string {
	args := []string{"claude", "--dangerously-skip-permissions"}
	switch TransportMode(getEnvOrDefault("SWARMOPS_TRANSPORT", string(TransportTmux))) {
	case TransportChannels, TransportShadow, TransportCanary:
		channelsURL := fmt.Sprintf("%s/mcp/channels/%s/%s?token=%s",
			swarmAPIBase(), agentID, runID, runToken)
		args = append(args, "--channels", channelsURL)
	}
	return args
}

// agentLaunchCmd joins agentLaunchArgs for tmux send-keys (single string arg).
// tmux takes the command as a single string; we join with spaces.
// All components are generated internally (no user-controlled shell input).
func agentLaunchCmd(agentID, runID, runToken string) string {
	args := agentLaunchArgs(agentID, runID, runToken)
	cmd := ""
	for i, a := range args {
		if i > 0 {
			cmd += " "
		}
		cmd += a
	}
	return cmd
}

// recordAgentRun inserts an agent_runs row for the current spawn.
// Logs and continues on DB error (non-fatal).
func recordAgentRun(ctx context.Context, agentID, runID, runToken string) {
	mode := getEnvOrDefault("SWARMOPS_TRANSPORT", string(TransportTmux))
	if _, err := database.ExecContext(ctx,
		`INSERT INTO agent_runs (run_id, agent_id, transport_mode, run_token) VALUES (?, ?, ?, ?)
         ON CONFLICT(agent_id) DO UPDATE SET
             run_id = excluded.run_id,
             transport_mode = excluded.transport_mode,
             run_token = excluded.run_token,
             started_at = unixepoch(),
             acked_at = NULL,
             ended_at = NULL`,
		runID, agentID, mode, runToken,
	); err != nil {
		log.Printf("swarm: warning — could not record agent_run: %v", err)
	}
}

// closeAgentRun marks the agent_runs row as ended and closes its SSE queue.
func closeAgentRun(ctx context.Context, agentID string) {
	database.ExecContext(ctx, //nolint:errcheck
		"UPDATE agent_runs SET ended_at = unixepoch() WHERE agent_id = ? AND ended_at IS NULL",
		agentID,
	)
	if ct := getChannelsTransport(); ct != nil {
		ct.CloseQueue(agentID)
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
