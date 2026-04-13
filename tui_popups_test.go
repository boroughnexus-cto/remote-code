package main

import (
	"fmt"
	"strings"
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
		{Host: "backup-fire", Service: "restic-backup", State: 2, Output: "Backup failed: exit code 1", FullOutput: "Backup failed: exit code 1", ObjectName: "backup-fire!restic-backup"},
		{Host: "unraid", Service: "disk-temp", State: 1, Output: "Disk 3 temperature 52C", FullOutput: "Disk 3 temperature 52C", ObjectName: "unraid!disk-temp"},
	}
}

// ─── View contract tests: Plane popup ───────────────────────────────────────

func TestView_PlaneIssues(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()
	view := viewStripped(m)

	assertContains(t, view, "Plane Issues")
	assertContains(t, view, "Fix auth mid")  // truncated in split pane at 80 cols
	assertContains(t, view, "Add API rate")
	assertContains(t, view, "Refactor models")
	assertContains(t, view, "started")
	assertContains(t, view, "backlog")
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
	assertContains(t, view, "Backup failed") // in detail pane
	assertContains(t, view, "WARNING")
	assertContains(t, view, "unraid")
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
	m = sendSpecialKey(m, "alt+z")
	if m.popupCursor != 1 {
		t.Errorf("cursor should be 1, got %d", m.popupCursor)
	}

	m = sendSpecialKey(m, "alt+z")
	m = sendSpecialKey(m, "alt+z")
	if m.popupCursor != 3 {
		t.Errorf("cursor should be 3, got %d", m.popupCursor)
	}

	// Clamp at bottom
	m = sendSpecialKey(m, "alt+z")
	if m.popupCursor != 3 {
		t.Errorf("cursor should clamp at 3, got %d", m.popupCursor)
	}

	// Move back up
	m = sendSpecialKey(m, "alt+a")
	if m.popupCursor != 2 {
		t.Errorf("cursor should be 2, got %d", m.popupCursor)
	}

	// Clamp at top
	m.popupCursor = 0
	m = sendSpecialKey(m, "alt+a")
	if m.popupCursor != 0 {
		t.Errorf("cursor should clamp at 0, got %d", m.popupCursor)
	}
}

func TestKey_IcingaCursorNav(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeIcingaAlerts
	m.icingaProblems = fakeIcingaProblems()
	m.popupCursor = 0

	m = sendSpecialKey(m, "alt+z")
	if m.popupCursor != 1 {
		t.Errorf("cursor should be 1, got %d", m.popupCursor)
	}

	// Clamp at bottom (2 items, max index 1)
	m = sendSpecialKey(m, "alt+z")
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
	updated, _ := m.Update(planeIssuesMsg{reqID: m.planeReqID, issues: issues})
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
	updated, _ := m.Update(icingaProblemsMsg{reqID: m.icingaReqID, problems: problems})
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

	updated, _ := m.Update(popupErrMsg{reqID: m.planeReqID, source: "plane", text: "connection refused"})
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

// ─── Filter tests ──────────────────────────────────────────────────���────────

func TestFilter_PlaneMatchesTitle(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()
	m.popupFilter.SetValue("auth")

	filtered := filteredPlaneIssues(m)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 match for 'auth', got %d", len(filtered))
	}
	if filtered[0].Title != "Fix auth middleware" {
		t.Errorf("expected 'Fix auth middleware', got %q", filtered[0].Title)
	}
}

func TestFilter_PlaneMatchesPriority(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()
	m.popupFilter.SetValue("urgent")

	filtered := filteredPlaneIssues(m)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 match for 'urgent', got %d", len(filtered))
	}
}

func TestFilter_PlaneMatchesState(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()
	m.popupFilter.SetValue("backlog")

	filtered := filteredPlaneIssues(m)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 matches for 'backlog', got %d", len(filtered))
	}
}

func TestFilter_PlaneNoMatch(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()
	m.popupFilter.SetValue("zzzzz")

	filtered := filteredPlaneIssues(m)
	if len(filtered) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(filtered))
	}
}

func TestFilter_PlaneEmptyFilterReturnsAll(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()

	filtered := filteredPlaneIssues(m)
	if len(filtered) != len(m.planeIssues) {
		t.Fatalf("empty filter should return all %d issues, got %d", len(m.planeIssues), len(filtered))
	}
}

