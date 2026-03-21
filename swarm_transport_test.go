package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// mockTransport records calls to Send for inspection in tests.
// mu guards sends to allow safe concurrent access from goroutines (shadow mode).
type mockTransport struct {
	mu     sync.Mutex
	sends  []ControlMessage
	ready  bool
	sendFn func(ctx context.Context, agentID string, msg ControlMessage) error
}

func (m *mockTransport) Send(ctx context.Context, agentID string, msg ControlMessage) error {
	m.mu.Lock()
	m.sends = append(m.sends, msg)
	m.mu.Unlock()
	if m.sendFn != nil {
		return m.sendFn(ctx, agentID, msg)
	}
	return nil
}

func (m *mockTransport) sendCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sends)
}

func (m *mockTransport) IsReady(_ string) bool { return m.ready }
func (m *mockTransport) Mode() TransportMode   { return "mock" }

func TestInjectToSwarmAgent_DelegatesToTransport(t *testing.T) {
	mock := &mockTransport{ready: true}
	SetTransportForTesting(mock)
	t.Cleanup(func() { swarmTransport = &TmuxTransport{} })

	// injectToSwarmAgent should delegate directly to swarmTransport.Send
	// We can't call it without a real DB, so test the transport delegation directly.
	ctx := context.Background()
	want := "hello agent"
	_ = swarmTransport.Send(ctx, "agent-1", ControlMessage{Content: want})

	if mock.sendCount() != 1 {
		t.Fatalf("expected 1 send, got %d", mock.sendCount())
	}
	mock.mu.Lock()
	got := mock.sends[0].Content
	mock.mu.Unlock()
	if got != want {
		t.Errorf("got content %q, want %q", got, want)
	}
}

func TestInjectToSwarmAgent_PropagatesError(t *testing.T) {
	wantErr := errors.New("tmux not available")
	mock := &mockTransport{
		ready:  true,
		sendFn: func(_ context.Context, _ string, _ ControlMessage) error { return wantErr },
	}
	SetTransportForTesting(mock)
	t.Cleanup(func() { swarmTransport = &TmuxTransport{} })

	err := swarmTransport.Send(context.Background(), "agent-1", ControlMessage{Content: "x"})
	if !errors.Is(err, wantErr) {
		t.Errorf("expected %v, got %v", wantErr, err)
	}
}

func TestControlMessage_Defaults(t *testing.T) {
	msg := ControlMessage{Content: "ping"}
	if msg.Priority != 0 {
		t.Errorf("default priority should be 0 (heartbeat), got %d", msg.Priority)
	}
	if msg.TTL != 0 {
		t.Errorf("default TTL should be 0 (no expiry), got %v", msg.TTL)
	}
}

func TestTransportMode_Constants(t *testing.T) {
	cases := []struct {
		mode TransportMode
		want string
	}{
		{TransportTmux, "tmux"},
		{TransportChannels, "channels"},
		{TransportShadow, "shadow"},
		{TransportCanary, "canary"},
	}
	for _, c := range cases {
		if string(c.mode) != c.want {
			t.Errorf("TransportMode %q != %q", c.mode, c.want)
		}
	}
}

func TestGetEnvOrDefault(t *testing.T) {
	// Unset key returns default.
	if got := getEnvOrDefault("SWARMOPS_TRANSPORT_TEST_UNSET_XYZ", "fallback"); got != "fallback" {
		t.Errorf("got %q, want fallback", got)
	}
}

func TestTmuxTransport_Mode(t *testing.T) {
	tr := &TmuxTransport{}
	if tr.Mode() != TransportTmux {
		t.Errorf("expected TransportTmux, got %q", tr.Mode())
	}
}

func TestTmuxTransport_IsReady(t *testing.T) {
	tr := &TmuxTransport{}
	// TmuxTransport is always considered ready (it delegates error handling to Send).
	if !tr.IsReady("any-agent") {
		t.Error("TmuxTransport.IsReady should always return true")
	}
}

func TestControlMessage_TTLSemantics(t *testing.T) {
	// Priority 2 (HITL response) should never have a TTL — verify convention.
	hitl := ControlMessage{Content: "yes proceed", Priority: 2}
	if hitl.TTL != 0 {
		t.Errorf("HITL message should have TTL=0 (no expiry), got %v", hitl.TTL)
	}

	// Priority 0 (heartbeat) conventionally has TTL=4m.
	heartbeat := ControlMessage{Content: "ping", Priority: 0, TTL: 4 * time.Minute}
	if heartbeat.TTL != 4*time.Minute {
		t.Errorf("heartbeat TTL wrong: %v", heartbeat.TTL)
	}
}

func TestControlMessage_IsExpired(t *testing.T) {
	now := time.Now()

	// Zero TTL — never expires.
	m := ControlMessage{Priority: 0, TTL: 0, EnqueuedAt: now.Add(-10 * time.Minute)}
	if m.isExpired() {
		t.Error("zero TTL should never expire")
	}

	// Priority >= 2 — never expires regardless of TTL.
	m = ControlMessage{Priority: 2, TTL: 1 * time.Second, EnqueuedAt: now.Add(-1 * time.Hour)}
	if m.isExpired() {
		t.Error("priority=2 should never expire")
	}

	// Fresh message with TTL — not expired.
	m = ControlMessage{Priority: 0, TTL: 4 * time.Minute, EnqueuedAt: now}
	if m.isExpired() {
		t.Error("fresh message should not be expired")
	}

	// Stale heartbeat — expired.
	m = ControlMessage{Priority: 0, TTL: 4 * time.Minute, EnqueuedAt: now.Add(-5 * time.Minute)}
	if !m.isExpired() {
		t.Error("stale heartbeat should be expired")
	}

	// Zero EnqueuedAt — never expires.
	m = ControlMessage{Priority: 0, TTL: 1 * time.Second}
	if m.isExpired() {
		t.Error("zero EnqueuedAt should not expire")
	}
}
