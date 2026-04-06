package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ─── Test data helpers ──────────────────────────────────────────────────────

func fakePlaneIssues() []planeIssue {
	return []planeIssue{
		{ID: "p1", Title: "Fix auth middleware", Priority: "urgent", SequenceID: 101, StateGroup: "started"},
		{ID: "p2", Title: "Add API rate limiting", Priority: "high", SequenceID: 102, StateGroup: "backlog"},
		{ID: "p3", Title: "Update documentation", Priority: "medium", SequenceID: 103, StateGroup: "unstarted"},
		{ID: "p4", Title: "Refactor models", Priority: "none", SequenceID: 104, StateGroup: "backlog"},
	}
}

func fakeIcingaProblems() []icingaProblem {
	return []icingaProblem{
		{Host: "backup-fire", Service: "restic-backup", State: 2, Output: "Backup failed: exit code 1"},
		{Host: "unraid", Service: "disk-temp", State: 1, Output: "Disk 3 temperature 52C"},
	}
}

// ─── View contract tests: Plane popup ───────────────────────────────────────

func TestView_PlaneIssues(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()
	view := viewStripped(m)

	assertContains(t, view, "Plane Issues")
	assertContains(t, view, "Fix auth middleware")
	assertContains(t, view, "Add API rate limiting")
	assertContains(t, view, "Update documentation")
	assertContains(t, view, "Refactor models")
	assertContains(t, view, "started")
	assertContains(t, view, "backlog")
	assertContains(t, view, "unstarted")
	assertContains(t, view, "close")
	assertGolden(t, "view_plane", view)
}

func TestView_PlaneEmpty(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = []planeIssue{}
	view := viewStripped(m)

	assertContains(t, view, "Plane Issues")
	assertContains(t, view, "No issues found")
}

func TestView_PlaneLoading(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = nil // nil = loading
	view := viewStripped(m)

	assertContains(t, view, "Loading")
}

func TestView_PlaneError(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.popupErr = "Plane not configured"
	view := viewStripped(m)

	assertContains(t, view, "Error")
	assertContains(t, view, "Plane not configured")
}

// ─── View contract tests: Icinga popup ──────────────────────────────────────

func TestView_IcingaAlerts(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeIcingaAlerts
	m.icingaProblems = fakeIcingaProblems()
	view := viewStripped(m)

	assertContains(t, view, "Icinga Alerts")
	assertContains(t, view, "CRITICAL")
	assertContains(t, view, "backup-fire")
	assertContains(t, view, "restic-backup")
	assertContains(t, view, "Backup failed")
	assertContains(t, view, "WARNING")
	assertContains(t, view, "unraid")
	assertContains(t, view, "disk-temp")
	assertContains(t, view, "close")
	assertGolden(t, "view_icinga", view)
}

func TestView_IcingaEmpty(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeIcingaAlerts
	m.icingaProblems = []icingaProblem{}
	view := viewStripped(m)

	assertContains(t, view, "All clear")
}

func TestView_IcingaLoading(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeIcingaAlerts
	m.icingaProblems = nil
	view := viewStripped(m)

	assertContains(t, view, "Loading")
}

func TestView_IcingaError(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeIcingaAlerts
	m.popupErr = "Icinga not configured"
	view := viewStripped(m)

	assertContains(t, view, "Error")
	assertContains(t, view, "Icinga not configured")
}

// ─── Key handling: opening popups ───────────────────────────────────────────

func TestKey_OpenPlane(t *testing.T) {
	m := newTestModel(nil)

	// alt+p should switch to plane mode
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}, Alt: true})
	m = updated.(tuiModel)

	if m.mode != modePlaneIssues {
		t.Errorf("alt+p should enter modePlaneIssues, got %d", m.mode)
	}
	if cmd == nil {
		t.Error("should return a fetch command")
	}
	if m.planeIssues != nil {
		t.Error("planeIssues should be nil (loading)")
	}
}

func TestKey_OpenIcinga(t *testing.T) {
	m := newTestModel(nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}, Alt: true})
	m = updated.(tuiModel)

	if m.mode != modeIcingaAlerts {
		t.Errorf("alt+i should enter modeIcingaAlerts, got %d", m.mode)
	}
	if cmd == nil {
		t.Error("should return a fetch command")
	}
	if m.icingaProblems != nil {
		t.Error("icingaProblems should be nil (loading)")
	}
}

// ─── Key handling: closing popups ───────────────────────────────────────────

func TestKey_ClosePlaneWithEsc(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()

	m = sendSpecialKey(m, "esc")
	if m.mode != modePassthrough {
		t.Errorf("esc should return to passthrough, got %d", m.mode)
	}
}

func TestKey_ClosePlaneWithQ(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()

	m = sendKey(m, "q")
	if m.mode != modePassthrough {
		t.Errorf("q should return to passthrough, got %d", m.mode)
	}
}

func TestKey_CloseIcingaWithEsc(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeIcingaAlerts
	m.icingaProblems = fakeIcingaProblems()

	m = sendSpecialKey(m, "esc")
	if m.mode != modePassthrough {
		t.Errorf("esc should return to passthrough, got %d", m.mode)
	}
}

// ─── Key handling: popup cursor navigation ──────────────────────────────────

