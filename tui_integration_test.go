package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func altKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true}
}

func TestIntegration_CursorNavigation(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("alpha", "running"),
		fakeSessionItem("beta", "running"),
		fakeSessionItem("gamma", "stopped"),
	}
	m := newTestModel(items)
	if m.cursor != 0 {
		t.Fatalf("initial cursor: want 0, got %d", m.cursor)
	}
	updated, _ := m.Update(altKey('z'))
	m = updated.(tuiModel)
	if m.cursor != 1 {
		t.Errorf("after alt+z: want 1, got %d", m.cursor)
	}
	updated, _ = m.Update(altKey('z'))
	m = updated.(tuiModel)
	if m.cursor != 2 {
		t.Errorf("after 2x alt+z: want 2, got %d", m.cursor)
	}
	updated, _ = m.Update(altKey('z'))
	m = updated.(tuiModel)
	if m.cursor != 2 {
		t.Errorf("clamp at bottom: want 2, got %d", m.cursor)
	}
	updated, _ = m.Update(altKey('a'))
	m = updated.(tuiModel)
	if m.cursor != 1 {
		t.Errorf("after alt+a: want 1, got %d", m.cursor)
	}
}

func TestIntegration_ScrollState(t *testing.T) {
	m := newTestModel([]sidebarItem{
		fakeSessionItem("s1", "running"),
		fakeSessionItem("s2", "running"),
	})
	if m.userScrolled {
		t.Error("should be false initially")
	}
	m.userScrolled = true
	updated, _ := m.Update(altKey('z'))
	m = updated.(tuiModel)
	if m.userScrolled {
		t.Error("should reset after switching session")
	}
}

