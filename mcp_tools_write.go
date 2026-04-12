package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// registerWriteTools registers all write MCP tools and conditionally pool tools.
func registerWriteTools(reg *ToolRegistry, svc *Services, enablePoolTools bool) {
	// ─── rc_run_task ────────────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_run_task",
			Description: "[WRITE] Create and start a new Claude Code session in a tmux window.",
			InputSchema: jsonSchema(map[string]interface{}{
				"name":      stringProp("Session name (auto-generated if empty)"),
				"directory": stringProp("Working directory for the session (default: current directory)"),
			}, nil),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			name := getStringArg(args, "name", "")
			directory := getStringArg(args, "directory", "")
			return svc.RunTask(ctx, name, directory)
		},
	)

	// ─── rc_send_input ──────────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_send_input",
			Description: "[WRITE] Send text input to a running session's tmux terminal.",
			InputSchema: jsonSchema(map[string]interface{}{
				"session_id": stringProp("Session ID to send input to"),
				"input":      stringProp("Text to send to the session"),
			}, []string{"session_id", "input"}),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			sessionID := getStringArg(args, "session_id", "")
			input := getStringArg(args, "input", "")
			if sessionID == "" {
				return nil, fmt.Errorf("session_id is required")
			}
			if input == "" {
				return nil, fmt.Errorf("input is required")
			}
			if err := svc.SendInput(ctx, sessionID, input); err != nil {
				return nil, err
			}
			return map[string]string{"status": "ok"}, nil
		},
	)

	// ─── rc_accept_execution ────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_accept_execution",
			Description: "[WRITE] Accept/acknowledge a completed execution (no-op, returns current state).",
			InputSchema: jsonSchema(map[string]interface{}{
				"id": stringProp("Execution/session ID"),
			}, []string{"id"}),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			id := getStringArg(args, "id", "")
			if id == "" {
				return nil, fmt.Errorf("id is required")
			}
			return svc.GetExecution(ctx, id)
		},
	)

	// ─── rc_delete_execution ────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_delete_execution",
			Description: "[WRITE] Delete a session and kill its tmux window.",
			InputSchema: jsonSchema(map[string]interface{}{
				"id": stringProp("Execution/session ID to delete"),
			}, []string{"id"}),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			id := getStringArg(args, "id", "")
			if id == "" {
				return nil, fmt.Errorf("id is required")
			}
			if err := svc.DeleteExecution(ctx, id); err != nil {
				return nil, err
			}
			return map[string]string{"status": "deleted"}, nil
		},
	)

	// ─── rc_wait_for_execution ──────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_wait_for_execution",
			Description: "[READ] Check execution status. Returns current state immediately — poll for updates.",
			InputSchema: jsonSchema(map[string]interface{}{
				"id": stringProp("Execution/session ID"),
			}, []string{"id"}),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			id := getStringArg(args, "id", "")
			if id == "" {
				return nil, fmt.Errorf("id is required")
			}
			return svc.ExecutionProgress(ctx, id)
		},
	)

	// ─── rc_git_add ─────────────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_git_add",
			Description: "[WRITE] Stage files for git commit.",
			InputSchema: jsonSchema(map[string]interface{}{
				"path":  stringProp("Repository path (default: current directory)"),
				"files": arrayOfStringsProp("Files to stage (e.g. [\".\"] for all)"),
			}, []string{"files"}),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			path := getStringArg(args, "path", ".")
			files := getStringSliceArg(args, "files")
			if len(files) == 0 {
				return nil, fmt.Errorf("files is required")
			}
			if err := svc.GitAdd(path, files); err != nil {
				return nil, err
			}
			return map[string]string{"status": "ok"}, nil
		},
	)

	// ─── rc_git_commit ──────────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_git_commit",
			Description: "[WRITE] Create a git commit with the given message.",
			InputSchema: jsonSchema(map[string]interface{}{
				"path":    stringProp("Repository path (default: current directory)"),
				"message": stringProp("Commit message"),
			}, []string{"message"}),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			path := getStringArg(args, "path", ".")
			message := getStringArg(args, "message", "")
			if message == "" {
				return nil, fmt.Errorf("message is required")
			}
			out, err := svc.GitCommit(path, message)
			if err != nil {
				return nil, fmt.Errorf("%w: %s", err, out)
			}
			return map[string]string{"output": out}, nil
		},
	)

	// ─── rc_git_push ────────────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_git_push",
			Description: "[WRITE] Push committed changes to remote.",
			InputSchema: jsonSchema(map[string]interface{}{
				"path": stringProp("Repository path (default: current directory)"),
			}, nil),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			path := getStringArg(args, "path", ".")
			out, err := svc.GitPush(path)
			if err != nil {
				return nil, fmt.Errorf("%w: %s", err, out)
			}
			return map[string]string{"output": out}, nil
		},
	)

	// ─── Pool tools (gated behind SWARMOPS_MCP_POOL_TOOLS) ──────────────

	if enablePoolTools {
		// ─── pool_status ────────────────────────────────────────────
		reg.Register(
			ToolDefinition{
				Name:        "pool_status",
				Description: "[READ] Get warm Claude CLI pool status (slots, models, costs, availability).",
				InputSchema: jsonSchema(map[string]interface{}{}, nil),
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				return svc.PoolStatus(), nil
			},
		)

		// ─── pool_chat ──────────────────────────────────────────────
		reg.Register(
			ToolDefinition{
				Name:        "pool_chat",
				Description: "[WRITE] Send a chat message through the warm Claude CLI pool. Returns the full response.",
				InputSchema: jsonSchema(map[string]interface{}{
					"model": stringProp("Model name or alias (e.g. 'haiku', 'sonnet', 'opus', 'claude-sonnet-4-6')"),
					"messages": map[string]interface{}{
						"type":        "array",
						"description": "OpenAI-format messages array [{role, content}, ...]",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"role":    map[string]interface{}{"type": "string"},
								"content": map[string]interface{}{"type": "string"},
							},
							"required": []string{"role", "content"},
						},
					},
				}, []string{"model", "messages"}),
			},
			poolChatHandler(svc),
		)
	}
}

