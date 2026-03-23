package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ─── Dispatch suggestion via LLM ──────────────────────────────────────────────
//
// POST /api/swarm/suggest-dispatch
// Given an Icinga alert + list of available sessions, asks claude-haiku to pick
// the most appropriate session and agent role to investigate the alert.
//
// LLM calls are routed through the warm dispatch worker (swarm_dispatch_worker.go)
// which keeps a persistent `claude --output-format stream-json` process alive
// between calls and clears its context after each use.

type dispatchSuggestReq struct {
	Alert    dispatchAlert     `json:"alert"`
	Sessions []dispatchSession `json:"sessions"`
}

type dispatchAlert struct {
	Host       string `json:"host"`
	Service    string `json:"service"`
	State      int    `json:"state"`
	StateLabel string `json:"state_label"`
	Output     string `json:"output"`
}

type dispatchSession struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContextName string `json:"context_name"`
}

type DispatchSuggestResp struct {
	SessionID string `json:"session_id"`
	Role      string `json:"role"`
	Mission   string `json:"mission"`
	Error     string `json:"error,omitempty"`
}

func handleDispatchSuggestAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	var req dispatchSuggestReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(DispatchSuggestResp{Error: "bad request: " + err.Error()}) //nolint:errcheck
		return
	}
	if req.Alert.Host == "" || req.Alert.Service == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(DispatchSuggestResp{Error: "alert.host and alert.service required"}) //nolint:errcheck
		return
	}

	roles := loadDispatchRoles(r.Context())

	suggestion, err := llmSuggestDispatch(r.Context(), req, roles)
	if err != nil {
		suggestion = dispatchFallback(req)
		suggestion.Error = err.Error()
	}

	json.NewEncoder(w).Encode(suggestion) //nolint:errcheck
}

// loadDispatchRoles returns the set of role names defined in swarm_role_prompts,
// falling back to a hardcoded default list if the table is empty or inaccessible.
func loadDispatchRoles(ctx context.Context) []string {
	rows, err := database.QueryContext(ctx, `SELECT role FROM swarm_role_prompts ORDER BY role`)
	if err != nil {
		return dispatchDefaultRoles()
	}
	defer rows.Close()
	var roles []string
	for rows.Next() {
		var role string
		if rows.Scan(&role) == nil {
			roles = append(roles, role)
		}
	}
	if len(roles) == 0 {
		return dispatchDefaultRoles()
	}
	return roles
}

func dispatchDefaultRoles() []string {
	return []string{
		"homelab-agent",
		"mcp-developer",
		"devops-agent",
		"home-automation-agent",
		"worker",
	}
}

// llmSuggestDispatch routes an Icinga alert to the warm dispatch worker.
func llmSuggestDispatch(ctx context.Context, req dispatchSuggestReq, roles []string) (DispatchSuggestResp, error) {
	var sessLines []string
	for _, s := range req.Sessions {
		ctxName := s.ContextName
		if ctxName == "" {
			ctxName = "(no context)"
		}
		sessLines = append(sessLines, fmt.Sprintf("  - id=%q  name=%q  context=%q", s.ID, s.Name, ctxName))
	}

	prompt := fmt.Sprintf(`You are routing an infrastructure alert to the right AI agent session.

## Alert
Host: %s
Service: %s
State: %s
Output: %s

## Available Sessions
%s

## Available Roles
%s

## Role Guide (use this to pick the right role)
- homelab-agent: Komodo stacks, Unraid, Docker Compose, general Linux services, network issues
- mcp-developer: TKNet MCP server failures, health check failures on mcp-* services, TypeScript/Bun issues
- devops-agent: CI/CD failures, build errors, deployment issues, GitHub Actions
- home-automation-agent: Home Assistant, Zigbee, automations, sensor failures, Pi hardware
- worker: general purpose, use only if no specific role fits

## Instructions
Pick the session and role that best match this alert. Choose the session whose name/context is most relevant to the affected service.

Respond with ONLY a JSON object, no markdown, no explanation:
{"session_id": "<one of the session IDs above>", "role": "<one of the roles above>", "mission": "<one concise sentence describing what the agent should investigate and resolve>"}`,
		req.Alert.Host,
		req.Alert.Service,
		req.Alert.StateLabel,
		truncStr(req.Alert.Output, 200),
		strings.Join(sessLines, "\n"),
		strings.Join(roles, ", "),
	)

	text, err := warmDispatchQuery(ctx, prompt)
	if err != nil {
		return DispatchSuggestResp{}, err
	}

	// Strip any accidental markdown fences.
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	// Find the first '{' in case there's leading prose.
	if idx := strings.Index(text, "{"); idx > 0 {
		text = text[idx:]
	}

	var suggestion DispatchSuggestResp
	if err := json.Unmarshal([]byte(text), &suggestion); err != nil {
		return DispatchSuggestResp{}, fmt.Errorf("parse suggestion JSON %q: %w", truncStr(text, 80), err)
	}

	// Validate session_id is one we actually provided.
	validSession := false
	for _, s := range req.Sessions {
		if s.ID == suggestion.SessionID {
			validSession = true
			break
		}
	}
	if !validSession && len(req.Sessions) > 0 {
		suggestion.SessionID = req.Sessions[0].ID
	}

	return suggestion, nil
}

// dispatchFallback returns a keyword-based best-guess when the LLM is unavailable.
func dispatchFallback(req dispatchSuggestReq) DispatchSuggestResp {
	svcLow := strings.ToLower(req.Alert.Service + " " + req.Alert.Output)
	role := "homelab-agent"
	switch {
	case strings.Contains(svcLow, "mcp") || strings.Contains(svcLow, "tkn-"):
		role = "mcp-developer"
	case strings.Contains(svcLow, "home assistant") || strings.Contains(svcLow, "zigbee") || strings.Contains(svcLow, "homeassistant"):
		role = "home-automation-agent"
	case strings.Contains(svcLow, "deploy") || strings.Contains(svcLow, "ci") || strings.Contains(svcLow, "build"):
		role = "devops-agent"
	}

	stateLabel := map[int]string{0: "OK", 1: "WARNING", 2: "CRITICAL", 3: "UNKNOWN"}
	state := stateLabel[req.Alert.State]
	mission := fmt.Sprintf("Investigate and resolve Icinga alert: %s / %s is %s — %s",
		req.Alert.Host, req.Alert.Service, state, truncStr(req.Alert.Output, 120))

	sid := ""
	if len(req.Sessions) > 0 {
		sid = req.Sessions[0].ID
	}

	return DispatchSuggestResp{SessionID: sid, Role: role, Mission: mission}
}
