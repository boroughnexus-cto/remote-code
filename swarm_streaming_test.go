package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─── buildAnthropicRequest ───────────────────────────────────────────────────

func TestBuildAnthropicRequest_TextOnly(t *testing.T) {
	req := oaiChatRequest{
		Model:    "claude-haiku-4-5",
		Messages: []oaiMessage{{Role: "user", Content: marshalString("Hello")}},
	}
	ar := buildAnthropicRequest(req, "claude-haiku-4-5")
	if ar.Model != "claude-haiku-4-5" {
		t.Errorf("model = %q", ar.Model)
	}
	if len(ar.Messages) != 1 {
		t.Fatalf("messages = %d", len(ar.Messages))
	}
	if ar.Messages[0].Content != "Hello" {
		t.Errorf("content = %v", ar.Messages[0].Content)
	}
	if ar.MaxTokens != 8096 {
		t.Errorf("max_tokens = %d, want 8096 (default)", ar.MaxTokens)
	}
	if ar.System != "" {
		t.Errorf("system = %q, want empty", ar.System)
	}
	if !ar.Stream {
		t.Error("stream should be true")
	}
}

func TestBuildAnthropicRequest_SystemMessage(t *testing.T) {
	req := oaiChatRequest{
		Messages: []oaiMessage{
			{Role: "system", Content: marshalString("You are a helpful assistant.")},
			{Role: "user", Content: marshalString("Hi")},
		},
	}
	ar := buildAnthropicRequest(req, "claude-haiku-4-5")
	if ar.System != "You are a helpful assistant." {
		t.Errorf("system = %q", ar.System)
	}
	if len(ar.Messages) != 1 {
		t.Fatalf("messages = %d (system should be excluded)", len(ar.Messages))
	}
	if ar.Messages[0].Role != "user" {
		t.Errorf("role = %q", ar.Messages[0].Role)
	}
}

func TestBuildAnthropicRequest_MultipleSystemMessages(t *testing.T) {
	req := oaiChatRequest{
		Messages: []oaiMessage{
			{Role: "system", Content: marshalString("Part 1.")},
			{Role: "system", Content: marshalString("Part 2.")},
			{Role: "user", Content: marshalString("Go")},
		},
	}
	ar := buildAnthropicRequest(req, "claude-haiku-4-5")
	if ar.System != "Part 1.\n\nPart 2." {
		t.Errorf("system = %q", ar.System)
	}
}

func TestBuildAnthropicRequest_MultiTurn(t *testing.T) {
	req := oaiChatRequest{
		Messages: []oaiMessage{
			{Role: "user", Content: marshalString("Q1")},
			{Role: "assistant", Content: marshalString("A1")},
			{Role: "user", Content: marshalString("Q2")},
		},
	}
	ar := buildAnthropicRequest(req, "claude-sonnet-4-6")
	if len(ar.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(ar.Messages))
	}
	roles := []string{ar.Messages[0].Role, ar.Messages[1].Role, ar.Messages[2].Role}
	want := []string{"user", "assistant", "user"}
	for i := range roles {
		if roles[i] != want[i] {
			t.Errorf("messages[%d].role = %q, want %q", i, roles[i], want[i])
		}
	}
}

func TestBuildAnthropicRequest_MaxTokensRespected(t *testing.T) {
	req := oaiChatRequest{
		MaxTokens: 512,
		Messages:  []oaiMessage{{Role: "user", Content: marshalString("hi")}},
	}
	ar := buildAnthropicRequest(req, "claude-haiku-4-5")
	if ar.MaxTokens != 512 {
		t.Errorf("max_tokens = %d, want 512", ar.MaxTokens)
	}
}

// ─── SSE frame parsing ───────────────────────────────────────────────────────

func TestParseSSEFrame_EventAndData(t *testing.T) {
	frame := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}"
	ev := parseSSEFrame(frame)
	if ev.eventType != "content_block_delta" {
		t.Errorf("eventType = %q", ev.eventType)
	}
	if !strings.Contains(ev.data, "Hello") {
		t.Errorf("data = %q", ev.data)
	}
}

func TestParseSSEFrame_MessageStop(t *testing.T) {
	frame := "event: message_stop\ndata: {\"type\":\"message_stop\"}"
	ev := parseSSEFrame(frame)
	if ev.eventType != "message_stop" {
		t.Errorf("eventType = %q", ev.eventType)
	}
}

func TestParseSSEFrame_DataOnly(t *testing.T) {
	frame := "data: {\"type\":\"ping\"}"
	ev := parseSSEFrame(frame)
	if ev.eventType != "" {
		t.Errorf("eventType = %q, want empty", ev.eventType)
	}
	if !strings.Contains(ev.data, "ping") {
		t.Errorf("data = %q", ev.data)
	}
}