func TestFilter_PlaneNilIssuesReturnsNil(t *testing.T) {
	m := newTestModel(nil)
	m.planeIssues = nil

	filtered := filteredPlaneIssues(m)
	if filtered != nil {
		t.Fatalf("nil issues should return nil, got %v", filtered)
	}
}

func TestFilter_IcingaMatchesHost(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeIcingaAlerts
	m.icingaProblems = fakeIcingaProblems()
	m.popupFilter.SetValue("unraid")

	filtered := filteredIcingaProblems(m)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 match for 'unraid', got %d", len(filtered))
	}
	if filtered[0].Host != "unraid" {
		t.Errorf("expected host 'unraid', got %q", filtered[0].Host)
	}
}

func TestFilter_IcingaMatchesOutput(t *testing.T) {
	m := newTestModel(nil)
	m.icingaProblems = fakeIcingaProblems()
	m.popupFilter.SetValue("exit code")

	filtered := filteredIcingaProblems(m)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 match for 'exit code', got %d", len(filtered))
	}
}

func TestFilter_CaseInsensitive(t *testing.T) {
	m := newTestModel(nil)
	m.planeIssues = fakePlaneIssues()
	m.popupFilter.SetValue("FIX AUTH")

	filtered := filteredPlaneIssues(m)
	if len(filtered) != 1 {
		t.Fatalf("case-insensitive filter should match, got %d", len(filtered))
	}
}

func TestKey_FilterActivate(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()

	m = sendKey(m, "/")
	if !m.popupFilterActive {
		t.Error("/ should activate filter")
	}
}

func TestKey_FilterEscClears(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()
	m.popupFilterActive = true
	m.popupFilter.SetValue("test")

	m = sendSpecialKey(m, "esc")
	if m.popupFilterActive {
		t.Error("esc should deactivate filter")
	}
	if m.popupFilter.Value() != "" {
		t.Errorf("esc should clear filter, got %q", m.popupFilter.Value())
	}
	if m.mode != modePlaneIssues {
		t.Errorf("esc in filter should stay in popup mode, got %d", m.mode)
	}
}

func TestKey_FilterEnterConfirms(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()
	m.popupFilterActive = true
	m.popupFilter.SetValue("auth")
	m.popupFilter.Focus()

	m = sendSpecialKey(m, "enter")
	if m.popupFilterActive {
		t.Error("enter should deactivate filter")
	}
	// Filter value should persist
	if m.popupFilter.Value() != "auth" {
		t.Errorf("enter should keep filter value, got %q", m.popupFilter.Value())
	}
}

func TestKey_FilterCursorResets(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()
	m.popupCursor = 3

	// Activate filter and type — cursor should reset
	m = sendKey(m, "/")
	// Simulate typing by setting filter value directly since Bubbletea input is complex
	m.popupFilter.SetValue("auth")
	m.popupCursor = 0 // filter handler resets cursor
	filtered := filteredPlaneIssues(m)
	if m.popupCursor >= len(filtered) && len(filtered) > 0 {
		t.Error("cursor should be within filtered range")
	}
}

// ─── Sort tests ─────────────────────────────────────────────────────────────

func TestSort_PlaneCycles(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()

	if m.popupSortMode != 0 {
		t.Fatalf("initial sort mode should be 0, got %d", m.popupSortMode)
	}

	m = sendKey(m, "s")
	if m.popupSortMode != 1 {
		t.Errorf("after first s: sort mode should be 1, got %d", m.popupSortMode)
	}

	m = sendKey(m, "s")
	if m.popupSortMode != 2 {
		t.Errorf("after second s: sort mode should be 2, got %d", m.popupSortMode)
	}

	m = sendKey(m, "s")
	if m.popupSortMode != 3 {
		t.Errorf("after third s: sort mode should be 3, got %d", m.popupSortMode)
	}

	m = sendKey(m, "s")
	if m.popupSortMode != 0 {
		t.Errorf("after fourth s: sort mode should wrap to 0, got %d", m.popupSortMode)
	}
}

func TestSort_PlaneByPriority(t *testing.T) {
	m := newTestModel(nil)
	m.planeIssues = fakePlaneIssues()
	m.popupSortMode = 1 // priority

	sorted := filteredPlaneIssues(m)
	if len(sorted) < 2 {
		t.Fatal("need at least 2 issues")
	}
	// urgent should come first
	if sorted[0].Priority != "urgent" {
		t.Errorf("first issue should be urgent, got %q", sorted[0].Priority)
	}
}

