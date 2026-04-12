package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testServices creates a Services instance with no real dependencies.
// Tools that hit the DB or tmux will fail gracefully.
func testServices() *Services {
	return &Services{db: nil, pool: nil, config: nil}
}

func testMCPServer() *MCPServer {
	return NewMCPServer(testServices(), false)
}

// ─── JSON-RPC parsing ───────────────────────────────────────────────────────

func TestParseMCPRequest(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, false},
		{"valid ping", `{"jsonrpc":"2.0","id":2,"method":"ping"}`, false},
		{"missing method", `{"jsonrpc":"2.0","id":1}`, false}, // parses, but method is empty
		{"invalid json", `{not json}`, true},
		{"empty", ``, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req MCPRequest
			err := json.Unmarshal([]byte(tt.input), &req)
			if (err != nil) != tt.wantErr {
				t.Errorf("Unmarshal(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// ─── Method routing ─────────────────────────────────────────────────────────

func TestHandleRequest_Initialize(t *testing.T) {
	server := testMCPServer()
	req := MCPRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	}

	resp := server.HandleRequest(context.Background(), req)

	if resp.Error != nil {
		t.Fatalf("expected no error, got: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", resp.Result)
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("expected protocolVersion 2024-11-05, got %v", result["protocolVersion"])
	}
	serverInfo, ok := result["serverInfo"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected serverInfo map, got %T", result["serverInfo"])
	}
	if serverInfo["name"] != "swarmops" {
		t.Errorf("expected server name swarmops, got %v", serverInfo["name"])
	}
}

func TestHandleRequest_Ping(t *testing.T) {
	server := testMCPServer()
	req := MCPRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`42`),
		Method:  "ping",
	}

	resp := server.HandleRequest(context.Background(), req)

	if resp.Error != nil {
		t.Fatalf("expected no error, got: %v", resp.Error)
	}
	if string(resp.ID) != "42" {
		t.Errorf("expected ID 42, got %s", resp.ID)
	}
}

func TestHandleRequest_MethodNotFound(t *testing.T) {
	server := testMCPServer()
	req := MCPRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "nonexistent/method",
	}

	resp := server.HandleRequest(context.Background(), req)

	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("expected error code -32601, got %d", resp.Error.Code)
	}
}

func TestHandleRequest_NotificationsInitialized(t *testing.T) {
	server := testMCPServer()
	req := MCPRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}

	resp := server.HandleRequest(context.Background(), req)

	// Notifications return empty response (no ID, no result, no error)
	if resp.Error != nil {
		t.Fatalf("expected no error for notification, got: %v", resp.Error)
	}
}

// ─── tools/list ─────────────────────────────────────────────────────────────

func TestHandleRequest_ToolsList(t *testing.T) {
	server := testMCPServer()
	req := MCPRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/list",
	}

	resp := server.HandleRequest(context.Background(), req)

	if resp.Error != nil {
		t.Fatalf("expected no error, got: %v", resp.Error)
	}

	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", resp.Result)
	}
	tools, ok := result["tools"].([]ToolDefinition)
	if !ok {
		t.Fatalf("expected []ToolDefinition, got %T", result["tools"])
	}

	// Should have at least the core tools (no pool tools since enablePoolTools=false)
	if len(tools) < 20 {
		t.Errorf("expected at least 20 tools, got %d", len(tools))
	}

	// Verify rc_health_check exists
	found := false
	for _, tool := range tools {
		if tool.Name == "rc_health_check" {
			found = true
			break
		}
	}
	if !found {
		t.Error("rc_health_check tool not found in tools list")
	}

	// Verify pool tools are NOT present
	for _, tool := range tools {
		if tool.Name == "pool_status" || tool.Name == "pool_chat" {
			t.Errorf("pool tool %s should not be present when pool tools are disabled", tool.Name)
		}
	}
}

func TestHandleRequest_ToolsListWithPoolTools(t *testing.T) {
	server := NewMCPServer(testServices(), true)
	req := MCPRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/list",
	}

	resp := server.HandleRequest(context.Background(), req)
	result := resp.Result.(map[string]interface{})
	tools := result["tools"].([]ToolDefinition)

	poolFound := false
	for _, tool := range tools {
		if tool.Name == "pool_status" {
			poolFound = true
		}
	}
	if !poolFound {
		t.Error("pool_status tool should be present when pool tools are enabled")
	}
}

// ─── tools/call ─────────────────────────────────────────────────────────────

