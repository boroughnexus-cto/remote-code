package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// handleMCPHTTP handles POST /mcp for the streamable-http MCP transport.
// Supports SSE (preferred) and plain JSON response based on Accept header.
func handleMCPHTTP(server *MCPServer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req MCPRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONRPCError(w, nil, -32700, "parse error: "+err.Error())
			return
		}

		if req.JSONRPC != "2.0" {
			writeJSONRPCError(w, req.ID, -32600, "invalid request: jsonrpc must be 2.0")
			return
		}

		log.Printf("mcp: %s %s", r.Method, req.Method)

		resp := server.HandleRequest(r.Context(), req)

		// notifications/initialized is a no-op with no response
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Check if client accepts SSE
		acceptsSSE := strings.Contains(r.Header.Get("Accept"), "text/event-stream")

		if acceptsSSE {
			writeSSEResponse(w, resp)
		} else {
			writeJSONResponse(w, resp)
		}
	}
}

// writeSSEResponse writes a JSON-RPC response as an SSE event.
func writeSSEResponse(w http.ResponseWriter, resp MCPResponse) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf("mcp: SSE marshal error: %v", err)
		return
	}

	fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// writeJSONResponse writes a JSON-RPC response as plain JSON.
func writeJSONResponse(w http.ResponseWriter, resp MCPResponse) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// writeJSONRPCError writes a JSON-RPC error response (used for transport-level errors).
func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK) // JSON-RPC errors use 200 status
	json.NewEncoder(w).Encode(MCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &MCPError{Code: code, Message: message},
	})
}

// ─── Stdio transport ────────────────────────────────────────────────────────

// stdioDecode is the result of a json.Decoder.Decode call, sent over a channel
// to make blocking reads interruptible by context cancellation.
type stdioDecode struct {
	req MCPRequest
	err error
}

// runMCPStdio runs the MCP server over stdin/stdout using newline-delimited JSON-RPC.
// Uses json.Decoder for robustness with large payloads (no bufio.Scanner size limits).
// Decode runs in a goroutine so ctx cancellation can interrupt the read loop.
func runMCPStdio(server *MCPServer) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals for graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		cancel()
	}()

	decoder := json.NewDecoder(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)

	// Decode loop runs in a goroutine to avoid blocking the select on ctx.Done()
	decodeCh := make(chan stdioDecode, 1)
	go func() {
		for {
			var req MCPRequest
			err := decoder.Decode(&req)
			decodeCh <- stdioDecode{req: req, err: err}
			if err != nil {
				return // EOF or fatal error — stop decoding
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-decodeCh:
			if msg.err != nil {
				if msg.err == io.EOF {
					return
				}
				writeStdioResponse(writer, MCPResponse{
					JSONRPC: "2.0",
					Error:   &MCPError{Code: -32700, Message: "parse error: " + msg.err.Error()},
				})
				return // fatal decode error — exit
			}

			if msg.req.JSONRPC != "2.0" {
				writeStdioResponse(writer, MCPResponse{
					JSONRPC: "2.0",
					ID:      msg.req.ID,
					Error:   &MCPError{Code: -32600, Message: "invalid request: jsonrpc must be 2.0"},
				})
				continue
			}

			resp := server.HandleRequest(ctx, msg.req)

			// notifications don't get responses
			if msg.req.Method == "notifications/initialized" {
				continue
			}

			writeStdioResponse(writer, resp)
		}
	}
}

// writeStdioResponse writes a JSON-RPC response as a single line to stdout.
func writeStdioResponse(w *bufio.Writer, resp MCPResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf("mcp: stdio marshal error: %v", err)
		return
	}
	w.Write(data)
	w.WriteByte('\n')
	w.Flush()
}
