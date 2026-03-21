// TUI test module 01 — Navigation
//
// User stories:
//   US-N.1  Down/j advances cursor through sidebar items
//   US-N.2  Up/k moves cursor backwards
//   US-N.3  Cursor clamps at top and bottom boundaries
//   US-N.4  Enter on a session toggles collapse/expand
//   US-N.5  Tab switches focus to chat input
//   US-N.6  Esc from input returns focus to sidebar
//   US-N.7  q quits (returns quit command)
//   US-N.8  ? shows help overlay; any other key hides it

package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestNav_DownAdvancesCursor(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	initial := m.cursor
	m = drive(m, keyDown())
	if m.cursor != initial+1 {
		t.Errorf("cursor after down: want %d, got %d", initial+1, m.cursor)
	}
}

func TestNav_JAdvancesCursor(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	initial := m.cursor
	m = drive(m, keyRune('j'))
	if m.cursor != initial+1 {
		t.Errorf("cursor after j: want %d, got %d", initial+1, m.cursor)
	}
}

func TestNav_UpRetreats(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyDown(), keyDown())
	after2 := m.cursor
	m = drive(m, keyUp())
	if m.cursor != after2-1 {
		t.Errorf("cursor after up: want %d, got %d", after2-1, m.cursor)
	}
}

func TestNav_KRetreats(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyDown(), keyDown())
	after2 := m.cursor
	m = drive(m, keyRune('k'))
	if m.cursor != after2-1 {
		t.Errorf("cursor after k: want %d, got %d", after2-1, m.cursor)
	}
}

func TestNav_CursorClampsAtTop(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	// Already at 0 — up should not go negative.
	m = drive(m, keyUp())
	if m.cursor != 0 {
		t.Errorf("cursor should stay at 0 when already at top, got %d", m.cursor)
	}
}

func TestNav_CursorClampsAtBottom(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	// Drive past end.
	for range 100 {
		m = drive(m, keyDown())
	}
	if m.cursor >= len(m.items) {
		t.Errorf("cursor %d exceeds item count %d", m.cursor, len(m.items))
	}
}

func TestNav_EnterCollapseSession(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	// Cursor should start at the first session item.
	if len(m.items) == 0 {
		t.Fatal("no items in sidebar")
	}
	if m.items[0].kind != tuiItemSession {
		t.Fatal("first item is not a session")
	}
	sid := m.items[0].sid
	countBefore := len(m.items)

	m = drive(m, keyEnter())

	if !m.collapsedSessions[sid] {
		t.Error("session should be collapsed after Enter")
	}
	if len(m.items) >= countBefore {
		t.Errorf("items should shrink after collapse: before=%d after=%d", countBefore, len(m.items))
	}
}

func TestNav_EnterExpandSession(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	sid := m.items[0].sid
	countBefore := len(m.items)

	// Collapse then expand.
	m = drive(m, keyEnter(), keyEnter())

	if m.collapsedSessions[sid] {
		t.Error("session should be expanded after second Enter")
	}
	if len(m.items) != countBefore {
		t.Errorf("items should restore after expand: want %d, got %d", countBefore, len(m.items))
	}
}

func TestNav_TabFocusesInput(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	if m.focus != tuiFocusSidebar {
		t.Fatalf("initial focus should be sidebar, got %d", m.focus)
	}
	m = drive(m, keyTab())
	if m.focus != tuiFocusInput {
		t.Errorf("focus after Tab: want tuiFocusInput (%d), got %d", tuiFocusInput, m.focus)
	}
}

func TestNav_SlashFocusesInput(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('/'))
	if m.focus != tuiFocusInput {
		t.Errorf("focus after /: want tuiFocusInput, got %d", m.focus)
	}
}

func TestNav_EscFromInputRestoresSidebar(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyTab(), keyEsc())
	if m.focus != tuiFocusSidebar {
		t.Errorf("focus after Esc from input: want tuiFocusSidebar (%d), got %d", tuiFocusSidebar, m.focus)
	}
}

func TestNav_QReturnsQuit(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	_, cmd := m.Update(keyRune('q'))
	if cmd == nil {
		t.Fatal("q should return a quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("q command should produce tea.QuitMsg, got %T", msg)
	}
}

func TestNav_QuestionMarkShowsHelp(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	if m.helpVisible {
		t.Fatal("help should not be visible initially")
	}
	m = drive(m, keyRune('?'))
	if !m.helpVisible {
		t.Error("help should be visible after ?")
	}
}

func TestNav_AnyKeyHidesHelp(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('?'))
	m = drive(m, keyRune('j')) // any non-? key
	if m.helpVisible {
		t.Error("help should be hidden after any non-? key")
	}
}
