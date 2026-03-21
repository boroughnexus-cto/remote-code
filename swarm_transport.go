package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"os"
	"os/exec"
	"time"
)

// TransportMode controls which delivery mechanism is active.
type TransportMode string

const (
	TransportTmux     TransportMode = "tmux"
	TransportChannels TransportMode = "channels"
	TransportShadow   TransportMode = "shadow"
	TransportCanary   TransportMode = "canary"
)

// ControlMessage is a server-to-agent message delivered via the active transport.
type ControlMessage struct {
	Content     string
	Priority    int           // 0=heartbeat, 1=budget-warn, 2=hitl-response, 3=task-brief
	TTL         time.Duration // 0 = no expiry; non-zero messages are dropped if stale
	RunID       string        // correlates to agent_runs.run_id
	EnqueuedAt  time.Time     // stamped by MessageDispatcher.Send(); zero until then
}

// isExpired reports whether the message has exceeded its TTL.
// Messages with priority >= 2 (HITL responses, task briefs) are never dropped.
// A zero EnqueuedAt or zero TTL means the message never expires.
func (m ControlMessage) isExpired() bool {
	if m.Priority >= 2 || m.TTL == 0 || m.EnqueuedAt.IsZero() {
		return false
	}
	return time.Since(m.EnqueuedAt) > m.TTL
}

// AgentTransport abstracts server→agent message delivery.
// TmuxTransport (Phase 1) wraps tmux send-keys.
// ChannelsTransport (Phase 3) uses Claude Code --channels SSE.
type AgentTransport interface {
	Send(ctx context.Context, agentID string, msg ControlMessage) error
	IsReady(agentID string) bool
	Mode() TransportMode
}

// swarmTransport is the active transport, initialized once in main().
// Parallel to the package-level `var database *sql.DB` pattern.
var swarmTransport AgentTransport

// SetTransportForTesting replaces swarmTransport in tests. Not for production use.
func SetTransportForTesting(t AgentTransport) {
	swarmTransport = t
}

// initTransport reads SWARMOPS_TRANSPORT and returns the appropriate transport.
func initTransport() AgentTransport {
	mode := TransportMode(getEnvOrDefault("SWARMOPS_TRANSPORT", string(TransportTmux)))
	tmux := &TmuxTransport{}
	switch mode {
	case TransportChannels, TransportShadow, TransportCanary:
		log.Printf("transport: %s mode", mode)
		channels := &ChannelsTransport{}
		return &MessageDispatcher{primary: channels, fallback: tmux, mode: mode}
	default:
		log.Printf("transport: tmux mode")
		return tmux
	}
}

// -----------------
// MessageDispatcher
// -----------------

// MessageDispatcher owns routing policy (fallback, shadow, canary).
// Transports handle mechanism only; policy lives here.
type MessageDispatcher struct {
	primary  AgentTransport // ChannelsTransport
	fallback AgentTransport // TmuxTransport — always available
	mode     TransportMode
}

func (d *MessageDispatcher) Mode() TransportMode    { return d.mode }
func (d *MessageDispatcher) IsReady(id string) bool { return d.primary.IsReady(id) || d.fallback.IsReady(id) }

func (d *MessageDispatcher) Send(ctx context.Context, agentID string, msg ControlMessage) error {
	// Stamp enqueue time if not already set (allows callers to pre-set it for replay).
	if msg.EnqueuedAt.IsZero() {
		msg.EnqueuedAt = time.Now()
	}
	// Drop expired messages before even attempting delivery.
	if msg.isExpired() {
		log.Printf("transport: dropped expired msg for %s (priority=%d ttl=%s age=%s)",
			agentID, msg.Priority, msg.TTL, time.Since(msg.EnqueuedAt).Round(time.Second))
		return nil
	}

	switch d.mode {
	case TransportShadow:
		// Primary path: tmux (real). Secondary: channels (observed only).
		// Use a background-derived context for the shadow so caller cancellation
		// does not abort the observation goroutine before it completes.
		err := d.fallback.Send(ctx, agentID, msg)
		go func() {
			shadowCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if perr := d.primary.Send(shadowCtx, agentID, msg); perr != nil {
				log.Printf("transport: shadow channels mismatch for %s: %v", agentID, perr)
			}
		}()
		return err

	case TransportCanary:
		// 20% of agents (by deterministic hash) use channels; rest use tmux.
		if canaryHash(agentID) < 0.2 && d.primary.IsReady(agentID) {
			return d.sendWithFallback(ctx, agentID, msg)
		}
		return d.fallback.Send(ctx, agentID, msg)

	default: // TransportChannels
		if d.primary.IsReady(agentID) {
			return d.sendWithFallback(ctx, agentID, msg)
		}
		return d.fallback.Send(ctx, agentID, msg)
	}
}

func (d *MessageDispatcher) sendWithFallback(ctx context.Context, agentID string, msg ControlMessage) error {
	if err := d.primary.Send(ctx, agentID, msg); err != nil {
		log.Printf("transport: channels send failed for %s: %v; falling back to tmux", agentID, err)
		return d.fallback.Send(ctx, agentID, msg)
	}
	return nil
}

// canaryHash maps agentID to [0, 1) using FNV-1a for deterministic, stable routing.
// Divides by MaxUint32+1 to guarantee the range is strictly [0, 1).
func canaryHash(agentID string) float64 {
	h := fnv.New32a()
	h.Write([]byte(agentID))
	return float64(h.Sum32()) / (float64(math.MaxUint32) + 1)
}

// -----------------
// TmuxTransport
// -----------------

// TmuxTransport delivers messages via `tmux send-keys -l`.
// This is the extracted body of the original injectToSwarmAgent().
type TmuxTransport struct{}

func (t *TmuxTransport) Mode() TransportMode { return TransportTmux }

// IsReady always returns true for tmux — the session may or may not be alive, but
// we don't check here; Send() returns an error if the session doesn't exist.
func (t *TmuxTransport) IsReady(_ string) bool { return true }

func (t *TmuxTransport) Send(ctx context.Context, agentID string, msg ControlMessage) error {
	var tmuxSession string
	err := database.QueryRowContext(ctx,
		"SELECT COALESCE(tmux_session, '') FROM swarm_agents WHERE id = ?",
		agentID,
	).Scan(&tmuxSession)
	if err != nil {
		return fmt.Errorf("agent not found: %v", err)
	}
	if tmuxSession == "" {
		return fmt.Errorf("agent is not spawned — spawn it first")
	}

	// -l sends text as literal input so tmux does not interpret tokens as key names
	// (e.g. "Up", "Enter", "C-c"). -- ends option parsing so text starting with
	// "-" is not treated as a tmux flag.
	// Use CommandContext so caller cancellation/timeout propagates to the subprocess.
	if out, err := exec.CommandContext(ctx, "tmux", "send-keys", "-t", tmuxSession, "-l", "--", msg.Content).CombinedOutput(); err != nil {
		return fmt.Errorf("tmux send-keys: %v: %s", err, out)
	}
	// Enter must be sent as a named key (not literal) to press the actual Return key.
	if out, err := exec.CommandContext(ctx, "tmux", "send-keys", "-t", tmuxSession, "Enter").CombinedOutput(); err != nil {
		return fmt.Errorf("tmux enter: %v: %s", err, out)
	}
	return nil
}

// -----------------
// Helpers
// -----------------

func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
