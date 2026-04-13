package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ─── MCP client for calling remote MCP servers from the TUI ─────────────────

const contextMCPURL = "https://mcp-tkn-context.gate-hexatonic.ts.net/mcp"

// mcpToolCall makes a JSON-RPC tools/call to an MCP server via streamable-http.
// Returns the text content blocks from the tool result.
func mcpToolCall(serverURL, toolName string, args map[string]interface{}) ([]string, error) {
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      toolName,
			"arguments": args,
		},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", serverURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// Parse SSE response: extract "data: " lines
	var jsonData []byte
	for _, line := range strings.Split(string(respBody), "\n") {
		if strings.HasPrefix(line, "data: ") {
			jsonData = []byte(strings.TrimPrefix(line, "data: "))
			break
		}
	}
	if jsonData == nil {
		// Try as plain JSON
		jsonData = respBody
	}

	var rpcResp struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(jsonData, &rpcResp); err != nil {
		return nil, fmt.Errorf("parse MCP response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("MCP error: %s", rpcResp.Error.Message)
	}

	var texts []string
	for _, c := range rpcResp.Result.Content {
		if c.Type == "text" {
			texts = append(texts, c.Text)
		}
	}
	return texts, nil
}

// ─── Context fetching (uses mcpToolCall) ────────────────────────────────────

// contextContentMsg is the result of fetching rendered context content.
type contextContentMsg struct {
	name    string
	content string
	err     error
}

func fetchContexts() tea.Cmd {
	return func() tea.Msg {
		result, err := mcpToolCall(contextMCPURL, "context_list", map[string]interface{}{"limit": 50})
		if err != nil {
			return contextListMsg(nil)
		}
		var items []contextItem
		for _, block := range result {
			var item contextItem
			if json.Unmarshal([]byte(block), &item) == nil && item.Name != "" {
				items = append(items, item)
			}
		}
		return contextListMsg(items)
	}
}

func fetchContextContent(name string) tea.Cmd {
	return func() tea.Msg {
		result, err := mcpToolCall(contextMCPURL, "context_render", map[string]interface{}{"name_or_id": name})
		if err != nil {
			return contextContentMsg{name: name, err: err}
		}
		if len(result) > 0 {
			return contextContentMsg{name: name, content: result[0]}
		}
		return contextContentMsg{name: name, err: fmt.Errorf("empty response")}
	}
}
