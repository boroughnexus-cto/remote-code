package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ─── Handler tests ───────────────────────────────────────────────────────────

func TestHandleListModels_NoPool(t *testing.T) {
	oldPool := globalPool
	globalPool = nil
	defer func() { globalPool = oldPool }()

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	handlePoolListModels(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp oaiModelList
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Object != "list" {
		t.Errorf("object = %q, want list", resp.Object)
	}
	if len(resp.Data) != 0 {
		t.Errorf("expected 0 models when pool disabled, got %d", len(resp.Data))
	}
}

func TestHandleListModels_WithPool(t *testing.T) {
	oldPool := globalPool
	globalPool = &PoolManager{
		config: PoolConfig{
			Models: []string{"claude-haiku-4-5", "claude-sonnet-4-6"},
		},
	}
	defer func() { globalPool = oldPool }()

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	handlePoolListModels(w, req)

	var resp oaiModelList
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 2 {
		t.Errorf("expected 2 models, got %d", len(resp.Data))
	}
	if resp.Data[0].OwnedBy != "anthropic" {
		t.Errorf("owned_by = %q", resp.Data[0].OwnedBy)
	}
}

func TestHandleListModels_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/models", nil)
	w := httptest.NewRecorder()
	handlePoolListModels(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleChatCompletions_PoolDisabled(t *testing.T) {
	oldPool := globalPool
	globalPool = nil
	defer func() { globalPool = oldPool }()

	body := `{"model":"haiku","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	handlePoolChatCompletions(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestHandleChatCompletions_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	handlePoolChatCompletions(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleChatCompletions_InvalidJSON(t *testing.T) {
	oldPool := globalPool
	globalPool = &PoolManager{
		config:    PoolConfig{},
		available: map[string]chan *PoolSlot{},
	}
	defer func() { globalPool = oldPool }()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	handlePoolChatCompletions(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleChatCompletions_EmptyMessages(t *testing.T) {
	oldPool := globalPool
	globalPool = &PoolManager{
		config:    PoolConfig{},
		available: map[string]chan *PoolSlot{},
	}
	defer func() { globalPool = oldPool }()

	body := `{"model":"haiku","messages":[]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	handlePoolChatCompletions(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleChatCompletions_UnknownModel(t *testing.T) {
	oldPool := globalPool
	globalPool = &PoolManager{
		config:    PoolConfig{},
		available: map[string]chan *PoolSlot{},
	}
	defer func() { globalPool = oldPool }()

	body := `{"model":"nonexistent","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	handlePoolChatCompletions(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleChatCompletions_AuthRequired(t *testing.T) {
	oldPool := globalPool
	globalPool = &PoolManager{
		config:    PoolConfig{APIKey: "secret123"},
		available: map[string]chan *PoolSlot{},
	}
	defer func() { globalPool = oldPool }()

	body := `{"model":"haiku","messages":[{"role":"user","content":"hi"}]}`

	// No auth header
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	handlePoolChatCompletions(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no auth: status = %d, want 401", w.Code)
	}

	// Wrong auth
	req = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer wrongkey")
	w = httptest.NewRecorder()
	handlePoolChatCompletions(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong auth: status = %d, want 401", w.Code)
	}
}

func TestHandleChatCompletions_AuthCorrect(t *testing.T) {
	// Use a fake pool with no real slots — will get 429 (pool exhausted) rather than 401
	oldPool := globalPool
	globalPool = &PoolManager{
		config:    PoolConfig{APIKey: "secret123"},
		available: map[string]chan *PoolSlot{"claude-haiku-4-5": make(chan *PoolSlot, 1)},
		db:        database,
	}
	defer func() { globalPool = oldPool }()

	body := `{"model":"haiku","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret123")
	// Use a short context so it doesn't block forever waiting for a slot
	ctx, cancel := context.WithTimeout(req.Context(), 100*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	handlePoolChatCompletions(w, req)

	// Should get 429 (pool exhausted), NOT 401 (auth passed)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429 (auth passed, pool empty)", w.Code)
	}
}

func TestHandleChatCompletions_NoAuthRequired(t *testing.T) {
	// Empty API key = no auth required
	oldPool := globalPool
	globalPool = &PoolManager{
		config:    PoolConfig{APIKey: ""},
		available: map[string]chan *PoolSlot{"claude-haiku-4-5": make(chan *PoolSlot, 1)},
		db:        database,
	}
	defer func() { globalPool = oldPool }()

	body := `{"model":"haiku","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	ctx, cancel := context.WithTimeout(req.Context(), 100*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	handlePoolChatCompletions(w, req)

	// Should get 429 (pool empty), not 401
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
}

// ─── Non-streaming response with fake slot ───────────────────────────────────

func newFakePoolSlot(responses []string) (*PoolSlot, io.WriteCloser) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	slot := &PoolSlot{
		ID:     "fake-slot",
		Model:  "claude-haiku-4-5",
		stdin:  stdinW,
		stdout: bufio.NewReaderSize(stdoutR, 4096),
		state:  slotBusy,
	}

	// Feed responses on stdout
	go func() {
		for _, line := range responses {
			fmt.Fprintln(stdoutW, line)
		}
		stdoutW.Close()
	}()

	// Drain stdin
	go func() {
		io.Copy(io.Discard, stdinR)
	}()

	return slot, stdinW
}

func TestHandleNonStreamingResponse(t *testing.T) {
	oldPool := globalPool
	globalPool = &PoolManager{
		config: PoolConfig{},
		db:     database,
	}
	defer func() { globalPool = oldPool }()

	responses := []string{
		`{"type":"system","subtype":"init","session_id":"test"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"4"}]}}`,
		`{"type":"result","subtype":"success","result":"4","total_cost_usd":0.001,"is_error":false,"usage":{"input_tokens":10,"output_tokens":5},"stop_reason":"end_turn"}`,
	}
	slot, _ := newFakePoolSlot(responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	handleNonStreamingResponse(w, req, slot, "req-1", "claude-haiku-4-5", "test", time.Now())

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp oaiChatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != "req-1" {
		t.Errorf("id = %q", resp.ID)
	}
	if resp.Object != "chat.completion" {
		t.Errorf("object = %q", resp.Object)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "4" {
		t.Errorf("content = %q, want 4", resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q", resp.Choices[0].FinishReason)
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("prompt_tokens = %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 5 {
		t.Errorf("completion_tokens = %d", resp.Usage.CompletionTokens)
	}
}

func TestHandleNonStreamingResponse_ErrorResult(t *testing.T) {
	oldPool := globalPool
	globalPool = &PoolManager{
		config: PoolConfig{},
		db:     database,
	}
	defer func() { globalPool = oldPool }()

	responses := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"result","subtype":"error","result":"rate limit reached","is_error":true}`,
	}
	slot, _ := newFakePoolSlot(responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	handleNonStreamingResponse(w, req, slot, "req-err", "claude-haiku-4-5", "test", time.Now())

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestHandleNonStreamingResponse_FallbackToResultText(t *testing.T) {
	// When assistant events have no text but result.Result does
	oldPool := globalPool
	globalPool = &PoolManager{
		config: PoolConfig{},
		db:     database,
	}
	defer func() { globalPool = oldPool }()

	responses := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"result","subtype":"success","result":"fallback text","is_error":false,"stop_reason":"end_turn"}`,
	}
	slot, _ := newFakePoolSlot(responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	handleNonStreamingResponse(w, req, slot, "req-fb", "claude-haiku-4-5", "test", time.Now())

	var resp oaiChatResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Choices[0].Message.Content != "fallback text" {
		t.Errorf("content = %q, want 'fallback text'", resp.Choices[0].Message.Content)
	}
}

// ─── Streaming response with fake slot ───────────────────────────────────────

func TestHandleStreamingResponse(t *testing.T) {
	oldPool := globalPool
	globalPool = &PoolManager{
		config: PoolConfig{},
		db:     database,
	}
	defer func() { globalPool = oldPool }()

	responses := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello world"}]}}`,
		`{"type":"result","subtype":"success","result":"Hello world","total_cost_usd":0.002,"is_error":false,"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`,
	}
	slot, _ := newFakePoolSlot(responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	handleStreamingResponse(w, req, slot, "req-stream", "claude-haiku-4-5", "test", time.Now())

	body := w.Body.String()

	// Should contain SSE data lines
	if !strings.Contains(body, "data: ") {
		t.Errorf("missing SSE data prefix in: %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("missing [DONE] terminator in: %s", body)
	}

	// Parse SSE chunks
	chunks := parseSSEChunks(body)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks (role + content), got %d", len(chunks))
	}

	// First chunk should have role
	if chunks[0].Choices[0].Delta.Role != "assistant" {
		t.Errorf("first chunk role = %q, want assistant", chunks[0].Choices[0].Delta.Role)
	}

	// Find content chunk
	found := false
	for _, c := range chunks {
		if c.Choices[0].Delta.Content == "Hello world" {
			found = true
			break
		}
	}
	if !found {
		t.Error("no chunk with content 'Hello world'")
	}

	// Last chunk should have finish_reason
	last := chunks[len(chunks)-1]
	if last.Choices[0].FinishReason == nil || *last.Choices[0].FinishReason != "stop" {
		t.Errorf("last chunk finish_reason = %v", last.Choices[0].FinishReason)
	}
}

func TestHandleStreamingResponse_CostTracking(t *testing.T) {
	oldPool := globalPool
	pm := &PoolManager{
		config: PoolConfig{},
		db:     database,
	}
	pm.totalCost.Store(0)
	globalPool = pm
	defer func() { globalPool = oldPool }()

	responses := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"result","subtype":"success","result":"ok","total_cost_usd":0.005,"is_error":false,"stop_reason":"end_turn"}`,
	}
	slot, _ := newFakePoolSlot(responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	handleStreamingResponse(w, req, slot, "req-cost", "claude-haiku-4-5", "test", time.Now())

	// Check cost was tracked
	costMicro := pm.totalCost.Load()
	if costMicro != 5000 { // 0.005 * 1e6
		t.Errorf("total cost = %d microdollars, want 5000", costMicro)
	}

	// Slot cost should also be updated
	slot.mu.Lock()
	if slot.totalCost != 0.005 {
		t.Errorf("slot cost = %f, want 0.005", slot.totalCost)
	}
	if slot.totalRequests != 1 {
		t.Errorf("slot requests = %d, want 1", slot.totalRequests)
	}
	slot.mu.Unlock()
}

// ─── Pool status API ─────────────────────────────────────────────────────────

func TestHandlePoolStatusAPI_Disabled(t *testing.T) {
	oldPool := globalPool
	globalPool = nil
	defer func() { globalPool = oldPool }()

	req := httptest.NewRequest("GET", "/api/swarm/pool", nil)
	w := httptest.NewRecorder()
	handlePoolStatusAPI(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["enabled"] != false {
		t.Error("expected enabled=false when pool nil")
	}
}

func TestHandlePoolStatusAPI_Enabled(t *testing.T) {
	oldPool := globalPool
	pm := &PoolManager{
		slots:     map[string][]*PoolSlot{},
		available: map[string]chan *PoolSlot{},
		config:    PoolConfig{Models: []string{"claude-haiku-4-5"}},
	}
	pm.available["claude-haiku-4-5"] = make(chan *PoolSlot, 2)
	s := newFakeSlot("pool-haiku-0", "claude-haiku-4-5")
	pm.slots["claude-haiku-4-5"] = []*PoolSlot{s}
	pm.available["claude-haiku-4-5"] <- s
	globalPool = pm
	defer func() { globalPool = oldPool }()

	req := httptest.NewRequest("GET", "/api/swarm/pool", nil)
	w := httptest.NewRecorder()
	handlePoolStatusAPI(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["enabled"] != true {
		t.Error("expected enabled=true")
	}
}

func TestHandlePoolStatusAPI_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/swarm/pool", nil)
	w := httptest.NewRecorder()
	handlePoolStatusAPI(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// ─── Error response format ───────────────────────────────────────────────────

func TestWriteOAIError(t *testing.T) {
	w := httptest.NewRecorder()
	writeOAIError(w, 429, "rate_limited", "Too many requests")

	if w.Code != 429 {
		t.Errorf("status = %d", w.Code)
	}
	var resp oaiError
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error.Code != "rate_limited" {
		t.Errorf("code = %q", resp.Error.Code)
	}
	if resp.Error.Message != "Too many requests" {
		t.Errorf("message = %q", resp.Error.Message)
	}
}

func TestGenerateReqID(t *testing.T) {
	id1 := generateReqID()
	id2 := generateReqID()
	if !strings.HasPrefix(id1, "swarm-") {
		t.Errorf("id %q missing swarm- prefix", id1)
	}
	if id1 == id2 {
		t.Error("two generated IDs should differ")
	}
	if len(id1) != 30 { // "swarm-" + 24 hex chars
		t.Errorf("id length = %d, want 30", len(id1))
	}
}

// ─── Full end-to-end with fake slot (non-streaming) ──────────────────────────

func TestEndToEnd_NonStreaming(t *testing.T) {
	oldPool := globalPool
	pm := &PoolManager{
		config:    PoolConfig{},
		available: map[string]chan *PoolSlot{},
		slots:     map[string][]*PoolSlot{},
		db:        database,
	}
	pm.totalCost = atomic.Int64{}

	model := "claude-haiku-4-5"
	pm.available[model] = make(chan *PoolSlot, 1)

	// Create a fake slot with piped stdin/stdout
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	slot := &PoolSlot{
		ID:     "e2e-slot",
		Model:  model,
		stdin:  stdinW,
		stdout: bufio.NewReaderSize(stdoutR, 4096),
		state:  slotIdle,
	}
	pm.available[model] <- slot
	pm.slots[model] = []*PoolSlot{slot}
	globalPool = pm
	defer func() { globalPool = oldPool }()

	// Background: read stdin (the query), then write response
	go func() {
		scanner := bufio.NewScanner(stdinR)
		if scanner.Scan() {
			// Got query — send response
			fmt.Fprintln(stdoutW, `{"type":"system","subtype":"init","session_id":"e2e"}`)
			fmt.Fprintln(stdoutW, `{"type":"assistant","message":{"content":[{"type":"text","text":"42"}]}}`)
			fmt.Fprintln(stdoutW, `{"type":"result","subtype":"success","result":"42","total_cost_usd":0.003,"is_error":false,"stop_reason":"end_turn","usage":{"input_tokens":8,"output_tokens":2}}`)
		}
	}()

	body := `{"model":"haiku","messages":[{"role":"user","content":"What is the answer?"}],"stream":false}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	handlePoolChatCompletions(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp oaiChatResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Choices[0].Message.Content != "42" {
		t.Errorf("content = %q, want 42", resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].Message.Role != "assistant" {
		t.Errorf("role = %q", resp.Choices[0].Message.Role)
	}
	if resp.Usage.PromptTokens != 8 {
		t.Errorf("prompt_tokens = %d", resp.Usage.PromptTokens)
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func parseSSEChunks(body string) []oaiChunk {
	var chunks []oaiChunk
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var chunk oaiChunk
		if err := json.Unmarshal([]byte(data), &chunk); err == nil {
			chunks = append(chunks, chunk)
		}
	}
	return chunks
}