func TestHandleRequest_ToolsCall_HealthCheck(t *testing.T) {
	server := testMCPServer()
	req := MCPRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`5`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"rc_health_check","arguments":{}}`),
	}

	resp := server.HandleRequest(context.Background(), req)

	if resp.Error != nil {
		t.Fatalf("expected no error, got: %v", resp.Error)
	}

	// Result should be a ToolResult
	result, ok := resp.Result.(ToolResult)
	if !ok {
		t.Fatalf("expected ToolResult, got %T", resp.Result)
	}
	if result.IsError {
		t.Error("expected non-error result")
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	if result.Content[0].Type != "text" {
		t.Errorf("expected content type text, got %s", result.Content[0].Type)
	}
	if !strings.Contains(result.Content[0].Text, `"status":"ok"`) {
		t.Errorf("expected status ok in content, got: %s", result.Content[0].Text)
	}
}

func TestHandleRequest_ToolsCall_UnknownTool(t *testing.T) {
	server := testMCPServer()
	req := MCPRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`6`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"nonexistent_tool","arguments":{}}`),
	}

	resp := server.HandleRequest(context.Background(), req)

	if resp.Error == nil {
		t.Fatal("expected error for unknown tool")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("expected error code -32602, got %d", resp.Error.Code)
	}
}

func TestHandleRequest_ToolsCall_InvalidParams(t *testing.T) {
	server := testMCPServer()
	req := MCPRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`7`),
		Method:  "tools/call",
		Params:  json.RawMessage(`"not an object"`),
	}

	resp := server.HandleRequest(context.Background(), req)

	if resp.Error == nil {
		t.Fatal("expected error for invalid params")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("expected error code -32602, got %d", resp.Error.Code)
	}
}

// ─── Panic recovery ─────────────────────────────────────────────────────────

func TestSafeCallTool_PanicRecovery(t *testing.T) {
	panicker := func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		panic("test panic")
	}

	_, err := safeCallTool(context.Background(), "test_panic", nil, panicker)
	if err == nil {
		t.Fatal("expected error from panic recovery")
	}
	if !strings.Contains(err.Error(), "internal error") {
		t.Errorf("expected internal error message, got: %v", err)
	}
}

// ─── SSE conformance (HTTP transport) ───────────────────────────────────────

func TestHTTPTransport_SSE(t *testing.T) {
	server := testMCPServer()
	handler := handleMCPHTTP(server)

	body := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	w := httptest.NewRecorder()
	handler(w, req)

	resp := w.Result()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %s", ct)
	}

	// Parse SSE response
	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)

	if !strings.HasPrefix(bodyStr, "event: message\ndata: ") {
		t.Errorf("expected SSE format, got: %q", bodyStr)
	}

	// Extract JSON from SSE data line
	dataLine := strings.TrimPrefix(bodyStr, "event: message\ndata: ")
	dataLine = strings.TrimSuffix(dataLine, "\n\n")

	var mcpResp MCPResponse
	if err := json.Unmarshal([]byte(dataLine), &mcpResp); err != nil {
		t.Fatalf("failed to parse SSE data as JSON-RPC: %v", err)
	}
	if mcpResp.Error != nil {
		t.Errorf("expected no error in response, got: %v", mcpResp.Error)
	}
	if string(mcpResp.ID) != "1" {
		t.Errorf("expected ID 1, got %s", mcpResp.ID)
	}
}

func TestHTTPTransport_PlainJSON(t *testing.T) {
	server := testMCPServer()
	handler := handleMCPHTTP(server)

	body := `{"jsonrpc":"2.0","id":2,"method":"ping"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No Accept header → should get plain JSON

	w := httptest.NewRecorder()
	handler(w, req)

	resp := w.Result()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}

	var mcpResp MCPResponse
	if err := json.NewDecoder(resp.Body).Decode(&mcpResp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if string(mcpResp.ID) != "2" {
		t.Errorf("expected ID 2, got %s", mcpResp.ID)
	}
}

func TestHTTPTransport_MethodNotAllowed(t *testing.T) {
	server := testMCPServer()
	handler := handleMCPHTTP(server)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHTTPTransport_InvalidJSON(t *testing.T) {
	server := testMCPServer()
	handler := handleMCPHTTP(server)

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{broken"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	var mcpResp MCPResponse
	json.NewDecoder(w.Body).Decode(&mcpResp)
	if mcpResp.Error == nil || mcpResp.Error.Code != -32700 {
		t.Errorf("expected parse error (-32700), got: %+v", mcpResp.Error)
	}
}

func TestHTTPTransport_NotificationsInitialized(t *testing.T) {
	server := testMCPServer()
	handler := handleMCPHTTP(server)

	body := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204 for notification, got %d", w.Code)
	}
}

// ─── Stdio roundtrip ────────────────────────────────────────────────────────

func TestStdioTransport_Roundtrip(t *testing.T) {
	server := testMCPServer()

	// Simulate stdin with a ping request
	input := `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"
	stdin := strings.NewReader(input)
	var stdout bytes.Buffer

	// Run the stdio loop manually (can't use runMCPStdio since it uses os.Stdin)
	decoder := json.NewDecoder(stdin)
	writer := bufio.NewWriter(&stdout)

	var req MCPRequest
	if err := decoder.Decode(&req); err != nil {
		t.Fatalf("failed to decode request: %v", err)
	}

	resp := server.HandleRequest(context.Background(), req)
	data, _ := json.Marshal(resp)
	writer.Write(data)
	writer.WriteByte('\n')
	writer.Flush()

	// Parse response
	var mcpResp MCPResponse
	if err := json.Unmarshal(stdout.Bytes(), &mcpResp); err != nil {
		t.Fatalf("failed to parse stdio response: %v", err)
	}
	if mcpResp.Error != nil {
		t.Errorf("expected no error, got: %v", mcpResp.Error)
	}
	if string(mcpResp.ID) != "1" {
		t.Errorf("expected ID 1, got %s", mcpResp.ID)
	}
}