func TestSort_PlaneByState(t *testing.T) {
	m := newTestModel(nil)
	m.planeIssues = fakePlaneIssues()
	m.popupSortMode = 2 // state

	sorted := filteredPlaneIssues(m)
	if len(sorted) < 2 {
		t.Fatal("need at least 2 issues")
	}
	// started should come first
	if sorted[0].StateGroup != "started" {
		t.Errorf("first issue should be started, got %q", sorted[0].StateGroup)
	}
}

func TestSort_PlaneByName(t *testing.T) {
	m := newTestModel(nil)
	m.planeIssues = fakePlaneIssues()
	m.popupSortMode = 3 // name

	sorted := filteredPlaneIssues(m)
	if len(sorted) < 2 {
		t.Fatal("need at least 2 issues")
	}
	// "Add API..." comes before "Fix auth..."
	if !strings.HasPrefix(sorted[0].Title, "Add") {
		t.Errorf("first issue by name should start with 'Add', got %q", sorted[0].Title)
	}
}

func TestSort_IcingaBySeverity(t *testing.T) {
	m := newTestModel(nil)
	m.icingaProblems = []icingaProblem{
		{Host: "host1", Service: "svc1", State: 1, Output: "warning"},
		{Host: "host2", Service: "svc2", State: 2, Output: "critical"},
	}
	m.popupSortMode = 1 // severity (critical first)

	sorted := filteredIcingaProblems(m)
	if sorted[0].State != 2 {
		t.Errorf("first by severity should be critical (2), got %d", sorted[0].State)
	}
}

func TestSort_IcingaByHost(t *testing.T) {
	m := newTestModel(nil)
	m.icingaProblems = fakeIcingaProblems() // backup-fire, unraid
	m.popupSortMode = 2 // host

	sorted := filteredIcingaProblems(m)
	if sorted[0].Host != "backup-fire" {
		t.Errorf("first by host should be backup-fire, got %q", sorted[0].Host)
	}
}

func TestSort_ResetsCursor(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()
	m.popupCursor = 3

	m = sendKey(m, "s")
	if m.popupCursor != 0 {
		t.Errorf("sort should reset cursor to 0, got %d", m.popupCursor)
	}
}

func TestSort_FilterAndSortCombined(t *testing.T) {
	m := newTestModel(nil)
	m.planeIssues = fakePlaneIssues()
	m.popupFilter.SetValue("backlog") // 2 items: "Add API...", "Refactor..."
	m.popupSortMode = 3               // name

	sorted := filteredPlaneIssues(m)
	if len(sorted) != 2 {
		t.Fatalf("expected 2 backlog items, got %d", len(sorted))
	}
	if !strings.HasPrefix(sorted[0].Title, "Add") {
		t.Errorf("sorted+filtered first should be 'Add...', got %q", sorted[0].Title)
	}
}

// ─── Sort label shown in view ───────────────────────────────────────────────

func TestView_PlaneSortLabel(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()
	m.popupSortMode = 1
	view := viewStripped(m)

	assertContains(t, view, "[priority]")
}

func TestView_IcingaSortLabel(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeIcingaAlerts
	m.icingaProblems = fakeIcingaProblems()
	m.popupSortMode = 1
	view := viewStripped(m)

	assertContains(t, view, "[severity]")
}

// ─── View shows filter input ────────────────────────────────────────────────

func TestView_PlaneShowsFilter(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()
	m.popupFilterActive = true
	m.popupFilter.SetValue("auth")
	view := viewStripped(m)

	assertContains(t, view, "/ ")
	assertContains(t, view, "auth")
}

// ─── Help bar shows new keys ────────────────────────────────────────────────

func TestView_PopupHelpBar(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()
	view := viewStripped(m)

	assertContains(t, view, "/ filter")
	assertContains(t, view, "s sort")
	assertContains(t, view, "Enter dispatch")
}

// ─── Action picker tests ────────────────────────────────────────────────────

