package main

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
)

// Services provides business logic shared by REST API and MCP handlers.
// Dependencies are injected via struct fields — no package-level globals.
type Services struct {
	db     *sql.DB
	pool   *PoolManager
	config *configService
}

// ─── Dashboard ──────────────────────────────────────────────────────────────

func (s *Services) Dashboard(ctx context.Context) (DashboardStats, error) {
	sessions, _ := listSessions(ctx)
	running := 0
	for _, sess := range sessions {
		if sess.Status == "running" {
			running++
		}
	}
	return DashboardStats{
		ActiveSessions:           running,
		Agents:                   0,
		Projects:                 0,
		TaskExecutions:           0,
		GitChangesAwaitingReview: []interface{}{},
		AgentsWaitingForInput:    []interface{}{},
		RemotePorts:              []interface{}{},
		DirectoryDevServers:      []interface{}{},
	}, nil
}

// ─── Agents (sessions exposed as agents for MCP compat) ─────────────────────

type AgentsResponse struct {
	Configured []interface{} `json:"configured"`
	Detected   struct {
		Agents []DetectedAgent `json:"agents"`
	} `json:"detected"`
}

type DetectedAgent struct {
	Available bool   `json:"available"`
	Command   string `json:"command"`
	Name      string `json:"name"`
	Path      string `json:"path"`
}

func (s *Services) ListAgents(ctx context.Context) (AgentsResponse, error) {
	claudePath, _ := exec.LookPath("claude")
	resp := AgentsResponse{
		Configured: []interface{}{},
	}
	resp.Detected.Agents = []DetectedAgent{{
		Available: claudePath != "",
		Command:   "claude",
		Name:      "claude",
		Path:      claudePath,
	}}
	return resp, nil
}

// ─── Executions (sessions) ──────────────────────────────────────────────────

func (s *Services) ListExecutions(ctx context.Context) ([]Session, error) {
	refreshSessionStatuses(ctx)
	sessions, err := listSessions(ctx)
	if err != nil {
		return nil, err
	}
	if sessions == nil {
		sessions = []Session{}
	}
	return sessions, nil
}

func (s *Services) GetExecution(ctx context.Context, id string) (*Session, error) {
	return getSession(ctx, id)
}

func (s *Services) RunTask(ctx context.Context, name, directory string, mission *string) (*Session, error) {
	if name == "" {
		name = "session-" + generateID()
	}
	if directory == "" {
		directory = "."
	}
	return spawnSession(ctx, name, directory, nil, nil, mission)
}

func (s *Services) UpdateSessionMission(ctx context.Context, id, mission string) error {
	return updateSessionMission(ctx, id, mission)
}

func (s *Services) SendInput(ctx context.Context, sessionID, input string) error {
	sess, err := getSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	return injectToSession(sess.TmuxSession, input)
}

func (s *Services) DeleteExecution(ctx context.Context, id string) error {
	return deleteSession(ctx, id)
}

// ─── Tmux Sessions ──────────────────────────────────────────────────────────

func (s *Services) TmuxSessions() ([]TmuxSessionInfo, error) {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}\t#{session_created_string}").Output()
	if err != nil {
		return []TmuxSessionInfo{}, nil
	}

	var sessions []TmuxSessionInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		created := ""
		if len(parts) > 1 {
			created = parts[1]
		}
		sessions = append(sessions, TmuxSessionInfo{
			Name:    parts[0],
			Created: created,
		})
	}
	return sessions, nil
}

// ─── Git ────────────────────────────────────────────────────────────────────

type GitStatus struct {
	Branch  string `json:"branch"`
	Output  string `json:"output"`
	IsDirty bool   `json:"is_dirty"`
}

func (s *Services) GitStatus(dir string) (GitStatus, error) {
	if dir == "" {
		dir = "."
	}
	out, err := runGitCommand(dir, "status", "--porcelain=v1")
	if err != nil {
		return GitStatus{}, fmt.Errorf("git status: %w", err)
	}
	branch, _ := runGitCommand(dir, "rev-parse", "--abbrev-ref", "HEAD")
	return GitStatus{
		Branch:  strings.TrimSpace(branch),
		Output:  out,
		IsDirty: strings.TrimSpace(out) != "",
	}, nil
}