func TestKey_PlaneCursorNav(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()
	m.popupCursor = 0

	// Move down
	m = sendSpecialKey(m, "ctrl+z")
	if m.popupCursor != 1 {
		t.Errorf("cursor should be 1, got %d", m.popupCursor)
	}

	m = sendSpecialKey(m, "ctrl+z")
	m = sendSpecialKey(m, "ctrl+z")
	if m.popupCursor != 3 {
		t.Errorf("cursor should be 3, got %d", m.popupCursor)
	}

	// Clamp at bottom
	m = sendSpecialKey(m, "ctrl+z")
	if m.popupCursor != 3 {
		t.Errorf("cursor should clamp at 3, got %d", m.popupCursor)
	}

	// Move back up
	m = sendSpecialKey(m, "ctrl+a")
	if m.popupCursor != 2 {
		t.Errorf("cursor should be 2, got %d", m.popupCursor)
	}

	// Clamp at top
	m.popupCursor = 0
	m = sendSpecialKey(m, "ctrl+a")
	if m.popupCursor != 0 {
		t.Errorf("cursor should clamp at 0, got %d", m.popupCursor)
	}
}

func TestKey_IcingaCursorNav(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeIcingaAlerts
	m.icingaProblems = fakeIcingaProblems()
	m.popupCursor = 0

	m = sendSpecialKey(m, "ctrl+z")
	if m.popupCursor != 1 {
		t.Errorf("cursor should be 1, got %d", m.popupCursor)
	}

	// Clamp at bottom (2 items, max index 1)
	m = sendSpecialKey(m, "ctrl+z")
	if m.popupCursor != 1 {
		t.Errorf("cursor should clamp at 1, got %d", m.popupCursor)
	}
}

// ─── Key handling: refresh ──────────────────────────────────────────────────

func TestKey_PlaneRefresh(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = updated.(tuiModel)

	if m.planeIssues != nil {
		t.Error("refresh should clear planeIssues to nil (loading)")
	}
	if cmd == nil {
		t.Error("refresh should return a fetch command")
	}
}

func TestKey_IcingaRefresh(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeIcingaAlerts
	m.icingaProblems = fakeIcingaProblems()

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = updated.(tuiModel)

	if m.icingaProblems != nil {
		t.Error("refresh should clear icingaProblems to nil (loading)")
	}
	if cmd == nil {
		t.Error("refresh should return a fetch command")
	}
}

// ─── Message handling tests ─────────────────────────────────────────────────

func TestPlaneIssuesMsg(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues

	issues := fakePlaneIssues()
	updated, _ := m.Update(planeIssuesMsg(issues))
	m = updated.(tuiModel)

	if len(m.planeIssues) != 4 {
		t.Errorf("expected 4 issues, got %d", len(m.planeIssues))
	}
	if m.popupErr != "" {
		t.Errorf("error should be cleared, got %q", m.popupErr)
	}
	if m.popupCursor != 0 {
		t.Errorf("cursor should reset to 0, got %d", m.popupCursor)
	}
}

func TestIcingaProblemsMsg(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeIcingaAlerts

	problems := fakeIcingaProblems()
	updated, _ := m.Update(icingaProblemsMsg(problems))
	m = updated.(tuiModel)

	if len(m.icingaProblems) != 2 {
		t.Errorf("expected 2 problems, got %d", len(m.icingaProblems))
	}
	if m.popupErr != "" {
		t.Errorf("error should be cleared, got %q", m.popupErr)
	}
}

func TestPopupErrMsg(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues

	updated, _ := m.Update(popupErrMsg{source: "plane", text: "connection refused"})
	m = updated.(tuiModel)

	if m.popupErr != "connection refused" {
		t.Errorf("popupErr should be set, got %q", m.popupErr)
	}
}

// ─── Status bar shows popup keybindings ─────────────────────────────────────

func TestStatusBar_ShowsPopupKeys(t *testing.T) {
	m := newTestModel(nil)
	view := viewStripped(m)

	assertContains(t, view, "Alt+P plane")
	assertContains(t, view, "Alt+I icinga")
}

// ─── Popup doesn't show sidebar/content ─────────────────────────────────────

func TestView_PlaneReplacesMainView(t *testing.T) {
	items := []sidebarItem{fakeSessionItem("my-session", "running")}
	m := newTestModel(items)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()
	view := viewStripped(m)

	// Should NOT show the main sidebar
	assertNotContains(t, view, "SwarmOps")
	assertNotContains(t, view, "my-session")
	// Should show popup
	assertContains(t, view, "Plane Issues")
}

func TestView_IcingaReplacesMainView(t *testing.T) {
	items := []sidebarItem{fakeSessionItem("my-session", "running")}
	m := newTestModel(items)
	m.mode = modeIcingaAlerts
	m.icingaProblems = fakeIcingaProblems()
	view := viewStripped(m)

	assertNotContains(t, view, "SwarmOps")
	assertNotContains(t, view, "my-session")
	assertContains(t, view, "Icinga Alerts")
}

// ─── Rendering helpers ──────────────────────────────────────────────────────

func TestIcingaStateLabel(t *testing.T) {
	tests := []struct {
		state int
		want  string
	}{
		{1, "WARNING"},
		{2, "CRITICAL"},
		{3, "UNKNOWN"},
		{0, "OK"},
	}
	for _, tt := range tests {
		got := icingaStateLabel(tt.state)
		if !containsStr(got, tt.want) {
			t.Errorf("icingaStateLabel(%d) = %q, want to contain %q", tt.state, got, tt.want)
		}
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findStr(s, substr))
}

func findStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