func TestAction_OpenFromPlane(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("dev-session", "running"),
		fakeSessionItem("stopped-one", "stopped"),
	}
	m := newTestModel(items)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()
	m.popupCursor = 0

	m = sendSpecialKey(m, "enter")
	if m.mode != modePopupAction {
		t.Errorf("enter should open action picker, got mode %d", m.mode)
	}
	if !strings.Contains(m.actionTarget, "Fix auth middleware") {
		t.Errorf("actionTarget should contain issue title, got %q", m.actionTarget)
	}
	if !strings.Contains(m.actionPrompt, "Fix auth middleware") {
		t.Errorf("actionPrompt should contain issue title, got %q", m.actionPrompt)
	}
	if m.actionPrevMode != modePlaneIssues {
		t.Errorf("actionPrevMode should be modePlaneIssues, got %d", m.actionPrevMode)
	}
	// Only running sessions should appear
	if len(m.actionSessions) != 1 {
		t.Errorf("should have 1 running session, got %d", len(m.actionSessions))
	}
	if m.actionSessions[0].label != "dev-session" {
		t.Errorf("running session should be dev-session, got %q", m.actionSessions[0].label)
	}
}

func TestAction_OpenFromIcinga(t *testing.T) {
	items := []sidebarItem{fakeSessionItem("ops", "running")}
	m := newTestModel(items)
	m.mode = modeIcingaAlerts
	m.icingaProblems = fakeIcingaProblems()
	m.popupCursor = 0

	m = sendSpecialKey(m, "enter")
	if m.mode != modePopupAction {
		t.Errorf("enter should open action picker, got mode %d", m.mode)
	}
	if !strings.Contains(m.actionTarget, "backup-fire") {
		t.Errorf("actionTarget should contain host, got %q", m.actionTarget)
	}
	if !strings.Contains(m.actionPrompt, "restic-backup") {
		t.Errorf("actionPrompt should contain service, got %q", m.actionPrompt)
	}
}

func TestAction_EscReturnsToPopup(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePopupAction
	m.actionPrevMode = modePlaneIssues

	m = sendSpecialKey(m, "esc")
	if m.mode != modePlaneIssues {
		t.Errorf("esc should return to previous popup mode, got %d", m.mode)
	}
}

func TestAction_CursorNav(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("s1", "running"),
		fakeSessionItem("s2", "running"),
	}
	m := newTestModel(items)
	m.mode = modePopupAction
	m.actionSessions = items[:2] // 2 sessions
	m.actionCursor = 0

	// Navigate to second session
	m = sendSpecialKey(m, "alt+z")
	if m.actionCursor != 1 {
		t.Errorf("cursor should be 1, got %d", m.actionCursor)
	}

	// Navigate to "new session" option (index 2)
	m = sendSpecialKey(m, "alt+z")
	if m.actionCursor != 2 {
		t.Errorf("cursor should be 2 (new session), got %d", m.actionCursor)
	}

	// Clamp at bottom
	m = sendSpecialKey(m, "alt+z")
	if m.actionCursor != 2 {
		t.Errorf("cursor should clamp at 2, got %d", m.actionCursor)
	}

	// Back up
	m = sendSpecialKey(m, "alt+a")
	if m.actionCursor != 1 {
		t.Errorf("cursor should be 1, got %d", m.actionCursor)
	}
}

func TestAction_SpawnNew(t *testing.T) {
	spawner := &mockSpawner{}
	m := newTestModel(nil)
	m.spawner = spawner
	m.mode = modePopupAction
	m.actionSessions = nil // no existing sessions
	m.actionCursor = 0     // "new session" is index 0 when no sessions
	m.actionTarget = "Fix auth middleware"
	m.actionPrompt = "Work on Plane issue: Fix auth middleware"

	// Enter → context picker (not immediate dispatch)
	m = sendSpecialKey(m, "enter")
	if m.mode != modeActionContext {
		t.Fatalf("expected modeActionContext, got %d", m.mode)
	}

	// Esc → skip context, dispatch
	m = sendSpecialKey(m, "esc")

	if len(spawner.calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(spawner.calls))
	}
	if spawner.calls[0].name != "fix-auth-middleware" {
		t.Errorf("session name should be sanitized, got %q", spawner.calls[0].name)
	}
	if m.mode != modePassthrough {
		t.Errorf("should return to passthrough, got %d", m.mode)
	}
}

func TestAction_NoItemsNoEnter(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = []planeIssue{} // empty

	m = sendSpecialKey(m, "enter")
	// Should stay in plane mode since there's nothing to act on
	if m.mode != modePlaneIssues {
		t.Errorf("enter on empty list should stay in popup, got mode %d", m.mode)
	}
}

// ─── Action picker rendering ────────────────────────────────────────────────

