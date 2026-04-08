package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ─── Step runner ────────────────────────────────────────────────────────────

// step represents one message + assertion in a multi-step test.
type step struct {
	name   string  // description for error messages
	msg    tea.Msg // message to send through Update
	assert func(t *testing.T, m tuiModel)
}

// runSteps applies a sequence of messages to the model, asserting after each.
// Returns the final model state.
func runSteps(t *testing.T, m tuiModel, steps []step) tuiModel {
	t.Helper()
	for i, s := range steps {
		label := s.name
		if label == "" {
			label = fmt.Sprintf("step %d", i)
		}
		updated, _ := m.Update(s.msg)
		m = updated.(tuiModel)
		if s.assert != nil {
			t.Run(label, func(t *testing.T) {
				t.Helper()
				s.assert(t, m)
			})
		}
	}
	return m
}

// keyMsg constructs a tea.KeyMsg for common key types.
func keyMsg(key string) tea.KeyMsg {
	switch key {
	case "alt+a":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a"), Alt: true}
	case "alt+z":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z"), Alt: true}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	default:
		if strings.HasPrefix(key, "alt+") {
			return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune(key[4])}, Alt: true}
		}
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}

// ─── Multi-step async flow tests ────────────────────────────────────────────

func TestSteps_PlanePopupFullFlow(t *testing.T) {
	items := []sidebarItem{fakeSessionItem("dev", "running")}
	m := newTestModel(items)

	m = runSteps(t, m, []step{
		{
			name: "open plane popup",
			msg:  keyMsg("alt+p"),
			assert: func(t *testing.T, m tuiModel) {
				if m.mode != modePlaneIssues {
					t.Errorf("mode should be modePlaneIssues, got %d", m.mode)
				}
				if m.planeIssues != nil {
					t.Error("issues should be nil (loading)")
				}
				if m.planeReqID != 1 {
					t.Errorf("planeReqID should be 1, got %d", m.planeReqID)
				}
			},
		},
		{
			name: "receive plane issues",
			msg:  planeIssuesMsg{reqID: 1, issues: fakePlaneIssues()},
			assert: func(t *testing.T, m tuiModel) {
				if len(m.planeIssues) != 4 {
					t.Errorf("should have 4 issues, got %d", len(m.planeIssues))
				}
				if m.popupCursor != 0 {
					t.Errorf("cursor should be 0, got %d", m.popupCursor)
				}
			},
		},
		{
			name: "navigate down",
			msg:  keyMsg("alt+z"),
			assert: func(t *testing.T, m tuiModel) {
				if m.popupCursor != 1 {
					t.Errorf("cursor should be 1, got %d", m.popupCursor)
				}
			},
		},
		{
			name: "press enter to open action picker",
			msg:  keyMsg("enter"),
			assert: func(t *testing.T, m tuiModel) {
				if m.mode != modePopupAction {
					t.Errorf("should be in action picker, got %d", m.mode)
				}
				if m.actionPrevMode != modePlaneIssues {
					t.Errorf("prev mode should be plane, got %d", m.actionPrevMode)
				}
				if len(m.actionSessions) != 1 {
					t.Errorf("should have 1 running session, got %d", len(m.actionSessions))
				}
			},
		},
		{
			name: "escape back to plane popup",
			msg:  keyMsg("esc"),
			assert: func(t *testing.T, m tuiModel) {
				if m.mode != modePlaneIssues {
					t.Errorf("should return to plane popup, got %d", m.mode)
				}
			},
		},
		{
			name: "close popup",
			msg:  keyMsg("esc"),
			assert: func(t *testing.T, m tuiModel) {
				if m.mode != modePassthrough {
					t.Errorf("should return to passthrough, got %d", m.mode)
				}
			},
		},
	})
}