func TestParseSSEFrame_CRLFLineEndings(t *testing.T) {
	frame := "event: message_stop\r\ndata: {\"type\":\"message_stop\"}"
	ev := parseSSEFrame(frame)
	if ev.eventType != "message_stop" {
		t.Errorf("eventType = %q (CRLF not stripped?)", ev.eventType)
	}
}

func TestSplitSSEFrames_MultipleFrames(t *testing.T) {
	raw := "event: a\ndata: 1\n\nevent: b\ndata: 2\n\n"
	sc := sseFrameScanner(strings.NewReader(raw))
	var frames []string
	for sc.Scan() {
		frames = append(frames, sc.Text())
	}
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want 2: %v", len(frames), frames)
	}
}

func TestSplitSSEFrames_CRLFBoundaries(t *testing.T) {
	raw := "event: a\r\ndata: 1\r\n\r\nevent: b\r\ndata: 2\r\n\r\n"
	sc := sseFrameScanner(strings.NewReader(raw))
	var frames []string
	for sc.Scan() {
		frames = append(frames, sc.Text())
	}
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(frames))
	}
}

// ─── handleDirectStreamingResponse — mock server E2E ────────────────────────

func anthropicMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("anthropic-version") == "" {
			http.Error(w, "missing version", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		events := []string{
			`event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}`,
			`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
			`event: content_block_stop
data: {"type":"content_block_stop","index":0}`,
			`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`event: message_stop
data: {"type":"message_stop"}`,
		}
		for _, ev := range events {
			fmt.Fprintf(w, "%s\n\n", ev)
			flusher.Flush()
		}
	}))
}

func TestHandleDirectStreamingResponse_MockServer(t *testing.T) {
	srv := anthropicMockServer(t)
	defer srv.Close()

	oldURL := anthropicAPIURL
	anthropicAPIURL = srv.URL + "/v1/messages"
	defer func() { anthropicAPIURL = oldURL }()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	oldPool := globalPool
	globalPool = &PoolManager{config: PoolConfig{}, db: database}
	defer func() { globalPool = oldPool }()

	req := oaiChatRequest{
		Model:    "claude-haiku-4-5",
		Messages: []oaiMessage{{Role: "user", Content: marshalString("Hi")}},
		Stream:   true,
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	handleDirectStreamingResponse(w, r, req, "req-e2e", "claude-haiku-4-5", "Hi", time.Now())

	body := w.Body.String()
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("missing [DONE] in: %s", body)
	}

	chunks := parseSSEChunks(body)
	if len(chunks) < 3 {
		t.Fatalf("expected >=3 chunks (role + 2 content + finish), got %d\n%s", len(chunks), body)
	}

	// First chunk: role
	if chunks[0].Choices[0].Delta.Role != "assistant" {
		t.Errorf("first chunk role = %q, want assistant", chunks[0].Choices[0].Delta.Role)
	}

	// Assemble content
	var content strings.Builder
	for _, c := range chunks {
		content.WriteString(c.Choices[0].Delta.Content)
	}
	if content.String() != "Hello world" {
		t.Errorf("assembled = %q, want 'Hello world'", content.String())
	}

	// Last chunk: finish_reason
	last := chunks[len(chunks)-1]
	if last.Choices[0].FinishReason == nil || *last.Choices[0].FinishReason != "stop" {
		t.Errorf("last chunk finish_reason = %v", last.Choices[0].FinishReason)
	}
}

func TestHandleDirectStreamingResponse_MissingAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")

	oldPool := globalPool
	globalPool = &PoolManager{config: PoolConfig{}, db: database}
	defer func() { globalPool = oldPool }()

	req := oaiChatRequest{
		Messages: []oaiMessage{{Role: "user", Content: marshalString("Hi")}},
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	handleDirectStreamingResponse(w, r, req, "req-nokey", "claude-haiku-4-5", "Hi", time.Now())

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestHandleDirectStreamingResponse_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"type":"rate_limit_error","message":"too many requests"}}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	oldURL := anthropicAPIURL
	anthropicAPIURL = srv.URL + "/v1/messages"
	defer func() { anthropicAPIURL = oldURL }()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	oldPool := globalPool
	globalPool = &PoolManager{config: PoolConfig{}, db: database}
	defer func() { globalPool = oldPool }()

	req := oaiChatRequest{
		Messages: []oaiMessage{{Role: "user", Content: marshalString("Hi")}},
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	handleDirectStreamingResponse(w, r, req, "req-rl", "claude-haiku-4-5", "Hi", time.Now())

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
}