func TestView_ActionPicker(t *testing.T) {
	items := []sidebarItem{fakeSessionItem("my-project", "running")}
	m := newTestModel(items)
	m.mode = modePopupAction
	m.actionTarget = "Fix auth middleware"
	m.actionPrompt = "Work on this"
	m.actionSessions = items
	m.actionCursor = 0
	view := viewStripped(m)

	assertContains(t, view, "Act on")
	assertContains(t, view, "Fix auth middleware")
	assertContains(t, view, "my-project")
	assertContains(t, view, "running")
	assertContains(t, view, "New session")
	assertContains(t, view, "confirm")
	assertContains(t, view, "cancel")
}

func TestView_ActionPickerNoSessions(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePopupAction
	m.actionTarget = "Check alert"
	m.actionSessions = nil
	m.actionCursor = 0
	view := viewStripped(m)

	assertContains(t, view, "New session")
	assertNotContains(t, view, "Send to existing")
}

func TestView_ActionPickerReplacesMain(t *testing.T) {
	items := []sidebarItem{fakeSessionItem("sess", "running")}
	m := newTestModel(items)
	m.mode = modePopupAction
	m.actionTarget = "test"
	view := viewStripped(m)

	assertNotContains(t, view, "SwarmOps")
}

// ─── Prompt generation ──────────────────────────────────────────────────────

func TestPlaneIssuePrompt(t *testing.T) {
	issue := planeIssue{Title: "Fix auth", Priority: "urgent", StateGroup: "started"}
	prompt := planeIssuePrompt(issue)

	if !strings.Contains(prompt, "Fix auth") {
		t.Errorf("prompt should contain title, got %q", prompt)
	}
	if !strings.Contains(prompt, "urgent") {
		t.Errorf("prompt should contain priority, got %q", prompt)
	}
	if !strings.Contains(prompt, "started") {
		t.Errorf("prompt should contain state, got %q", prompt)
	}
}

func TestIcingaProblemPrompt(t *testing.T) {
	problem := icingaProblem{Host: "web-01", Service: "http-check", State: 2, Output: "Connection refused", FullOutput: "Connection refused"}
	prompt := icingaProblemPrompt(problem)

	if !strings.Contains(prompt, "http-check") {
		t.Errorf("prompt should contain service, got %q", prompt)
	}
	if !strings.Contains(prompt, "web-01") {
		t.Errorf("prompt should contain host, got %q", prompt)
	}
	if !strings.Contains(prompt, "Connection refused") {
		t.Errorf("prompt should contain output, got %q", prompt)
	}
}

// ─── sanitizeSessionName ────────────────────────────────────────────────────

func TestSanitizeSessionName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Fix auth middleware", "fix-auth-middleware"},
		{"URGENT: Deploy now!!!", "urgent-deploy-now"},
		{"", "task"},
		{"a very long session name that exceeds thirty characters easily", "a-very-long-session-name-that-"},
		{"---test---", "test"},
		{"hello_world", "hello-world"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeSessionName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeSessionName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ─── Pure function tests: sort ──────────────────────────────────────────────

func TestSortPlaneIssues_AllModes(t *testing.T) {
	issues := []planeIssue{
		{Title: "Zebra", Priority: "low", StateGroup: "backlog"},
		{Title: "Alpha", Priority: "urgent", StateGroup: "started"},
		{Title: "Middle", Priority: "high", StateGroup: "unstarted"},
	}

	tests := []struct {
		mode      int
		firstWant string // expected Title of first element
	}{
		{0, "Zebra"},  // default: no sort, original order
		{1, "Alpha"},  // priority: urgent first
		{2, "Alpha"},  // state: started first
		{3, "Alpha"},  // name: Alpha first
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("mode_%d", tt.mode), func(t *testing.T) {
			result := sortPlaneIssues(issues, tt.mode)
			if len(result) != 3 {
				t.Fatalf("expected 3 issues, got %d", len(result))
			}
			if result[0].Title != tt.firstWant {
				t.Errorf("mode %d: first=%q, want %q", tt.mode, result[0].Title, tt.firstWant)
			}
		})
	}
}

func TestSortPlaneIssues_Empty(t *testing.T) {
	result := sortPlaneIssues(nil, 1)
	if result != nil {
		t.Errorf("sorting nil should return nil, got %v", result)
	}
}

func TestSortPlaneIssues_Single(t *testing.T) {
	issues := []planeIssue{{Title: "Only"}}
	result := sortPlaneIssues(issues, 1)
	if len(result) != 1 || result[0].Title != "Only" {
		t.Errorf("single item sort failed")
	}
}