func TestSteps_IcingaFilterSortFlow(t *testing.T) {
	m := newTestModel(nil)

	m = runSteps(t, m, []step{
		{
			name: "open icinga popup",
			msg:  keyMsg("alt+i"),
			assert: func(t *testing.T, m tuiModel) {
				if m.mode != modeIcingaAlerts {
					t.Errorf("mode should be icinga, got %d", m.mode)
				}
			},
		},
		{
			name: "receive problems",
			msg:  icingaProblemsMsg{reqID: 1, problems: fakeIcingaProblems()},
			assert: func(t *testing.T, m tuiModel) {
				if len(m.icingaProblems) != 2 {
					t.Errorf("should have 2 problems, got %d", len(m.icingaProblems))
				}
			},
		},
		{
			name: "cycle sort to severity",
			msg:  keyMsg("s"),
			assert: func(t *testing.T, m tuiModel) {
				if m.popupSortMode != 1 {
					t.Errorf("sort mode should be 1, got %d", m.popupSortMode)
				}
				if m.popupCursor != 0 {
					t.Error("cursor should reset on sort")
				}
			},
		},
		{
			name: "activate filter",
			msg:  keyMsg("/"),
			assert: func(t *testing.T, m tuiModel) {
				if !m.popupFilterActive {
					t.Error("filter should be active")
				}
			},
		},
		{
			name: "cancel filter with esc",
			msg:  keyMsg("esc"),
			assert: func(t *testing.T, m tuiModel) {
				if m.popupFilterActive {
					t.Error("filter should be inactive after esc")
				}
				if m.mode != modeIcingaAlerts {
					t.Errorf("should still be in icinga mode, got %d", m.mode)
				}
			},
		},
	})
}

func TestSteps_StaleResponseIgnored(t *testing.T) {
	m := newTestModel(nil)

	m = runSteps(t, m, []step{
		{
			name: "open plane popup (reqID=1)",
			msg:  keyMsg("alt+p"),
			assert: func(t *testing.T, m tuiModel) {
				if m.planeReqID != 1 {
					t.Errorf("reqID should be 1, got %d", m.planeReqID)
				}
			},
		},
		{
			name: "refresh (reqID bumps to 2)",
			msg:  keyMsg("r"),
			assert: func(t *testing.T, m tuiModel) {
				if m.planeReqID != 2 {
					t.Errorf("reqID should be 2, got %d", m.planeReqID)
				}
				if m.planeIssues != nil {
					t.Error("issues should be nil after refresh")
				}
			},
		},
		{
			name: "stale response (reqID=1) arrives — should be ignored",
			msg:  planeIssuesMsg{reqID: 1, issues: fakePlaneIssues()},
			assert: func(t *testing.T, m tuiModel) {
				if m.planeIssues != nil {
					t.Error("stale response should be ignored, issues should still be nil")
				}
			},
		},
		{
			name: "fresh response (reqID=2) arrives — should apply",
			msg:  planeIssuesMsg{reqID: 2, issues: fakePlaneIssues()[:2]},
			assert: func(t *testing.T, m tuiModel) {
				if len(m.planeIssues) != 2 {
					t.Errorf("fresh response should apply, got %d issues", len(m.planeIssues))
				}
			},
		},
	})
}

func TestSteps_ClosedPopupIgnoresResponse(t *testing.T) {
	m := newTestModel(nil)

	m = runSteps(t, m, []step{
		{
			name: "open icinga popup",
			msg:  keyMsg("alt+i"),
			assert: func(t *testing.T, m tuiModel) {
				if m.mode != modeIcingaAlerts {
					t.Errorf("should be icinga, got %d", m.mode)
				}
			},
		},
		{
			name: "close popup before response arrives",
			msg:  keyMsg("esc"),
			assert: func(t *testing.T, m tuiModel) {
				if m.mode != modePassthrough {
					t.Errorf("should be passthrough, got %d", m.mode)
				}
			},
		},
		{
			name: "late response arrives — should be ignored (wrong mode)",
			msg:  icingaProblemsMsg{reqID: 1, problems: fakeIcingaProblems()},
			assert: func(t *testing.T, m tuiModel) {
				if m.icingaProblems != nil {
					t.Error("response after close should be ignored")
				}
			},
		},
	})
}

func TestSteps_StaleErrorIgnored(t *testing.T) {
	m := newTestModel(nil)

	m = runSteps(t, m, []step{
		{
			name: "open plane popup",
			msg:  keyMsg("alt+p"),
		},
		{
			name: "refresh bumps reqID",
			msg:  keyMsg("r"),
		},
		{
			name: "stale error (reqID=1) arrives",
			msg:  popupErrMsg{reqID: 1, source: "plane", text: "stale error"},
			assert: func(t *testing.T, m tuiModel) {
				if m.popupErr != "" {
					t.Errorf("stale error should be ignored, got %q", m.popupErr)
				}
			},
		},
		{
			name: "fresh error (reqID=2) arrives",
			msg:  popupErrMsg{reqID: 2, source: "plane", text: "real error"},
			assert: func(t *testing.T, m tuiModel) {
				if m.popupErr != "real error" {
					t.Errorf("fresh error should apply, got %q", m.popupErr)
				}
			},
		},
	})
}
