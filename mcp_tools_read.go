package main

import (
	"context"
	"fmt"
)

// registerReadTools registers all read-only MCP tools.
func registerReadTools(reg *ToolRegistry, svc *Services) {
	// ─── rc_health_check ────────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_health_check",
			Description: "[READ] Basic health check — returns ok if the server is running.",
			InputSchema: jsonSchema(map[string]interface{}{}, nil),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{"status": "ok", "version": "2.0"}, nil
		},
	)

	// ─── rc_health ──────────────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_health",
			Description: "[READ] Get dashboard stats (active sessions, projects, agents, git changes).",
			InputSchema: jsonSchema(map[string]interface{}{}, nil),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return svc.Dashboard(ctx)
		},
	)

	// ─── rc_dashboard ───────────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_dashboard",
			Description: "[READ] Get full swarm dashboard with session list, running count, and pool status.",
			InputSchema: jsonSchema(map[string]interface{}{}, nil),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return svc.SwarmDashboard(ctx)
		},
	)

	// ─── rc_list_agents ─────────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_list_agents",
			Description: "[READ] List configured agents and auto-detect installed agents on the system.",
			InputSchema: jsonSchema(map[string]interface{}{}, nil),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return svc.ListAgents(ctx)
		},
	)

	// ─── rc_list_roots ──────────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_list_roots",
			Description: "[READ] List unique working directories from all sessions.",
			InputSchema: jsonSchema(map[string]interface{}{}, nil),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return svc.ListRoots(ctx)
		},
	)

	// ─── rc_list_executions ─────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_list_executions",
			Description: "[READ] List all task executions with status (running, waiting, completed, failed).",
			InputSchema: jsonSchema(map[string]interface{}{}, nil),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return svc.ListExecutions(ctx)
		},
	)

	// ─── rc_get_execution ───────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_get_execution",
			Description: "[READ] Get a single task execution by ID.",
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

	// ─── rc_execution_progress ──────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_execution_progress",
			Description: "[READ] Get execution progress including terminal capture for running sessions.",
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

	// ─── rc_tmux_sessions ───────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_tmux_sessions",
			Description: "[READ] List all tmux sessions on the system.",
			InputSchema: jsonSchema(map[string]interface{}{}, nil),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return svc.TmuxSessions()
		},
	)

	// ─── rc_list_sessions ───────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_list_sessions",
			Description: "[READ] List all managed sessions with status, mission, and activity.",
			InputSchema: jsonSchema(map[string]interface{}{}, nil),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return svc.ListExecutions(ctx)
		},
	)

	// ─── rc_get_session ─────────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_get_session",
			Description: "[READ] Get a single session by ID with full details.",
			InputSchema: jsonSchema(map[string]interface{}{
				"id": stringProp("Session ID"),
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

	// ─── rc_get_terminal ────────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_get_terminal",
			Description: "[READ] Get the terminal scrollback for a session (last ~500 lines).",
			InputSchema: jsonSchema(map[string]interface{}{
				"id": stringProp("Session ID"),
			}, []string{"id"}),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			id := getStringArg(args, "id", "")
			if id == "" {
				return nil, fmt.Errorf("id is required")
			}
			content, err := svc.GetTerminal(ctx, id)
			if err != nil {
				return nil, err
			}
			return map[string]string{"content": content}, nil
		},
	)

	// ─── rc_list_audit_events ───────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_list_audit_events",
			Description: "[READ] List session lifecycle events (create, stop, delete, rename). Returns most recent first.",
			InputSchema: jsonSchema(map[string]interface{}{
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of events to return (default 100)",
				},
			}, nil),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			limit := 100
			if v, ok := args["limit"].(float64); ok && v > 0 {
				limit = int(v)
			}
			return svc.ListAuditEvents(ctx, limit)
		},
	)

	// ─── rc_git_status ──────────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_git_status",
			Description: "[READ] Get git repository status (branch, staged/unstaged files, merge conflicts).",
			InputSchema: jsonSchema(map[string]interface{}{
				"path": stringProp("Repository path (default: current directory)"),
			}, nil),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			path := getStringArg(args, "path", ".")
			return svc.GitStatus(path)
		},
	)

	// ─── rc_git_diff ────────────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_git_diff",
			Description: "[READ] Get git diff output for a repository.",
			InputSchema: jsonSchema(map[string]interface{}{
				"path":   stringProp("Repository path (default: current directory)"),
				"staged": boolProp("Show staged changes only (default: false)"),
			}, nil),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			path := getStringArg(args, "path", ".")
			staged := getBoolArg(args, "staged", false)
			diff, err := svc.GitDiff(path, staged)
			if err != nil {
				return nil, err
			}
			return map[string]string{"diff": diff}, nil
		},
	)

	// ─── rc_git_branches ────────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_git_branches",
			Description: "[READ] List git branches (local and optionally remote) for the repository.",
			InputSchema: jsonSchema(map[string]interface{}{
				"path":           stringProp("Repository path (default: current directory)"),
				"include_remote": boolProp("Include remote branches (default: false)"),
			}, nil),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			path := getStringArg(args, "path", ".")
			includeRemote := getBoolArg(args, "include_remote", false)
			branches, err := svc.GitBranches(path, includeRemote)
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"branches": branches}, nil
		},
	)

	// ─── rc_git_log ─────────────────────────────────────────────────────
	reg.Register(
		ToolDefinition{
			Name:        "rc_git_log",
			Description: "[READ] Get recent git log (last 20 commits, oneline format).",
			InputSchema: jsonSchema(map[string]interface{}{
				"path": stringProp("Repository path (default: current directory)"),
			}, nil),
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			path := getStringArg(args, "path", ".")
			gitLog, err := svc.GitLog(path)
			if err != nil {
				return nil, err
			}
			return map[string]string{"log": gitLog}, nil
		},
	)

	// ─── pool_status (gated) ────────────────────────────────────────────
	// Registered conditionally by registerWriteTools if pool tools enabled.
	// We handle it here to keep read tools together.

	// This is a no-op — pool_status is registered in registerWriteTools
	// to keep the gating logic in one place.

	_ = fmt.Sprintf // reference fmt
}