// poolChatHandler creates the handler for the pool_chat tool.
func poolChatHandler(svc *Services) ToolHandler {
	return func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		if svc.pool == nil {
			return nil, fmt.Errorf("pool is not enabled")
		}

		modelName := getStringArg(args, "model", "")
		if modelName == "" {
			return nil, fmt.Errorf("model is required")
		}

		model, ok := resolveModel(modelName)
		if !ok {
			return nil, fmt.Errorf("unknown model: %s", modelName)
		}

		// Parse messages from args
		messagesRaw, ok := args["messages"]
		if !ok {
			return nil, fmt.Errorf("messages is required")
		}

		// Re-marshal and unmarshal to get typed messages
		messagesJSON, err := json.Marshal(messagesRaw)
		if err != nil {
			return nil, fmt.Errorf("invalid messages: %w", err)
		}
		var messages []oaiMessage
		if err := json.Unmarshal(messagesJSON, &messages); err != nil {
			return nil, fmt.Errorf("invalid messages format: %w", err)
		}

		if len(messages) == 0 {
			return nil, fmt.Errorf("messages array is empty")
		}

		// Build prompt from messages
		prompt := messagesToPrompt(messages)

		// Acquire pool slot
		slot, err := svc.pool.Acquire(ctx, model)
		if err != nil {
			return nil, fmt.Errorf("pool exhausted for model %s: %w", model, err)
		}
		defer svc.pool.Release(slot)

		// Send query
		if err := slot.sendQuery(prompt); err != nil {
			slot.mu.Lock()
			slot.state = slotDead
			slot.mu.Unlock()
			return nil, fmt.Errorf("failed to send query: %w", err)
		}

		// Collect full response
		var fullText strings.Builder
		var resultEv poolEvent

		for {
			ev, err := readEventWithCtx(ctx, slot)
			if err != nil {
				slot.mu.Lock()
				slot.state = slotDead
				slot.mu.Unlock()
				return nil, fmt.Errorf("failed reading response: %w", err)
			}

			switch ev.Type {
			case "assistant":
				fullText.WriteString(extractAssistantText(ev))
			case "stream_event":
				fullText.WriteString(extractStreamText(ev))
			case "rate_limit_event":
				svc.pool.handleRateLimit(slot, ev)
			case "result":
				resultEv = ev
				goto done
			}
		}

	done:
		slot.mu.Lock()
		slot.errorCount = 0
		slot.totalCost += resultEv.CostUSD
		slot.totalRequests++
		slot.mu.Unlock()
		svc.pool.totalCost.Add(int64(resultEv.CostUSD * 1e6))

		if resultEv.IsError {
			action := classifyResultError(resultEv)
			if action == "disable" || action == "recycle" {
				slot.mu.Lock()
				slot.state = slotDead
				slot.mu.Unlock()
			}
			return nil, fmt.Errorf("model error: %s", resultEv.Result)
		}

		text := fullText.String()
		if text == "" {
			text = resultEv.Result
		}

		tokensIn, tokensOut := 0, 0
		if resultEv.Usage != nil {
			tokensIn = resultEv.Usage.InputTokens
			tokensOut = resultEv.Usage.OutputTokens
		}

		return map[string]interface{}{
			"response":         text,
			"model":            model,
			"tokens_in":        tokensIn,
			"tokens_out":       tokensOut,
			"cost_usd":         resultEv.CostUSD,
			"duration_ms":      resultEv.DurationMS,
			"duration_api_ms":  resultEv.DurationAPIMS,
		}, nil
	}
}