func TestStdioTransport_Initialize_ToolsList_Roundtrip(t *testing.T) {
	server := testMCPServer()

	// Full MCP handshake: initialize → notifications/initialized → tools/list
	messages := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}

	var responses []MCPResponse
	for _, msg := range messages {
		var req MCPRequest
		json.Unmarshal([]byte(msg), &req)
		resp := server.HandleRequest(context.Background(), req)
		// Skip empty responses (notifications)
		if resp.JSONRPC != "" || resp.ID != nil || resp.Error != nil || resp.Result != nil {
			responses = append(responses, resp)
		}
	}

	// Should have 2 responses (initialize + tools/list)
	if len(responses) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(responses))
	}

	// Verify initialize response
	if responses[0].Error != nil {
		t.Errorf("initialize error: %v", responses[0].Error)
	}

	// Verify tools/list response has tools
	result, ok := responses[1].Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result for tools/list")
	}
	tools, ok := result["tools"].([]ToolDefinition)
	if !ok || len(tools) == 0 {
		t.Error("expected non-empty tools list")
	}
}

// ─── Tool registry ──────────────────────────────────────────────────────────

func TestToolRegistry_RegisterAndGet(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(
		ToolDefinition{Name: "test_tool", Description: "A test tool"},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return "ok", nil
		},
	)

	handler, ok := reg.Get("test_tool")
	if !ok {
		t.Fatal("expected to find test_tool")
	}

	result, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %v", result)
	}

	_, ok = reg.Get("nonexistent")
	if ok {
		t.Error("expected not to find nonexistent tool")
	}
}

func TestToolRegistry_Definitions(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(ToolDefinition{Name: "a", Description: "tool a"}, nil)
	reg.Register(ToolDefinition{Name: "b", Description: "tool b"}, nil)

	defs := reg.Definitions()
	if len(defs) != 2 {
		t.Fatalf("expected 2 definitions, got %d", len(defs))
	}
	if defs[0].Name != "a" || defs[1].Name != "b" {
		t.Error("definitions not in registration order")
	}
}

// ─── Arg helpers ────────────────────────────────────────────────────────────

func TestGetStringArg(t *testing.T) {
	args := map[string]interface{}{
		"name": "test",
		"num":  42,
	}

	if v := getStringArg(args, "name", ""); v != "test" {
		t.Errorf("expected 'test', got %q", v)
	}
	if v := getStringArg(args, "missing", "default"); v != "default" {
		t.Errorf("expected 'default', got %q", v)
	}
	if v := getStringArg(args, "num", "fallback"); v != "fallback" {
		t.Errorf("expected 'fallback' for non-string, got %q", v)
	}
}

func TestGetBoolArg(t *testing.T) {
	args := map[string]interface{}{
		"flag":   true,
		"string": "not a bool",
	}

	if v := getBoolArg(args, "flag", false); !v {
		t.Error("expected true")
	}
	if v := getBoolArg(args, "missing", true); !v {
		t.Error("expected default true")
	}
	if v := getBoolArg(args, "string", false); v {
		t.Error("expected false for non-bool")
	}
}

func TestGetStringSliceArg(t *testing.T) {
	args := map[string]interface{}{
		"files": []interface{}{"a.go", "b.go"},
		"empty": []interface{}{},
	}

	files := getStringSliceArg(args, "files")
	if len(files) != 2 || files[0] != "a.go" {
		t.Errorf("expected [a.go b.go], got %v", files)
	}

	empty := getStringSliceArg(args, "empty")
	if len(empty) != 0 {
		t.Errorf("expected empty slice, got %v", empty)
	}

	missing := getStringSliceArg(args, "missing")
	if missing != nil {
		t.Errorf("expected nil for missing key, got %v", missing)
	}
}
