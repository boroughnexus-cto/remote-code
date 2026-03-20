package main

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ─── Layout ───────────────────────────────────────────────────────────────────

const (
	tuiSidebarW = 32 // left column width
	tuiInputH   = 3  // textarea row height
	tuiDetailH  = 11 // agent/session detail rows in right pane
)

// ─── Focus / modal kinds ──────────────────────────────────────────────────────

type tuiFocus int

const (
	tuiFocusSidebar tuiFocus = iota
	tuiFocusInput
	tuiFocusModal
)

// ─── Data types ───────────────────────────────────────────────────────────────

type tuiAgent struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Role         string  `json:"role"`
	Status       string  `json:"status"`
	Mission      *string `json:"mission"`
	Project      *string `json:"project,omitempty"`
	RepoPath     *string `json:"repo_path,omitempty"`
	TmuxSession  *string `json:"tmux_session"`
	CurrentTask  *string `json:"current_task_id"`
	CurrentFile  *string `json:"current_file"`
	LatestNote   *string `json:"latest_note"`
	ContextPct   float64 `json:"context_pct"`
	ContextState string  `json:"context_state"`
	ModelName       string `json:"model_name,omitempty"`
	TokensUsed      int64  `json:"tokens_used,omitempty"`
	StatusChangedAt int64  `json:"status_changed_at,omitempty"`
}

type tuiTask struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Description   *string  `json:"description,omitempty"`
	Project       *string  `json:"project,omitempty"`
	Stage         string   `json:"stage"`
	Phase         *string  `json:"phase,omitempty"`
	PhaseOrder    *int64   `json:"phase_order,omitempty"`
	GoalID        *string  `json:"goal_id,omitempty"`
	PRUrl         *string  `json:"pr_url,omitempty"`
	CIStatus      *string  `json:"ci_status,omitempty"`
	Confidence    *float64 `json:"confidence,omitempty"`
	TokensUsed    *int64   `json:"tokens_used,omitempty"`
	BlockedReason  *string `json:"blocked_reason,omitempty"`
	StageChangedAt int64   `json:"stage_changed_at,omitempty"`
}

type tuiSession struct {
	ID                    string  `json:"id"`
	Name                  string  `json:"name"`
	AutopilotEnabled      bool    `json:"autopilot_enabled"`
	AutopilotPlaneProject *string `json:"autopilot_plane_project_id,omitempty"`
}

type tuiEvent struct {
	AgentID string `json:"agent_id"`
	TaskID  string `json:"task_id"`
	Type    string `json:"type"`
	Payload string `json:"payload"`
	Ts      int64  `json:"ts"`
}

type tuiGoal struct {
	ID           string `json:"id"`
	Description  string `json:"description"`
	Status       string `json:"status"`
	Complexity   string `json:"complexity"`
	TokenBudget  int64  `json:"token_budget"`
	TokensUsed   int64  `json:"tokens_used"`
	JudgeNotes   string `json:"judge_notes"`
	CreatedAt    int64  `json:"created_at"`
}

type tuiEscalation struct {
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	TaskID  string `json:"task_id"`
	Reason  string `json:"reason"`
	Ts      int64  `json:"ts"`
}

type tuiState struct {
	Session     tuiSession      `json:"session"`
	Agents      []tuiAgent      `json:"agents"`
	Tasks       []tuiTask       `json:"tasks"`
	Events      []tuiEvent      `json:"events"`
	Goals       []tuiGoal       `json:"goals"`
	Escalations []tuiEscalation `json:"escalations"`
}

// ─── Messages ─────────────────────────────────────────────────────────────────

type tuiAnimTickMsg struct{}
type tuiRolePromptSavedMsg struct{ role string }
type tuiRolePromptEditMsg struct {
	role    string
	tmpPath string
	editor  string
}
type tuiTermMsg struct {
	agentID string
	content string
}
type tuiWSUpdateMsg struct {
	sid   string
	state tuiState
}
type tuiDataMsg struct {
	sessions []tuiSession
	states   map[string]tuiState
}
type agentNote struct {
	ID        int64  `json:"id"`
	Content   string `json:"content"`
	CreatedBy string `json:"created_by"`
	CreatedAt int64  `json:"created_at"`
}

type tuiErrMsg       struct{ op, text string }
type tuiDoneMsg      struct{ op string }
type tuiAttachMsg    struct{ err error }
type tuiWorkQueueMsg struct{ items []WorkQueueItem }
type tuiIcingaMsg    struct{ services []IcingaService }
type tuiHelpHideMsg  struct{ version int }
type tuiNotesMsg     struct {
	agentID string
	items   []agentNote
}

// tuiGitStatus holds lightweight git info for a single agent's working tree.
type tuiGitStatus struct {
	Branch  string `json:"branch"`
	Dirty   bool   `json:"dirty"`
	Ahead   int    `json:"ahead"`
	Subject string `json:"subject"`
}

type tuiGitStatusMsg struct {
	agentID string
	status  tuiGitStatus
}

func tuiAnimTick() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return tuiAnimTickMsg{} })
}

func hideHelpAfter(version int) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(700 * time.Millisecond)
		return tuiHelpHideMsg{version: version}
	}
}
