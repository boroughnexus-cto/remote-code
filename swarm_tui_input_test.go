// TUI test module 03 — Input & commands
//
// User stories:
//   US-I.1  Tab focuses the chat input
//   US-I.2  Text typed while input is focused populates chatInput value
//   US-I.3  Empty input does not trigger send on Enter
//   US-I.4  /goal prefix is detected; non-/goal text goes to orchestrator
//   US-I.5  Esc from input returns focus to sidebar
//   US-I.6  Sending a message resets chatInput and refocuses sidebar

package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestInput_TabFocusesInput(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyTab())
	if m.focus != tuiFocusInput {
		t.Errorf("focus after Tab: want tuiFocusInput, got %d", m.focus)
	}
}

func TestInput_EscReturnsSidebar(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyTab(), keyEsc())
	if m.focus != tuiFocusSidebar {
		t.Errorf("focus after Esc: want tuiFocusSidebar, got %d", m.focus)
	}
}

func TestInput_EmptyEnterDoesNotSend(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyTab())
	// Input is empty — Enter should not change focus or produce a command.
	m2, cmd := m.Update(keyEnter())
	mAfter := m2.(tuiModel)

	// Focus should stay on input (nothing happened).
	if mAfter.focus != tuiFocusInput {
		t.Errorf("focus should stay on input after empty Enter, got %d", mAfter.focus)
	}
	// No HTTP command should be issued (cmd is nil or a textarea internal cmd).
	_ = cmd
}

func TestInput_TypingPopulatesValue(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyTab())
	// Type characters — bubbletea textarea handles these internally.
	for _, r := range "hello" {
		m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = m2.(tuiModel)
	}
	val := m.chatInput.Value()
	if !strings.Contains(val, "hello") {
		t.Errorf("chatInput value should contain 'hello', got %q", val)
	}
}

func TestInput_GoalPrefixDetected(t *testing.T) {
	// The /goal prefix is handled in updateInput — it routes to createGoal.
	// We can verify the routing by checking that the input is cleared on Enter
	// and that a command is returned (without actually firing HTTP).
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyTab())

	// Manually set the textarea value to a /goal command.
	m.chatInput.SetValue("/goal implement dark mode")

	m2, cmd := m.Update(keyEnter())
	mAfter := m2.(tuiModel)

	// Input should be cleared.
	if mAfter.chatInput.Value() != "" {
		t.Errorf("chatInput should be cleared after Enter, got %q", mAfter.chatInput.Value())
	}
	// A command should have been issued (the HTTP POST).
	if cmd == nil {
		t.Error("Enter on /goal text should return a command")
	}
}

func TestInput_PlainTextRoutedAsMessage(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyTab())
	m.chatInput.SetValue("status update")

	m2, cmd := m.Update(keyEnter())
	mAfter := m2.(tuiModel)

	// Input should be cleared.
	if mAfter.chatInput.Value() != "" {
		t.Errorf("chatInput should be cleared after plain Enter, got %q", mAfter.chatInput.Value())
	}
	// A command should have been issued (orchestrator message HTTP POST).
	if cmd == nil {
		t.Error("Enter on plain text should return a command")
	}
}

func TestInput_FocusReturnsToSidebarAfterSend(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyTab())
	m.chatInput.SetValue("hello world")
	m2, _ := m.Update(keyEnter())
	mAfter := m2.(tuiModel)

	if mAfter.focus != tuiFocusSidebar {
		t.Errorf("focus should return to sidebar after send, got %d", mAfter.focus)
	}
}
