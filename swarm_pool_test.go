package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─── Unit tests (no external processes) ──────────────────────────────────────

func TestModelShortName(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"claude-haiku-4-5", "haiku"},
		{"claude-sonnet-4-6", "sonnet"},
		{"claude-opus-4-6", "opus"},
		{"unknown-model", "unknown-model"},
	}
	for _, tt := range tests {
		if got := modelShortName(tt.model); got != tt.want {
			t.Errorf("modelShortName(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

func TestResolveModel(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{"haiku", "claude-haiku-4-5", true},
		{"sonnet", "claude-sonnet-4-6", true},
		{"opus", "claude-opus-4-6", true},
		{"gpt-4o", "claude-sonnet-4-6", true},
		{"gpt-4o-mini", "claude-haiku-4-5", true},
		{"gpt-4", "claude-opus-4-6", true},
		{"claude-haiku-4-5", "claude-haiku-4-5", true},
		{"claude-sonnet-4-6", "claude-sonnet-4-6", true},
		{"HAIKU", "claude-haiku-4-5", true},   // case insensitive
		{" sonnet ", "claude-sonnet-4-6", true}, // trimmed
		{"nonexistent", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := resolveModel(tt.input)
		if ok != tt.ok || got != tt.want {
			t.Errorf("resolveModel(%q) = (%q, %v), want (%q, %v)", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

func TestMessagesToPrompt(t *testing.T) {
	messages := []oaiMessage{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
		{Role: "user", Content: "What is 2+2?"},
	}
	got := messagesToPrompt(messages)
	if !strings.Contains(got, "[System]\nYou are helpful.") {
		t.Errorf("missing system prefix in: %s", got)
	}
	if !strings.Contains(got, "Hello") {
		t.Errorf("missing user content in: %s", got)
	}
	if !strings.Contains(got, "[Previous assistant response]\nHi there!") {
		t.Errorf("missing assistant prefix in: %s", got)
	}
	if !strings.Contains(got, "What is 2+2?") {
		t.Errorf("missing second user content in: %s", got)
	}
}

func TestMessagesToPrompt_SingleUser(t *testing.T) {
	messages := []oaiMessage{
		{Role: "user", Content: "just this"},
	}
	got := messagesToPrompt(messages)
	if got != "just this" {
		t.Errorf("single user message: got %q, want %q", got, "just this")
	}
}

func TestMessagesToPrompt_Empty(t *testing.T) {
	got := messagesToPrompt(nil)
	if got != "" {
		t.Errorf("empty messages: got %q, want empty", got)
	}
}

func TestMapStopReason(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"end_turn", "stop"},
		{"stop_sequence", "stop"},
		{"max_tokens", "length"},
		{"", "stop"},
		{"unknown", "stop"},
	}
	for _, tt := range tests {
		if got := mapStopReason(tt.input); got != tt.want {
			t.Errorf("mapStopReason(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestClassifyResultError(t *testing.T) {
	tests := []struct {
		name   string
		event  poolEvent
		want   string
	}{
		{"not error", poolEvent{IsError: false}, ""},
		{"auth failed", poolEvent{IsError: true, Result: "Not logged in · Please run /login"}, "disable"},
		{"billing", poolEvent{IsError: true, Result: "billing quota exceeded"}, "disable"},
		{"rate limit", poolEvent{IsError: true, Result: "rate limit reached"}, "retry"},
		{"prompt too long", poolEvent{IsError: true, Result: "Prompt is too long"}, "ignore"},
		{"unknown error", poolEvent{IsError: true, Result: "something went wrong"}, "recycle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyResultError(tt.event); got != tt.want {
				t.Errorf("classifyResultError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractAssistantText(t *testing.T) {
	msg := json.RawMessage(`{"content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"x"},{"type":"text","text":" world"}]}`)
	ev := poolEvent{Type: "assistant", Message: msg}
	got := extractAssistantText(ev)
	if got != "hello world" {
		t.Errorf("extractAssistantText = %q, want %q", got, "hello world")
	}
}

func TestExtractAssistantText_NoMessage(t *testing.T) {
	ev := poolEvent{Type: "assistant"}
	got := extractAssistantText(ev)
	if got != "" {
		t.Errorf("extractAssistantText with no message = %q, want empty", got)
	}
}

func TestExtractStreamText(t *testing.T) {
	event := json.RawMessage(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`)
	ev := poolEvent{Type: "stream_event", Event: event}
	got := extractStreamText(ev)
	if got != "hi" {
		t.Errorf("extractStreamText = %q, want %q", got, "hi")
	}
}

func TestExtractStreamText_NonTextDelta(t *testing.T) {
	event := json.RawMessage(`{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{"}}`)
	ev := poolEvent{Type: "stream_event", Event: event}
	got := extractStreamText(ev)
	if got != "" {
		t.Errorf("extractStreamText for json delta = %q, want empty", got)
	}
}

func TestExtractStreamText_NoEvent(t *testing.T) {
	ev := poolEvent{Type: "stream_event"}
	got := extractStreamText(ev)
	if got != "" {
		t.Errorf("extractStreamText with no event = %q, want empty", got)
	}
}

func TestTruncateStr(t *testing.T) {
	if got := truncateStr("hello", 3); got != "hel" {
		t.Errorf("truncateStr(hello,3) = %q", got)
	}
	if got := truncateStr("hi", 10); got != "hi" {
		t.Errorf("truncateStr(hi,10) = %q", got)
	}
	if got := truncateStr("", 5); got != "" {
		t.Errorf("truncateStr(empty,5) = %q", got)
	}
}

func TestNilIfEmpty(t *testing.T) {
	if nilIfEmpty("") != nil {
		t.Error("nilIfEmpty empty should be nil")
	}
	if p := nilIfEmpty("hello"); p == nil || *p != "hello" {
		t.Error("nilIfEmpty non-empty should return pointer")
	}
}

func TestParseInt(t *testing.T) {
	tests := []struct {
		input string
		want  int
		err   bool
	}{
		{"42", 42, false},
		{"0", 0, false},
		{"100", 100, false},
		{"abc", 0, true},
		{"12.5", 0, true},
		{"-1", 0, true},
	}
	for _, tt := range tests {
		got, err := parseInt(tt.input)
		if (err != nil) != tt.err {
			t.Errorf("parseInt(%q) error=%v, wantErr=%v", tt.input, err, tt.err)
		}
		if got != tt.want {
			t.Errorf("parseInt(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestDefaultPoolConfig(t *testing.T) {
	c := DefaultPoolConfig()
	if len(c.Models) != 3 {
		t.Errorf("expected 3 default models, got %d", len(c.Models))
	}
	if c.SlotsPerModel != 2 {
		t.Errorf("expected 2 slots per model, got %d", c.SlotsPerModel)
	}
	if c.RequestTimeout != 5*time.Minute {
		t.Errorf("expected 5m timeout, got %v", c.RequestTimeout)
	}
}

func TestSlotStateString(t *testing.T) {
	tests := []struct {
		state slotState
		want  string
	}{
		{slotIdle, "idle"},
		{slotBusy, "busy"},
		{slotStarting, "starting"},
		{slotDead, "dead"},
		{slotState(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("slotState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

// ─── PoolSlot readEvent tests (using pipe-based fake) ────────────────────────

func TestSlotReadEvent_ValidJSON(t *testing.T) {
	r, w := io.Pipe()
	slot := &PoolSlot{
		ID:     "test-slot",
		stdout: bufio.NewReaderSize(r, 4096),
	}

	go func() {
		fmt.Fprintln(w, `{"type":"system","subtype":"init","session_id":"abc"}`)
		fmt.Fprintln(w, `{"type":"result","subtype":"success","result":"4","total_cost_usd":0.01}`)
		w.Close()
	}()

	ev1, err := slot.readEvent()
	if err != nil {
		t.Fatalf("readEvent 1: %v", err)
	}
	if ev1.Type != "system" || ev1.Subtype != "init" {
		t.Errorf("ev1: got %s/%s, want system/init", ev1.Type, ev1.Subtype)
	}

	ev2, err := slot.readEvent()
	if err != nil {
		t.Fatalf("readEvent 2: %v", err)
	}
	if ev2.Type != "result" || ev2.Result != "4" {
		t.Errorf("ev2: got type=%s result=%q", ev2.Type, ev2.Result)
	}
	if ev2.CostUSD != 0.01 {
		t.Errorf("ev2: cost=%f, want 0.01", ev2.CostUSD)
	}
}

func TestSlotReadEvent_SkipsBlankAndNonJSON(t *testing.T) {
	r, w := io.Pipe()
	slot := &PoolSlot{
		ID:     "test-slot",
		stdout: bufio.NewReaderSize(r, 4096),
	}

	go func() {
		fmt.Fprintln(w, "")                    // blank line
		fmt.Fprintln(w, "not json")            // non-JSON
		fmt.Fprintln(w, "  ")                  // whitespace
		fmt.Fprintln(w, `{"type":"result","result":"ok"}`) // valid
		w.Close()
	}()

	ev, err := slot.readEvent()
	if err != nil {
		t.Fatalf("readEvent: %v", err)
	}
	if ev.Type != "result" {
		t.Errorf("expected result, got %s", ev.Type)
	}
}

func TestSlotReadEvent_EOF(t *testing.T) {
	r, w := io.Pipe()
	slot := &PoolSlot{
		ID:     "test-slot",
		stdout: bufio.NewReaderSize(r, 4096),
	}
	w.Close() // immediate EOF

	_, err := slot.readEvent()
	if err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}

func TestSlotSendQuery(t *testing.T) {
	r, w := io.Pipe()
	slot := &PoolSlot{
		ID:    "test-slot",
		stdin: w,
	}

	var received string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		if scanner.Scan() {
			received = scanner.Text()
		}
	}()

	err := slot.sendQuery("hello world")
	if err != nil {
		t.Fatalf("sendQuery: %v", err)
	}
	w.Close()
	wg.Wait()

	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(received), &msg); err != nil {
		t.Fatalf("parse sent message: %v", err)
	}
	if msg["type"] != "user" {
		t.Errorf("type = %v, want user", msg["type"])
	}
	m := msg["message"].(map[string]interface{})
	if m["role"] != "user" {
		t.Errorf("role = %v, want user", m["role"])
	}
	if m["content"] != "hello world" {
		t.Errorf("content = %v, want hello world", m["content"])
	}
}

// ─── Pool acquire/release with fake slots ────────────────────────────────────

func newFakeSlot(id, model string) *PoolSlot {
	return &PoolSlot{
		ID:       id,
		Model:    model,
		state:    slotIdle,
		lastUsed: time.Now(),
	}
}

func TestPoolAcquireRelease(t *testing.T) {
	ctx := context.Background()
	pm := &PoolManager{
		slots:     map[string][]*PoolSlot{},
		available: map[string]chan *PoolSlot{},
		config:    DefaultPoolConfig(),
	}

	model := "claude-haiku-4-5"
	pm.available[model] = make(chan *PoolSlot, 2)
	s1 := newFakeSlot("pool-haiku-0", model)
	s2 := newFakeSlot("pool-haiku-1", model)
	pm.available[model] <- s1
	pm.available[model] <- s2
	pm.slots[model] = []*PoolSlot{s1, s2}

	// Acquire first
	got, err := pm.Acquire(ctx, model)
	if err != nil {
		t.Fatalf("Acquire 1: %v", err)
	}
	if got.ID != "pool-haiku-0" {
		t.Errorf("got slot %s, want pool-haiku-0", got.ID)
	}
	if got.state != slotBusy {
		t.Errorf("state = %v, want busy", got.state)
	}

	// Acquire second
	got2, err := pm.Acquire(ctx, model)
	if err != nil {
		t.Fatalf("Acquire 2: %v", err)
	}
	if got2.ID != "pool-haiku-1" {
		t.Errorf("got slot %s, want pool-haiku-1", got2.ID)
	}

	// Third acquire should block — use timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err = pm.Acquire(timeoutCtx, model)
	if err == nil {
		t.Error("expected error when pool exhausted, got nil")
	}

	// Release first slot
	pm.Release(got)
	if got.state != slotIdle {
		t.Errorf("after release: state = %v, want idle", got.state)
	}

	// Now acquire should work
	got3, err := pm.Acquire(ctx, model)
	if err != nil {
		t.Fatalf("Acquire 3: %v", err)
	}
	if got3.ID != "pool-haiku-0" {
		t.Errorf("got slot %s, want pool-haiku-0", got3.ID)
	}
	pm.Release(got3)
	pm.Release(got2)
}

func TestPoolAcquire_UnknownModel(t *testing.T) {
	pm := &PoolManager{
		available: map[string]chan *PoolSlot{},
		config:    DefaultPoolConfig(),
	}
	_, err := pm.Acquire(context.Background(), "unknown-model")
	if err == nil {
		t.Error("expected error for unknown model")
	}
	if !strings.Contains(err.Error(), "unknown model") {
		t.Errorf("error = %v, want 'unknown model'", err)
	}
}

func TestPoolRelease_DeadSlot(t *testing.T) {
	// When a dead slot is released, it should NOT go back to the available channel.
	// We verify this by checking the channel stays empty.
	// We use a cancelled context so spawnSlot fails immediately in the recycle goroutine.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately — recycle will fail to spawn, which is fine for this test
	pm := &PoolManager{
		slots:     map[string][]*PoolSlot{},
		available: map[string]chan *PoolSlot{},
		config:    DefaultPoolConfig(),
		ctx:       ctx,
	}
	model := "claude-haiku-4-5"
	pm.available[model] = make(chan *PoolSlot, 2)
	pm.slots[model] = []*PoolSlot{}

	slot := newFakeSlot("pool-haiku-0", model)
	slot.state = slotDead

	pm.Release(slot)

	// Give the recycle goroutine a moment to run (and fail)
	time.Sleep(100 * time.Millisecond)

	// Available channel should be empty (dead slot not returned, recycle failed)
	select {
	case <-pm.available[model]:
		t.Error("dead slot should not be returned to available channel")
	default:
		// Good — channel empty
	}
}

func TestPoolStatus(t *testing.T) {
	pm := &PoolManager{
		slots:     map[string][]*PoolSlot{},
		available: map[string]chan *PoolSlot{},
		config:    DefaultPoolConfig(),
	}
	model := "claude-haiku-4-5"
	pm.available[model] = make(chan *PoolSlot, 2)
	s := newFakeSlot("pool-haiku-0", model)
	s.totalCost = 0.5
	s.totalRequests = 10
	pm.slots[model] = []*PoolSlot{s}
	pm.available[model] <- s

	status := pm.Status()
	if status["enabled"] != true {
		t.Error("status should show enabled=true")
	}
	models := status["models"].(map[string]interface{})
	haikuInfo := models[model].(map[string]interface{})
	if haikuInfo["available"] != 1 {
		t.Errorf("available = %v, want 1", haikuInfo["available"])
	}
	if haikuInfo["total_requests"] != int64(10) {
		t.Errorf("total_requests = %v, want 10", haikuInfo["total_requests"])
	}
}

func TestPoolHandleRateLimit(t *testing.T) {
	pm := &PoolManager{
		config: PoolConfig{
			BackoffBase: 100 * time.Millisecond,
			BackoffMax:  1 * time.Second,
		},
	}
	slot := newFakeSlot("test", "claude-haiku-4-5")

	pm.handleRateLimit(slot, poolEvent{Type: "rate_limit_event"})

	slot.mu.Lock()
	if slot.errorCount != 1 {
		t.Errorf("errorCount = %d, want 1", slot.errorCount)
	}
	if slot.rateLimitUntil.IsZero() {
		t.Error("rateLimitUntil should be set")
	}
	if time.Until(slot.rateLimitUntil) > 500*time.Millisecond {
		t.Errorf("rate limit duration too long: %v", time.Until(slot.rateLimitUntil))
	}
	slot.mu.Unlock()

	// Second rate limit should increase backoff
	pm.handleRateLimit(slot, poolEvent{Type: "rate_limit_event"})
	slot.mu.Lock()
	if slot.errorCount != 2 {
		t.Errorf("errorCount = %d, want 2", slot.errorCount)
	}
	slot.mu.Unlock()
}

func TestPoolDrainSlotFromAvailable(t *testing.T) {
	pm := &PoolManager{
		available: map[string]chan *PoolSlot{},
	}
	model := "claude-haiku-4-5"
	pm.available[model] = make(chan *PoolSlot, 3)

	s1 := newFakeSlot("s1", model)
	s2 := newFakeSlot("s2", model)
	s3 := newFakeSlot("s3", model)
	pm.available[model] <- s1
	pm.available[model] <- s2
	pm.available[model] <- s3

	pm.drainSlotFromAvailable(model, s2)

	// Should have s1 and s3 remaining
	remaining := make(map[string]bool)
	for i := 0; i < 2; i++ {
		select {
		case s := <-pm.available[model]:
			remaining[s.ID] = true
		default:
			t.Fatalf("expected 2 remaining slots, got %d", i)
		}
	}
	if remaining["s2"] {
		t.Error("s2 should have been drained")
	}
	if !remaining["s1"] || !remaining["s3"] {
		t.Errorf("expected s1 and s3, got %v", remaining)
	}
}

// ─── Integration test: real claude process (skipped if claude not available) ─

func TestPoolSlot_RealClaude(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not available")
	}
	// Only run if explicitly requested (uses real API credits)
	if os.Getenv("POOL_INTEGRATION_TEST") == "" {
		t.Skip("set POOL_INTEGRATION_TEST=1 to run real Claude tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	config := PoolConfig{
		Models:         []string{"claude-haiku-4-5"},
		SlotsPerModel:  1,
		RequestTimeout: 30 * time.Second,
		MaxConsecErrs:  3,
		IdleRecycleAge: 5 * time.Minute,
		BackoffBase:    1 * time.Second,
		BackoffMax:     10 * time.Second,
	}

	pm := NewPoolManager(ctx, nil, config)
	defer pm.Shutdown()

	// Acquire a slot
	slot, err := pm.Acquire(ctx, "claude-haiku-4-5")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Send a real query
	if err := slot.sendQuery("What is 2+2? Reply with ONLY the number."); err != nil {
		t.Fatalf("sendQuery: %v", err)
	}

	var result poolEvent
	for {
		ev, err := slot.readEvent()
		if err != nil {
			t.Fatalf("readEvent: %v", err)
		}
		if ev.Type == "result" {
			result = ev
			break
		}
	}

	if result.IsError {
		t.Fatalf("result error: %s", result.Result)
	}
	if !strings.Contains(result.Result, "4") {
		t.Errorf("result = %q, expected to contain 4", result.Result)
	}
	if result.CostUSD <= 0 {
		t.Errorf("cost = %f, expected > 0", result.CostUSD)
	}

	pm.Release(slot)
	t.Logf("Result: %q, cost: $%.6f", result.Result, result.CostUSD)
}

// ─── Pool request logging ────────────────────────────────────────────────────

func TestPoolLogRequest(t *testing.T) {
	pm := &PoolManager{db: database}

	reqID := "test-req-" + fmt.Sprintf("%d", time.Now().UnixNano())
	pm.logRequest(reqID, "claude-haiku-4-5", "pool-haiku-0", "test prompt", "complete",
		10, 20, 1500, 900, 0.01, "", "")

	var count int
	err := database.QueryRow("SELECT COUNT(*) FROM pool_requests WHERE request_id = ?", reqID).Scan(&count)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row, got %d", count)
	}

	// Verify fields
	var model, slotID, status string
	var costUSD float64
	err = database.QueryRow("SELECT model, slot_id, status, cost_usd FROM pool_requests WHERE request_id = ?", reqID).
		Scan(&model, &slotID, &status, &costUSD)
	if err != nil {
		t.Fatalf("query fields: %v", err)
	}
	if model != "claude-haiku-4-5" {
		t.Errorf("model = %q", model)
	}
	if slotID != "pool-haiku-0" {
		t.Errorf("slot_id = %q", slotID)
	}
	if status != "complete" {
		t.Errorf("status = %q", status)
	}
	if costUSD != 0.01 {
		t.Errorf("cost_usd = %f", costUSD)
	}

	// Cleanup
	database.Exec("DELETE FROM pool_requests WHERE request_id = ?", reqID)
}

func TestPoolLogRequest_NilDB(t *testing.T) {
	pm := &PoolManager{db: nil}
	// Should not panic
	pm.logRequest("x", "m", "s", "p", "complete", 0, 0, 0, 0, 0, "", "")
}
