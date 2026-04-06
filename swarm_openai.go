package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// ─── OpenAI-compatible types ─────────────────────────────────────────────────

type oaiChatRequest struct {
	Model       string         `json:"model"`
	Messages    []oaiMessage   `json:"messages"`
	Stream      bool           `json:"stream"`
	MaxTokens   int            `json:"max_tokens,omitempty"`
	Temperature *float64       `json:"temperature,omitempty"`
}

type oaiMessage struct {
	Role    string `json:"role"`
	Content json.RawMessage `json:"content"`
}

type oaiChatResponse struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []oaiChoice      `json:"choices"`
	Usage   oaiUsage         `json:"usage"`
}

type oaiChoice struct {
	Index        int        `json:"index"`
	Message      oaiMessage `json:"message"`
	FinishReason string     `json:"finish_reason"`
}

type oaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type oaiChunk struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []oaiChunkChoice  `json:"choices"`
}

type oaiChunkChoice struct {
	Index        int            `json:"index"`
	Delta        oaiDelta       `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

type oaiDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type oaiModelList struct {
	Object string     `json:"object"`
	Data   []oaiModel `json:"data"`
}

type oaiModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type oaiError struct {
	Error oaiErrorDetail `json:"error"`
}

type oaiErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// ─── Model resolution ────────────────────────────────────────────────────────

var modelAliases = map[string]string{
	// Short names
	"haiku":  "claude-haiku-4-5",
	"sonnet": "claude-sonnet-4-6",
	"opus":   "claude-opus-4-6",

	// OpenAI compatibility
	"gpt-4o-mini": "claude-haiku-4-5",
	"gpt-4o":      "claude-sonnet-4-6",
	"gpt-4":       "claude-opus-4-6",

	// Full names pass through
	"claude-haiku-4-5":  "claude-haiku-4-5",
	"claude-sonnet-4-6": "claude-sonnet-4-6",
	"claude-opus-4-6":   "claude-opus-4-6",
}

func resolveModel(name string) (string, bool) {
	m, ok := modelAliases[strings.ToLower(strings.TrimSpace(name))]
	return m, ok
}

// ─── Message conversion ─────────────────────────────────────────────────────

// extractTextContent extracts text from a Content field that may be
// a JSON string or an OpenAI-style array of content blocks.
func extractTextContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try as plain string first (most common)
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try as array of content blocks (vision/multi-part)
	var blocks []map[string]interface{}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if t, ok := b["type"].(string); ok && t == "text" {
				if text, ok := b["text"].(string); ok {
					sb.WriteString(text)
					sb.WriteString("\n")
				}
			}
		}
		return strings.TrimSpace(sb.String())
	}
	return string(raw)
}

// hasMultiPartContent returns true if any message has array content (vision).
func hasMultiPartContent(messages []oaiMessage) bool {
	for _, m := range messages {
		if len(m.Content) > 0 && m.Content[0] == '[' {
			return true
		}
	}
	return false
}

// rawContentForQuery returns the content suitable for sendQuery.
// For vision messages, returns the raw array; for text, returns the string.
func rawContentForQuery(messages []oaiMessage) interface{} {
	// Find the last user message
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			// If it is an array, return parsed array for multi-part
			var arr []interface{}
			if err := json.Unmarshal(messages[i].Content, &arr); err == nil {
				return arr
			}
			// Otherwise return as string
			return extractTextContent(messages[i].Content)
		}
	}
	return messagesToPrompt(messages)
}

// marshalString converts a Go string to a json.RawMessage (JSON string).
func marshalString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return json.RawMessage(b)
}

func messagesToPrompt(messages []oaiMessage) string {
	var sb strings.Builder
	for _, m := range messages {
		text := extractTextContent(m.Content)
		switch m.Role {
		case "system":
			sb.WriteString("[System]\n")
			sb.WriteString(text)
			sb.WriteString("\n\n")
		case "user":
			sb.WriteString(text)
			sb.WriteString("\n")
		case "assistant":
			sb.WriteString("[Previous assistant response]\n")
			sb.WriteString(text)
			sb.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(sb.String())
}

// ─── Handlers ────────────────────────────────────────────────────────────────

// handlePoolChatCompletions handles POST /v1/chat/completions.
func handlePoolChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOAIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is supported")
		return
	}

	if globalPool == nil {
		writeOAIError(w, http.StatusServiceUnavailable, "pool_disabled", "Pool is not enabled")
		return
	}

	// Auth check
	if globalPool.config.APIKey != "" {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+globalPool.config.APIKey {
			writeOAIError(w, http.StatusUnauthorized, "invalid_api_key", "Invalid API key")
			return
		}
	}

	var req oaiChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOAIError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON: "+err.Error())
		return
	}

	if len(req.Messages) == 0 {
		writeOAIError(w, http.StatusBadRequest, "invalid_request", "Messages array is empty")
		return
	}

	model, ok := resolveModel(req.Model)
	if !ok {
		writeOAIError(w, http.StatusBadRequest, "invalid_model", fmt.Sprintf("Unknown model: %s", req.Model))
		return
	}

	reqID := generateReqID()
	var queryContent interface{}
	var prompt string
	if hasMultiPartContent(req.Messages) {
		queryContent = rawContentForQuery(req.Messages)
		prompt = "[vision request]"
	} else {
		prompt = messagesToPrompt(req.Messages)
		queryContent = prompt
	}

	// Acquire a slot
	slot, err := globalPool.Acquire(r.Context(), model)
	if err != nil {
		globalPool.logRequest(reqID, model, "", truncateStr(prompt, 200), "error", 0, 0, 0, 0, 0, "pool_exhausted", err.Error())
		writeOAIError(w, http.StatusTooManyRequests, "pool_exhausted", "All slots busy for model: "+model)
		return
	}
	defer globalPool.Release(slot)

	start := time.Now()

	// Send query
	if err := slot.sendQuery(queryContent); err != nil {
		slot.mu.Lock()
		slot.state = slotDead
		slot.mu.Unlock()
		globalPool.logRequest(reqID, model, slot.ID, truncateStr(prompt, 200), "error", 0, 0, 0, 0, 0, "send_failed", err.Error())
		writeOAIError(w, http.StatusInternalServerError, "send_failed", "Failed to send query to slot")
		return
	}

	if req.Stream {
		handleStreamingResponse(w, r, slot, reqID, model, prompt, start)
	} else {
		handleNonStreamingResponse(w, r, slot, reqID, model, prompt, start)
	}
}

// handleNonStreamingResponse collects the full response and returns a single JSON object.
func handleNonStreamingResponse(w http.ResponseWriter, r *http.Request, slot *PoolSlot, reqID, model, prompt string, start time.Time) {
	var fullText strings.Builder
	var resultEv poolEvent
	var ttft time.Duration

	for {
		ev, err := readEventWithCtx(r.Context(), slot)
		if err != nil {
			slot.mu.Lock()
			slot.state = slotDead
			slot.mu.Unlock()
			globalPool.logRequest(reqID, model, slot.ID, truncateStr(prompt, 200), "error", 0, 0, int(time.Since(start).Milliseconds()), 0, 0, "read_failed", err.Error())
			writeOAIError(w, http.StatusInternalServerError, "read_failed", "Failed reading response")
			return
		}

		switch ev.Type {
		case "assistant":
			if ttft == 0 {
				ttft = time.Since(start)
			}
			text := extractAssistantText(ev)
			fullText.WriteString(text)

		case "stream_event":
			if ttft == 0 {
				ttft = time.Since(start)
			}
			text := extractStreamText(ev)
			fullText.WriteString(text)

		case "rate_limit_event":
			globalPool.handleRateLimit(slot, ev)

		case "system":
			if ev.Subtype == "api_retry" {
				log.Printf("pool: %s api retry: %s", slot.ID, ev.Subtype)
			}

		case "result":
			resultEv = ev
			goto done
		}
	}

done:
	latency := time.Since(start)
	slot.mu.Lock()
	slot.errorCount = 0 // successful response
	slot.totalCost += resultEv.CostUSD
	slot.totalRequests++
	slot.mu.Unlock()
	globalPool.totalCost.Add(int64(resultEv.CostUSD * 1e6))

	tokensIn, tokensOut := 0, 0
	if resultEv.Usage != nil {
		tokensIn = resultEv.Usage.InputTokens
		tokensOut = resultEv.Usage.OutputTokens
	}

	// Handle error result
	if resultEv.IsError {
		action := classifyResultError(resultEv)
		if action == "disable" || action == "recycle" {
			slot.mu.Lock()
			slot.state = slotDead
			slot.mu.Unlock()
		}
		globalPool.logRequest(reqID, model, slot.ID, truncateStr(prompt, 200), "error", tokensIn, tokensOut,
			int(latency.Milliseconds()), int(ttft.Milliseconds()), resultEv.CostUSD, "result_error", resultEv.Result)
		writeOAIError(w, http.StatusInternalServerError, "model_error", resultEv.Result)
		return
	}

	text := fullText.String()
	if text == "" {
		text = resultEv.Result
	}

	globalPool.logRequest(reqID, model, slot.ID, truncateStr(prompt, 200), "complete", tokensIn, tokensOut,
		int(latency.Milliseconds()), int(ttft.Milliseconds()), resultEv.CostUSD, "", "")

	resp := oaiChatResponse{
		ID:      reqID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []oaiChoice{{
			Index:        0,
			Message:      oaiMessage{Role: "assistant", Content: marshalString(text)},
			FinishReason: mapStopReason(resultEv.StopReason),
		}},
		Usage: oaiUsage{
			PromptTokens:     tokensIn,
			CompletionTokens: tokensOut,
			TotalTokens:      tokensIn + tokensOut,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleStreamingResponse streams SSE chunks as events arrive.
func handleStreamingResponse(w http.ResponseWriter, r *http.Request, slot *PoolSlot, reqID, model, prompt string, start time.Time) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOAIError(w, http.StatusInternalServerError, "streaming_unsupported", "Response writer does not support flushing")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// First chunk: role
	writeSSEChunk(w, flusher, reqID, model, oaiDelta{Role: "assistant"}, nil)

	var ttft time.Duration
	var resultEv poolEvent

	for {
		ev, err := readEventWithCtx(r.Context(), slot)
		if err != nil {
			slot.mu.Lock()
			slot.state = slotDead
			slot.mu.Unlock()
			globalPool.logRequest(reqID, model, slot.ID, truncateStr(prompt, 200), "error", 0, 0,
				int(time.Since(start).Milliseconds()), 0, 0, "read_failed", err.Error())
			return
		}

		switch ev.Type {
		case "assistant":
			if ttft == 0 {
				ttft = time.Since(start)
			}
			text := extractAssistantText(ev)
			if text != "" {
				writeSSEChunk(w, flusher, reqID, model, oaiDelta{Content: text}, nil)
			}

		case "stream_event":
			if ttft == 0 {
				ttft = time.Since(start)
			}
			text := extractStreamText(ev)
			if text != "" {
				writeSSEChunk(w, flusher, reqID, model, oaiDelta{Content: text}, nil)
			}

		case "rate_limit_event":
			globalPool.handleRateLimit(slot, ev)

		case "system":
			// Skip init and retry events during streaming

		case "result":
			resultEv = ev
			goto done
		}
	}

done:
	latency := time.Since(start)
	slot.mu.Lock()
	slot.errorCount = 0
	slot.totalCost += resultEv.CostUSD
	slot.totalRequests++
	slot.mu.Unlock()
	globalPool.totalCost.Add(int64(resultEv.CostUSD * 1e6))

	tokensIn, tokensOut := 0, 0
	if resultEv.Usage != nil {
		tokensIn = resultEv.Usage.InputTokens
		tokensOut = resultEv.Usage.OutputTokens
	}

	if resultEv.IsError {
		action := classifyResultError(resultEv)
		if action == "disable" || action == "recycle" {
			slot.mu.Lock()
			slot.state = slotDead
			slot.mu.Unlock()
		}
		globalPool.logRequest(reqID, model, slot.ID, truncateStr(prompt, 200), "error", tokensIn, tokensOut,
			int(latency.Milliseconds()), int(ttft.Milliseconds()), resultEv.CostUSD, "result_error", resultEv.Result)
	} else {
		globalPool.logRequest(reqID, model, slot.ID, truncateStr(prompt, 200), "complete", tokensIn, tokensOut,
			int(latency.Milliseconds()), int(ttft.Milliseconds()), resultEv.CostUSD, "", "")
	}

	// Final chunk with finish_reason
	stop := mapStopReason(resultEv.StopReason)
	writeSSEChunk(w, flusher, reqID, model, oaiDelta{}, &stop)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// ─── SSE helpers ─────────────────────────────────────────────────────────────

func writeSSEChunk(w http.ResponseWriter, flusher http.Flusher, id, model string, delta oaiDelta, finishReason *string) {
	chunk := oaiChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []oaiChunkChoice{{
			Index:        0,
			Delta:        delta,
			FinishReason: finishReason,
		}},
	}
	data, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// ─── Event parsing ───────────────────────────────────────────────────────────

func extractAssistantText(ev poolEvent) string {
	if ev.Message == nil {
		return ""
	}
	var msg poolAssistantMessage
	if err := json.Unmarshal(ev.Message, &msg); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, block := range msg.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	return sb.String()
}

func extractStreamText(ev poolEvent) string {
	if ev.Event == nil {
		return ""
	}
	var se poolStreamEvent
	if err := json.Unmarshal(ev.Event, &se); err != nil {
		return ""
	}
	if se.Delta != nil && se.Delta.Type == "text_delta" {
		return se.Delta.Text
	}
	return ""
}

func readEventWithCtx(ctx context.Context, slot *PoolSlot) (poolEvent, error) {
	evCh := make(chan poolEvent, 1)
	errCh := make(chan error, 1)
	go func() {
		ev, err := slot.readEvent()
		if err != nil {
			errCh <- err
		} else {
			evCh <- ev
		}
	}()

	select {
	case ev := <-evCh:
		return ev, nil
	case err := <-errCh:
		return poolEvent{}, err
	case <-ctx.Done():
		return poolEvent{}, ctx.Err()
	}
}

func mapStopReason(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	default:
		return "stop"
	}
}

// ─── List models ─────────────────────────────────────────────────────────────

func handlePoolListModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOAIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is supported")
		return
	}

	models := make([]oaiModel, 0)
	if globalPool != nil {
		for _, model := range globalPool.config.Models {
			models = append(models, oaiModel{
				ID:      model,
				Object:  "model",
				Created: time.Now().Unix(),
				OwnedBy: "anthropic",
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(oaiModelList{
		Object: "list",
		Data:   models,
	})
}

// ─── Pool status ─────────────────────────────────────────────────────────────

func handlePoolStatusAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if globalPool == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"enabled": false})
		return
	}
	json.NewEncoder(w).Encode(globalPool.Status())
}

// ─── Error response ──────────────────────────────────────────────────────────

func writeOAIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(oaiError{
		Error: oaiErrorDetail{
			Message: message,
			Type:    "error",
			Code:    code,
		},
	})
}

// ─── ID generation ───────────────────────────────────────────────────────────

func generateReqID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "swarm-" + hex.EncodeToString(b)
}
