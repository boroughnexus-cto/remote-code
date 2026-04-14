package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// anthropicAPIURL is the Anthropic streaming endpoint. Overridable in tests.
var anthropicAPIURL = "https://api.anthropic.com/v1/messages"

// ─── Anthropic API types ─────────────────────────────────────────────────────

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Stream    bool               `json:"stream"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ─── API key ─────────────────────────────────────────────────────────────────

func getAnthropicAPIKey() (string, error) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return "", fmt.Errorf("ANTHROPIC_API_KEY environment variable not set")
	}
	return key, nil
}

// ─── Message conversion ──────────────────────────────────────────────────────

// buildAnthropicRequest converts an OpenAI chat request to Anthropic format.
// System messages are concatenated into the top-level System field.
// User/assistant messages are placed in Messages in order.
func buildAnthropicRequest(req oaiChatRequest, model string) anthropicRequest {
	var systemParts []string
	var messages []anthropicMessage

	for _, m := range req.Messages {
		text := extractTextContent(m.Content)
		switch m.Role {
		case "system":
			if text != "" {
				systemParts = append(systemParts, text)
			}
		case "user", "assistant":
			messages = append(messages, anthropicMessage{
				Role:    m.Role,
				Content: text,
			})
		}
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 8096
	}

	return anthropicRequest{
		Model:     model,
		MaxTokens: maxTokens,
		System:    strings.Join(systemParts, "\n\n"),
		Messages:  messages,
		Stream:    true,
	}
}

// ─── SSE frame reader ────────────────────────────────────────────────────────

// sseEvent holds a parsed Anthropic SSE event frame.
type sseEvent struct {
	eventType string
	data      string
}

// sseFrameScanner returns a Scanner that yields one complete SSE frame per token
// (frames are blank-line delimited, robust to chunk boundaries).
func sseFrameScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	sc.Split(splitSSEFrames)
	return sc
}

// splitSSEFrames is a bufio.SplitFunc that splits on \n\n or \r\n\r\n boundaries.
func splitSSEFrames(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i := 0; i < len(data)-1; i++ {
		if data[i] == '\n' && data[i+1] == '\n' {
			return i + 2, bytes.TrimRight(data[:i], "\r"), nil
		}
		if i+3 < len(data) && data[i] == '\r' && data[i+1] == '\n' &&
			data[i+2] == '\r' && data[i+3] == '\n' {
			return i + 4, bytes.TrimRight(data[:i], "\r"), nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), bytes.TrimRight(data, "\r\n"), nil
	}
	return 0, nil, nil
}

// parseSSEFrame parses a raw SSE frame string into event type and data payload.
func parseSSEFrame(frame string) sseEvent {
	var ev sseEvent
	for _, line := range strings.Split(frame, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "event: ") {
			ev.eventType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			ev.data = strings.TrimPrefix(line, "data: ")
		}
	}
	return ev
}

// ─── Direct streaming handler ────────────────────────────────────────────────

// handleDirectStreamingResponse calls the Anthropic API directly with stream:true
// and forwards per-token SSE chunks to the client. Bypasses the warm pool entirely.
func handleDirectStreamingResponse(w http.ResponseWriter, r *http.Request, req oaiChatRequest, reqID, model, prompt string, start time.Time) {
	apiKey, err := getAnthropicAPIKey()
	if err != nil {
		logRequestIfPool(reqID, model, "", truncateStr(prompt, 200), "error", 0, 0, 0, 0, 0, "api_key_missing", err.Error())
		writeOAIError(w, http.StatusServiceUnavailable, "api_key_missing", "ANTHROPIC_API_KEY not configured")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOAIError(w, http.StatusInternalServerError, "streaming_unsupported", "Response writer does not support flushing")
		return
	}

	anthropicReq := buildAnthropicRequest(req, model)
	body, err := json.Marshal(anthropicReq)
	if err != nil {
		writeOAIError(w, http.StatusInternalServerError, "marshal_error", "Failed to marshal request")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicAPIURL, bytes.NewReader(body))
	if err != nil {
		writeOAIError(w, http.StatusInternalServerError, "request_build_error", err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		logRequestIfPool(reqID, model, "", truncateStr(prompt, 200), "error", 0, 0,
			int(time.Since(start).Milliseconds()), 0, 0, "upstream_error", err.Error())
		writeOAIError(w, http.StatusServiceUnavailable, "upstream_error", "Anthropic API request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("Anthropic API returned %d: %s", resp.StatusCode, string(errBody))
		statusCode := http.StatusInternalServerError
		errCode := "upstream_error"
		if resp.StatusCode == http.StatusTooManyRequests {
			statusCode = http.StatusTooManyRequests
			errCode = "rate_limited"
		}
		logRequestIfPool(reqID, model, "", truncateStr(prompt, 200), "error", 0, 0,
			int(time.Since(start).Milliseconds()), 0, 0, errCode, errMsg)
		writeOAIError(w, statusCode, errCode, errMsg)
		return
	}

	// Begin SSE response
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	writeSSEChunk(w, flusher, reqID, model, oaiDelta{Role: "assistant"}, nil)

	var ttft time.Duration
	var tokensIn, tokensOut int
	var stopReason string

	scanner := sseFrameScanner(resp.Body)
	for scanner.Scan() {
		frame := scanner.Text()
		if frame == "" {
			continue
		}
		ev := parseSSEFrame(frame)
		if ev.data == "" {
			continue
		}

		switch ev.eventType {
		case "content_block_delta":
			var d struct {
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(ev.data), &d); err != nil {
				continue
			}
			if d.Delta.Type == "text_delta" && d.Delta.Text != "" {
				if ttft == 0 {
					ttft = time.Since(start)
				}
				writeSSEChunk(w, flusher, reqID, model, oaiDelta{Content: d.Delta.Text}, nil)
			}

		case "message_start":
			var ms struct {
				Message struct {
					Usage struct {
						InputTokens int `json:"input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(ev.data), &ms); err == nil {
				tokensIn = ms.Message.Usage.InputTokens
			}

		case "message_delta":
			var md struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(ev.data), &md); err == nil {
				stopReason = md.Delta.StopReason
				tokensOut = md.Usage.OutputTokens
			}

		case "message_stop":
			goto done

		case "error":
			var apiErr struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(ev.data), &apiErr); err == nil {
				log.Printf("direct stream %s: Anthropic error event: %s", reqID, apiErr.Error.Message)
			}
			goto done
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		log.Printf("direct stream %s: SSE scanner error: %v", reqID, err)
	}

done:
	latency := time.Since(start)
	logRequestIfPool(reqID, model, "", truncateStr(prompt, 200), "complete",
		tokensIn, tokensOut, int(latency.Milliseconds()), int(ttft.Milliseconds()), 0, "", "")

	stop := mapStopReason(stopReason)
	writeSSEChunk(w, flusher, reqID, model, oaiDelta{}, &stop)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// logRequestIfPool calls globalPool.logRequest only when pool is non-nil.
func logRequestIfPool(reqID, model, slotID, prompt, status string, tokensIn, tokensOut, latencyMS, ttftMS int, costUSD float64, errType, errDetail string) {
	if globalPool != nil {
		globalPool.logRequest(reqID, model, slotID, prompt, status, tokensIn, tokensOut, latencyMS, ttftMS, costUSD, errType, errDetail)
	}
}