func TestIntegration_RenameFlow(t *testing.T) {
	m := newTestModel([]sidebarItem{fakeSessionItem("old-name", "running")})
	updated, _ := m.Update(altKey('r'))
	m = updated.(tuiModel)
	if m.mode != modeRename {
		t.Fatalf("want modeRename, got %d", m.mode)
	}
	if m.renameInput.Value() != "old-name" {
		t.Errorf("want pre-filled 'old-name', got %q", m.renameInput.Value())
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(tuiModel)
	if m.mode != modePassthrough {
		t.Errorf("esc should cancel, got mode %d", m.mode)
	}
	updated, _ = m.Update(altKey('r'))
	m = updated.(tuiModel)
	m.renameInput.SetValue("new-name")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if m.mode != modePassthrough {
		t.Errorf("enter should confirm, got mode %d", m.mode)
	}
	if m.flash != "Renamed to new-name" {
		t.Errorf("want rename flash, got %q", m.flash)
	}
}

func TestIntegration_RenameOnPoolSlot(t *testing.T) {
	m := newTestModel([]sidebarItem{fakePoolItem("claude-haiku-4-5", "idle")})
	updated, _ := m.Update(altKey('r'))
	m = updated.(tuiModel)
	if m.mode != modePassthrough {
		t.Error("alt+r on pool slot should stay in passthrough")
	}
}

func TestIntegration_FeedbackFlow(t *testing.T) {
	m := newTestModel([]sidebarItem{fakeSessionItem("s1", "running")})
	updated, _ := m.Update(altKey('f'))
	m = updated.(tuiModel)
	if m.mode != modeFeedbackType {
		t.Fatalf("want modeFeedbackType, got %d", m.mode)
	}
	if m.feedbackType != 0 {
		t.Errorf("initial type should be 0 (bug), got %d", m.feedbackType)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(tuiModel)
	if m.feedbackType != 1 {
		t.Errorf("after right: want 1, got %d", m.feedbackType)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(tuiModel)
	if m.feedbackType != 0 {
		t.Errorf("after left: want 0, got %d", m.feedbackType)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(tuiModel)
	if m.mode != modePassthrough {
		t.Error("esc should cancel")
	}
	// Full flow: bug type → enter → text → enter
	updated, _ = m.Update(altKey('f'))
	m = updated.(tuiModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if m.mode != modeFeedbackText {
		t.Fatalf("want modeFeedbackText, got %d", m.mode)
	}
	m.feedbackInput.SetValue("scroll snaps back")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if m.mode != modePassthrough {
		t.Errorf("after submit: want passthrough, got %d", m.mode)
	}
	if m.flash != "Submitted bug: scroll snaps back" {
		t.Errorf("want submit flash, got %q", m.flash)
	}
}

func TestIntegration_PlanePopup(t *testing.T) {
	m := newTestModel([]sidebarItem{fakeSessionItem("s1", "running")})
	updated, _ := m.Update(altKey('p'))
	m = updated.(tuiModel)
	if m.mode != modePlaneIssues {
		t.Errorf("want modePlaneIssues, got %d", m.mode)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(tuiModel)
	if m.mode != modePassthrough {
		t.Error("esc should close popup")
	}
}

func TestIntegration_IcingaPopup(t *testing.T) {
	m := newTestModel([]sidebarItem{fakeSessionItem("s1", "running")})
	updated, _ := m.Update(altKey('i'))
	m = updated.(tuiModel)
	if m.mode != modeIcingaAlerts {
		t.Errorf("want modeIcingaAlerts, got %d", m.mode)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(tuiModel)
	if m.mode != modePassthrough {
		t.Error("esc should close popup")
	}
}

func TestIntegration_NewSessionFlow(t *testing.T) {
	m := newTestModel(nil)
	updated, _ := m.Update(altKey('n'))
	m = updated.(tuiModel)
	if m.mode != modeNewName {
		t.Fatalf("want modeNewName, got %d", m.mode)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(tuiModel)
	if m.mode != modePassthrough {
		t.Error("esc should cancel")
	}
	updated, _ = m.Update(altKey('n'))
	m = updated.(tuiModel)
	m.newNameInput.SetValue("test-session")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if m.mode != modeNewDir {
		t.Fatalf("want modeNewDir, got %d", m.mode)
	}
}

func TestIntegration_DeleteSession(t *testing.T) {
	m := newTestModel([]sidebarItem{fakeSessionItem("to-delete", "stopped")})
	updated, _ := m.Update(altKey('d'))
	m = updated.(tuiModel)
	if m.flash != "Deleted to-delete" {
		t.Errorf("want delete flash, got %q", m.flash)
	}
}

func TestIntegration_QuitKey(t *testing.T) {
	m := newTestModel(nil)
	_, cmd := m.Update(altKey('q'))
	if cmd == nil {
		t.Fatal("alt+q should produce quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected QuitMsg, got %T", msg)
	}
}

func TestIntegration_AnimFrame(t *testing.T) {
	m := newTestModel(nil)
	if m.animFrame != 0 {
		t.Fatalf("initial animFrame: want 0, got %d", m.animFrame)
	}
	updated, _ := m.Update(tickMsg{})
	m = updated.(tuiModel)
	if m.animFrame != 1 {
		t.Errorf("after tick: want 1, got %d", m.animFrame)
	}
}

func TestIntegration_AnimatedIndicator(t *testing.T) {
	if animatedIndicator("stopped", 0) != statusStopped {
		t.Error("stopped should return statusStopped")
	}
	if animatedIndicator("working", 0) == animatedIndicator("working", 1) {
		t.Error("working should animate between frames")
	}
	if animatedIndicator("awaiting", 0) == animatedIndicator("awaiting", 1) {
		t.Error("awaiting should animate between frames")
	}
}

func TestIntegration_StatusBarHints(t *testing.T) {
	m := newTestModel([]sidebarItem{fakeSessionItem("s1", "running")})
	m.w = 140
	m.h = 30
	view := m.View()
	for _, hint := range []string{"Alt+A", "Alt+Z", "Alt+N", "Alt+R", "Alt+D", "Alt+P", "Alt+I", "Alt+F", "Alt+Q"} {
		if !strings.Contains(view, hint) {
			t.Errorf("status bar should contain %q", hint)
		}
	}
}

var _ = textinput.New
