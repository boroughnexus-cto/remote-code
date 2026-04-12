package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// ─── JSON-RPC types ─────────────────────────────────────────────────────────

type MCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type MCPResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *MCPError       `json:"error,omitempty"`
}

type MCPError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// ─── MCP tool types ─────────────────────────────────────────────────────────

type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

type ToolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

type ToolResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ─── Tool registry ──────────────────────────────────────────────────────────

type ToolHandler func(ctx context.Context, args map[string]interface{}) (interface{}, error)

type registeredTool struct {
	Definition ToolDefinition
	Handler    ToolHandler
}

type ToolRegistry struct {
	tools []registeredTool
	index map[string]int // name → index into tools
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		index: make(map[string]int),
	}
}

func (r *ToolRegistry) Register(def ToolDefinition, handler ToolHandler) {
	r.index[def.Name] = len(r.tools)
	r.tools = append(r.tools, registeredTool{Definition: def, Handler: handler})
}

func (r *ToolRegistry) Definitions() []ToolDefinition {
	defs := make([]ToolDefinition, len(r.tools))
	for i, t := range r.tools {
		defs[i] = t.Definition
	}
	return defs
}

func (r *ToolRegistry) Get(name string) (ToolHandler, bool) {
	idx, ok := r.index[name]
	if !ok {
		return nil, false
	}
	return r.tools[idx].Handler, true
}

// ─── MCP Server ─────────────────────────────────────────────────────────────

type MCPServer struct {
	services *Services
	registry *ToolRegistry
}

func NewMCPServer(svc *Services, enablePoolTools bool) *MCPServer {
	s := &MCPServer{
		services: svc,
		registry: NewToolRegistry(),
	}
	registerReadTools(s.registry, svc)
	registerWriteTools(s.registry, svc, enablePoolTools)
	return s
}

// HandleRequest dispatches a parsed JSON-RPC request to the appropriate handler.
func (s *MCPServer) HandleRequest(ctx context.Context, req MCPRequest) MCPResponse {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "notifications/initialized":
		// No-op acknowledgment — no response needed for notifications
		return MCPResponse{}
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	case "ping":
		return MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]interface{}{},
		}
	default:
		return MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &MCPError{
				Code:    -32601,
				Message: fmt.Sprintf("method not found: %s", req.Method),
			},
		}
	}
}

func (s *MCPServer) handleInitialize(req MCPRequest) MCPResponse {
	return MCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]interface{}{
				"name":    "swarmops",
				"version": "2.0",
			},
		},
	}
}

func (s *MCPServer) handleToolsList(req MCPRequest) MCPResponse {
	return MCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"tools": s.registry.Definitions(),
		},
	}
}

func (s *MCPServer) handleToolsCall(ctx context.Context, req MCPRequest) MCPResponse {
	var params ToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &MCPError{
				Code:    -32602,
				Message: "invalid params: " + err.Error(),
			},
		}
	}

	handler, ok := s.registry.Get(params.Name)
	if !ok {
		return MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &MCPError{
				Code:    -32602,
				Message: fmt.Sprintf("unknown tool: %s", params.Name),
			},
		}
	}

	start := time.Now()
	result, err := safeCallTool(ctx, params.Name, params.Arguments, handler)
	duration := time.Since(start)
	log.Printf("mcp: tools/call %s (%v)", params.Name, duration)

	if err != nil {
		// Tool execution errors → ToolResult with IsError, not JSON-RPC error
		return MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  errorToolResult(err.Error()),
		}
	}

	// Marshal result to JSON text for the tool content
	data, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  errorToolResult("failed to marshal result: " + marshalErr.Error()),
		}
	}

	return MCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: ToolResult{
			Content: []ToolContent{{
				Type: "text",
				Text: string(data),
			}},
		},
	}
}

// safeCallTool wraps a tool handler with panic recovery.
func safeCallTool(ctx context.Context, name string, args map[string]interface{}, handler ToolHandler) (result interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("mcp: PANIC in tool %s: %v", name, r)
			err = fmt.Errorf("internal error in tool %s", name)
		}
	}()
	return handler(ctx, args)
}

func errorToolResult(msg string) ToolResult {
	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: msg}},
		IsError: true,
	}
}

// ─── Helpers ────────────────────────────────────────────────────────────────

// jsonSchema builds a minimal JSON Schema object for tool input definitions.
func jsonSchema(properties map[string]interface{}, required []string) interface{} {
	s := map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func stringProp(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": desc}
}

func boolProp(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "boolean", "description": desc}
}

func arrayOfStringsProp(desc string) map[string]interface{} {
	return map[string]interface{}{
		"type":        "array",
		"description": desc,
		"items":       map[string]interface{}{"type": "string"},
	}
}

// getStringArg extracts a string from tool arguments with a default.
func getStringArg(args map[string]interface{}, key, def string) string {
	v, ok := args[key]
	if !ok {
		return def
	}
	s, ok := v.(string)
	if !ok {
		return def
	}
	return s
}

// getBoolArg extracts a bool from tool arguments with a default.
func getBoolArg(args map[string]interface{}, key string, def bool) bool {
	v, ok := args[key]
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

// getStringSliceArg extracts a []string from tool arguments.
func getStringSliceArg(args map[string]interface{}, key string) []string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}
