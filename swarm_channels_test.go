package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// -----------------
// ChannelsTransport tests
// -----------------

func TestChannelsTransport_CreateAndClose(t *testing.T) {
	ct := &ChannelsTransport{}

	ct.CreateQueue("agent-1", "run-1")
	if !ct.IsReady("agent-1") {
		t.Error("expected IsReady=true after CreateQueue")
	}

	ct.CloseQueue("agent-1")
	if ct.IsReady("agent-1") {
		t.Error("expected IsReady=false after CloseQueue")
	}
}

func TestChannelsTransport_SendAndReceive(t *testing.T) {
	ct := &ChannelsTransport{}
	ct.CreateQueue("agent-1", "run-1")
	defer ct.CloseQueue("agent-1")

	msg := ControlMessage{Content: "hello from server", Priority: 1}
	if err := ct.Send(context.Background(), "agent-1", msg); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Drain the queue directly to verify delivery.
	v, _ := ct.queues.Load("agent-1")
	q := v.(*agentQueue)
	select {
	case got := <-q.ch:
		if got.Content != msg.Content {
			t.Errorf("got %q, want %q", got.Content, msg.Content)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("message not delivered within 100ms")
	}
}

func TestChannelsTransport_SendNoQueue(t *testing.T) {
	ct := &ChannelsTransport{}
	err := ct.Send(context.Background(), "missing-agent", ControlMessage{Content: "x"})
	if err == nil {
		t.Error("expected error when no queue exists")
	}
}

func TestChannelsTransport_SendAfterClose(t *testing.T) {
	ct := &ChannelsTransport{}
	ct.CreateQueue("agent-1", "run-1")
	ct.CloseQueue("agent-1")

	err := ct.Send(context.Background(), "agent-1", ControlMessage{Content: "x"})
	if err == nil {
		t.Error("expected error when sending to closed queue")
	}
}

func TestChannelsTransport_QueueFull(t *testing.T) {
	ct := &ChannelsTransport{}
	ct.CreateQueue("agent-1", "run-1")
	defer ct.CloseQueue("agent-1")

	// Fill the buffer (size 64).
	for i := 0; i < 64; i++ {
		if err := ct.Send(context.Background(), "agent-1", ControlMessage{Content: "x"}); err != nil {
			t.Fatalf("unexpected error filling buffer at i=%d: %v", i, err)
		}
	}

	// 65th send should fail (non-blocking, returns error for fallback).
	if err := ct.Send(context.Background(), "agent-1", ControlMessage{Content: "overflow"}); err == nil {
		t.Error("expected error on full queue, got nil")
	}
}

func TestChannelsTransport_CloseIdempotent(t *testing.T) {
	ct := &ChannelsTransport{}
	ct.CreateQueue("agent-1", "run-1")
	ct.CloseQueue("agent-1")
	// Second close should not panic.
	ct.CloseQueue("agent-1")
}

// -----------------
// MessageDispatcher tests
// -----------------

func TestMessageDispatcher_TmuxFallback(t *testing.T) {
	primary := &mockTransport{ready: false} // channels not ready
	fallback := &mockTransport{ready: true}

	d := &MessageDispatcher{primary: primary, fallback: fallback, mode: TransportChannels}
	msg := ControlMessage{Content: "test"}

	if err := d.Send(context.Background(), "agent-1", msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fallback.sendCount() != 1 {
		t.Errorf("expected fallback to receive 1 message, got %d", fallback.sendCount())
	}
	if primary.sendCount() != 0 {
		t.Errorf("expected primary to receive 0 messages (not ready), got %d", primary.sendCount())
	}
}

func TestMessageDispatcher_ChannelsWhenReady(t *testing.T) {
	primary := &mockTransport{ready: true}
	fallback := &mockTransport{ready: true}

	d := &MessageDispatcher{primary: primary, fallback: fallback, mode: TransportChannels}
	msg := ControlMessage{Content: "via channels"}

	if err := d.Send(context.Background(), "agent-1", msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if primary.sendCount() != 1 {
		t.Errorf("expected primary to receive 1 message, got %d", primary.sendCount())
	}
	if fallback.sendCount() != 0 {
		t.Errorf("expected fallback to receive 0 messages, got %d", fallback.sendCount())
	}
}

func TestMessageDispatcher_FallbackOnPrimaryError(t *testing.T) {
	primary := &mockTransport{
		ready:  true,
		sendFn: func(_ context.Context, _ string, _ ControlMessage) error { return context.DeadlineExceeded },
	}
	fallback := &mockTransport{ready: true}

	d := &MessageDispatcher{primary: primary, fallback: fallback, mode: TransportChannels}

	if err := d.Send(context.Background(), "agent-1", ControlMessage{Content: "x"}); err != nil {
		t.Fatalf("unexpected error from fallback: %v", err)
	}

	if fallback.sendCount() != 1 {
		t.Errorf("expected fallback to receive 1 message after primary failure, got %d", fallback.sendCount())
	}
}

func TestMessageDispatcher_ShadowMode(t *testing.T) {
	primary := &mockTransport{ready: true}
	fallback := &mockTransport{ready: true}

	d := &MessageDispatcher{primary: primary, fallback: fallback, mode: TransportShadow}
	d.Send(context.Background(), "agent-1", ControlMessage{Content: "shadow"}) //nolint:errcheck

	if fallback.sendCount() != 1 {
		t.Errorf("shadow: fallback (real path) should receive 1 message, got %d", fallback.sendCount())
	}
	// Give the goroutine time to run.
	time.Sleep(20 * time.Millisecond)
	if primary.sendCount() != 1 {
		t.Errorf("shadow: primary (observed path) should receive 1 message, got %d", primary.sendCount())
	}
}

// -----------------
// canaryHash tests
// -----------------

func TestCanaryHash_Deterministic(t *testing.T) {
	h1 := canaryHash("agent-abc-123")
	h2 := canaryHash("agent-abc-123")
	if h1 != h2 {
		t.Errorf("canaryHash not deterministic: %f != %f", h1, h2)
	}
}

func TestCanaryHash_Range(t *testing.T) {
	for _, id := range []string{"a", "agent-1", "z123456789abcdef"} {
		h := canaryHash(id)
		if h < 0 || h >= 1.0 {
			t.Errorf("canaryHash(%q) = %f, want [0, 1)", id, h)
		}
	}
}

func TestCanaryHash_Distribution(t *testing.T) {
	// Rough check: ~20% of 1000 agents should hash below 0.2.
	below := 0
	for i := 0; i < 1000; i++ {
		id := strings.Repeat("x", i%16+1) + string(rune('a'+i%26))
		if canaryHash(id) < 0.2 {
			below++
		}
	}
	// Allow wide tolerance: just sanity-check it's not 0% or 100%.
	if below < 50 || below > 400 {
		t.Errorf("canaryHash distribution looks wrong: %d/1000 below 0.2 (want ~200)", below)
	}
}

// -----------------
// generateRunToken tests
// -----------------

func TestGenerateRunToken_Unique(t *testing.T) {
	id1, tok1 := generateRunToken()
	id2, tok2 := generateRunToken()

	if id1 == id2 {
		t.Error("run IDs should be unique")
	}
	if tok1 == tok2 {
		t.Error("run tokens should be unique")
	}
}

func TestGenerateRunToken_NonEmpty(t *testing.T) {
	id, tok := generateRunToken()
	if id == "" || tok == "" {
		t.Error("run ID and token must be non-empty")
	}
	// Tokens should be long enough to be unguessable.
	if len(tok) < 30 {
		t.Errorf("run token too short: %q", tok)
	}
}

// -----------------
// agentLaunchArgs tests
// -----------------

func TestAgentLaunchArgs_TmuxMode(t *testing.T) {
	t.Setenv("SWARMOPS_TRANSPORT", "tmux")
	args := agentLaunchArgs("agent-1", "run-1", "tok-1")

	if len(args) != 2 {
		t.Errorf("tmux mode: expected 2 args, got %v", args)
	}
	if args[0] != "claude" {
		t.Errorf("first arg should be 'claude', got %q", args[0])
	}
}

func TestAgentLaunchArgs_ChannelsMode(t *testing.T) {
	t.Setenv("SWARMOPS_TRANSPORT", "channels")
	t.Setenv("SWARM_API_BASE_URL", "http://localhost:8080")

	args := agentLaunchArgs("agent-1", "run-abc", "tok-xyz")

	if len(args) != 4 {
		t.Errorf("channels mode: expected 4 args, got %v", args)
	}
	if args[2] != "--channels" {
		t.Errorf("third arg should be '--channels', got %q", args[2])
	}
	if !strings.Contains(args[3], "agent-1") || !strings.Contains(args[3], "run-abc") {
		t.Errorf("channels URL missing agentID/runID: %q", args[3])
	}
	if !strings.Contains(args[3], "tok-xyz") {
		t.Errorf("channels URL missing token: %q", args[3])
	}
}

func TestAgentLaunchCmd_NoSpacesInComponents(t *testing.T) {
	// runID and token are base64url-encoded — no spaces. Verify join is safe.
	id, tok := generateRunToken()
	cmd := agentLaunchCmd("agent-123", id, tok)
	// Should never have adjacent spaces (which would indicate empty components).
	if strings.Contains(cmd, "  ") {
		t.Errorf("double space in launch cmd: %q", cmd)
	}
}