func (s *Services) GitDiff(dir string, staged bool) (string, error) {
	if dir == "" {
		dir = "."
	}
	args := []string{"diff"}
	if staged {
		args = append(args, "--staged")
	}
	out, err := runGitCommand(dir, args...)
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return out, nil
}

func (s *Services) GitBranches(dir string, includeRemote bool) ([]string, error) {
	if dir == "" {
		dir = "."
	}
	args := []string{"branch", "--format=%(refname:short)"}
	if includeRemote {
		args = append(args, "-a")
	}
	out, err := runGitCommand(dir, args...)
	if err != nil {
		return nil, fmt.Errorf("git branch: %w", err)
	}
	branches := strings.Split(strings.TrimSpace(out), "\n")
	if len(branches) == 1 && branches[0] == "" {
		return []string{}, nil
	}
	return branches, nil
}

func (s *Services) GitLog(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	out, err := runGitCommand(dir, "log", "--oneline", "-20")
	if err != nil {
		return "", fmt.Errorf("git log: %w", err)
	}
	return out, nil
}

func (s *Services) GitAdd(dir string, files []string) error {
	if dir == "" {
		dir = "."
	}
	args := append([]string{"add"}, files...)
	_, err := runGitCommand(dir, args...)
	if err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	return nil
}

func (s *Services) GitCommit(dir, message string) (string, error) {
	if dir == "" {
		dir = "."
	}
	out, err := runGitCommand(dir, "commit", "-m", message)
	if err != nil {
		return out, fmt.Errorf("git commit: %w", err)
	}
	return out, nil
}

func (s *Services) GitPush(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	out, err := runGitCommand(dir, "push")
	if err != nil {
		return out, fmt.Errorf("git push: %w", err)
	}
	return out, nil
}

// ─── Roots ──────────────────────────────────────────────────────────────────

func (s *Services) ListRoots(ctx context.Context) ([]map[string]string, error) {
	sessions, _ := listSessions(ctx)
	seen := make(map[string]bool)
	var roots []map[string]string
	for _, sess := range sessions {
		if !seen[sess.Directory] {
			seen[sess.Directory] = true
			roots = append(roots, map[string]string{"path": sess.Directory})
		}
	}
	if roots == nil {
		roots = []map[string]string{}
	}
	return roots, nil
}

// ─── Swarm Dashboard ──────────────────────────────────────���─────────────────

type SwarmDashboard struct {
	Sessions []Session              `json:"sessions"`
	Running  int                    `json:"running"`
	Total    int                    `json:"total"`
	Pool     map[string]interface{} `json:"pool"`
}

func (s *Services) SwarmDashboard(ctx context.Context) (SwarmDashboard, error) {
	sessions, _ := listSessions(ctx)
	if sessions == nil {
		sessions = []Session{}
	}
	running := 0
	for _, sess := range sessions {
		if sess.Status == "running" && !sess.Hidden {
			running++
		}
	}

	poolInfo := map[string]interface{}{"enabled": false}
	if s.pool != nil {
		poolInfo = s.pool.Status()
		poolInfo["enabled"] = true
	}

	return SwarmDashboard{
		Sessions: sessions,
		Running:  running,
		Total:    len(sessions),
		Pool:     poolInfo,
	}, nil
}

// ─── Pool ───────────────────────────────────────────────────────────────────

func (s *Services) PoolStatus() map[string]interface{} {
	if s.pool == nil {
		return map[string]interface{}{"enabled": false}
	}
	return s.pool.Status()
}

// ─── Execution Progress ─────────────────────────────────────────────────────

type ExecutionProgress struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Terminal string `json:"terminal,omitempty"`
}

func (s *Services) ExecutionProgress(ctx context.Context, id string) (ExecutionProgress, error) {
	sess, err := getSession(ctx, id)
	if err != nil {
		return ExecutionProgress{}, fmt.Errorf("session not found: %s", id)
	}
	terminal := ""
	if sess.Status == "running" {
		terminal, _ = captureTerminal(sess.TmuxSession)
	}
	return ExecutionProgress{
		ID:       sess.ID,
		Name:     sess.Name,
		Status:   sess.Status,
		Terminal: terminal,
	}, nil
}