func TestSortPlaneIssues_PreservesCount(t *testing.T) {
	issues := fakePlaneIssues()
	for mode := 0; mode <= 3; mode++ {
		result := sortPlaneIssues(issues, mode)
		if len(result) != len(issues) {
			t.Errorf("mode %d: count changed %d -> %d", mode, len(issues), len(result))
		}
	}
}

func TestSortPlaneIssues_DoesNotMutateOriginal(t *testing.T) {
	issues := fakePlaneIssues()
	firstTitle := issues[0].Title
	_ = sortPlaneIssues(issues, 3) // sort by name
	if issues[0].Title != firstTitle {
		t.Errorf("sort mutated original: first was %q, now %q", firstTitle, issues[0].Title)
	}
}

func TestSortIcingaProblems_AllModes(t *testing.T) {
	problems := []icingaProblem{
		{Host: "zebra", Service: "zz", State: 1},
		{Host: "alpha", Service: "aa", State: 2},
	}

	tests := []struct {
		mode      int
		firstHost string
	}{
		{0, "zebra"}, // default
		{1, "alpha"}, // severity: critical (2) first
		{2, "alpha"}, // host: alpha first
		{3, "alpha"}, // service: aa first
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("mode_%d", tt.mode), func(t *testing.T) {
			result := sortIcingaProblems(problems, tt.mode)
			if result[0].Host != tt.firstHost {
				t.Errorf("mode %d: first host=%q, want %q", tt.mode, result[0].Host, tt.firstHost)
			}
		})
	}
}

func TestSortIcingaProblems_Empty(t *testing.T) {
	result := sortIcingaProblems(nil, 1)
	if result != nil {
		t.Errorf("sorting nil should return nil")
	}
}

func TestSortIcingaProblems_DoesNotMutateOriginal(t *testing.T) {
	problems := fakeIcingaProblems()
	firstHost := problems[0].Host
	_ = sortIcingaProblems(problems, 2) // sort by host
	if problems[0].Host != firstHost {
		t.Errorf("sort mutated original")
	}
}

// ─── Pure function tests: filter ────────────────────────────────────────────

func TestFilterPlane_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		issues   []planeIssue
		query    string
		wantLen  int
	}{
		{"nil issues", nil, "test", 0},
		{"empty issues", []planeIssue{}, "test", 0},
		{"empty query returns all", fakePlaneIssues(), "", 4},
		{"whitespace query returns all", fakePlaneIssues(), "  ", 4},
		{"exact title match", fakePlaneIssues(), "Fix auth middleware", 1},
		{"partial match", fakePlaneIssues(), "API", 1},
		{"priority match", fakePlaneIssues(), "urgent", 1},
		{"state match", fakePlaneIssues(), "started", 2}, // matches "started" and "unstarted"
		{"no match", fakePlaneIssues(), "nonexistent", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel(nil)
			m.planeIssues = tt.issues
			m.popupFilter.SetValue(tt.query)
			result := filteredPlaneIssues(m)
			gotLen := len(result)
			if tt.issues == nil && result != nil {
				t.Errorf("nil issues should return nil")
			} else if gotLen != tt.wantLen {
				t.Errorf("got %d results, want %d", gotLen, tt.wantLen)
			}
		})
	}
}

func TestFilterIcinga_EdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		problems []icingaProblem
		query   string
		wantLen int
	}{
		{"nil problems", nil, "test", 0},
		{"empty problems", []icingaProblem{}, "test", 0},
		{"empty query returns all", fakeIcingaProblems(), "", 2},
		{"host match", fakeIcingaProblems(), "backup-fire", 1},
		{"service match", fakeIcingaProblems(), "disk-temp", 1},
		{"output match", fakeIcingaProblems(), "exit code", 1},
		{"no match", fakeIcingaProblems(), "nonexistent", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel(nil)
			m.icingaProblems = tt.problems
			m.popupFilter.SetValue(tt.query)
			result := filteredIcingaProblems(m)
			gotLen := len(result)
			if tt.problems == nil && result != nil {
				t.Errorf("nil problems should return nil")
			} else if gotLen != tt.wantLen {
				t.Errorf("got %d results, want %d", gotLen, tt.wantLen)
			}
		})
	}
}

// suppress unused import warning
var _ = fmt.Sprintf
