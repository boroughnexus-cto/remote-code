// TUI test module 09 — Settings overlay story tests
//
// User stories:
//   US-ST.1  esc from sidebar (no overlay) opens settings
//   US-ST.2  esc/q inside settings closes it
//   US-ST.3  settings renders a section title
//   US-ST.4  tab cycles to next section
//   US-ST.5  shift+tab cycles to previous section
//   US-ST.6  settings is blocked when another overlay is open
//   US-ST.7  personas section up/down navigation
//   US-ST.8  personas section renders injected persona names
//   US-ST.9  personas section 'e' key fires editor cmd for selected persona

package main

import (
	"testing"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// openSettings opens the settings overlay by pressing esc from sidebar focus.
func openSettings(m tuiModel) tuiModel {
	return drive(m, keyEsc())
}

// injectPersonas injects loaded persona items into an open settings overlay.
// The settings model must already be open with personas as the active section.
func injectPersonas(m tuiModel, personas ...personaItem) tuiModel {
	return drive(m, personasLoadedMsg{items: personas})
}

// ─── US-ST.1: esc from sidebar opens settings ─────────────────────────────

func TestSettings_EscFromSidebarOpens(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = openSettings(m)
	if m.settings == nil {
		t.Error("settings should open after esc from sidebar")
	}
}

// ─── US-ST.2: esc/q inside settings closes it ─────────────────────────────

func TestSettings_EscClosesSettings(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = openSettings(m)
	if m.settings == nil {
		t.Fatal("settings should be open")
	}
	m = drive(m, keyEsc())
	if m.settings != nil {
		t.Error("settings should close after esc")
	}
}

func TestSettings_QClosesSettings(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = openSettings(m)
	if m.settings == nil {
		t.Fatal("settings should be open")
	}
	m = drive(m, keyRune('q'))
	if m.settings != nil {
		t.Error("settings should close after q")
	}
}

// ─── US-ST.3: settings renders a section title ────────────────────────────

func TestSettings_RendersSectionTitle(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = openSettings(m)
	// The personas section header is always present in the first tab.
	assertView(t, m, "Agent Personas")
}

// ─── US-ST.4: tab cycles to next section ──────────────────────────────────

func TestSettings_TabCyclesToNextSection(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = openSettings(m)
	initial := m.settings.active
	m = drive(m, keyTab())
	if m.settings == nil {
		t.Fatal("settings should still be open after tab")
	}
	if m.settings.active == initial {
		t.Error("tab should advance to next section")
	}
	wantNext := (initial + 1) % len(m.settings.sections)
	if m.settings.active != wantNext {
		t.Errorf("active section: want %d, got %d", wantNext, m.settings.active)
	}
}

// ─── US-ST.5: shift+tab cycles to previous section ────────────────────────

func TestSettings_ShiftTabCyclesToPrevSection(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = openSettings(m)
	// Move to section 1 first so shift+tab can go back.
	m = drive(m, keyTab())
	afterTab := m.settings.active
	m = drive(m, keyShiftTab())
	if m.settings == nil {
		t.Fatal("settings should still be open")
	}
	total := len(m.settings.sections)
	wantPrev := (afterTab - 1 + total) % total
	if m.settings.active != wantPrev {
		t.Errorf("active section after shift+tab: want %d, got %d", wantPrev, m.settings.active)
	}
}

// ─── US-ST.6: settings blocked when overlay already open ──────────────────

func TestSettings_BlockedWhenModalOpen(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	// Open new-session modal first.
	m = drive(m, keyRune('c'))
	if m.modal == nil {
		t.Fatal("modal should be open after c")
	}
	// Esc should close modal, not open settings.
	m = drive(m, keyEsc())
	if m.settings != nil {
		t.Error("settings should not open when modal is being closed")
	}
}

// ─── US-ST.7: personas section up/down navigation ─────────────────────────

func TestSettings_PersonasNavDown(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = openSettings(m)
	m = injectPersonas(m,
		personaItem{role: "dev", prompt: "a developer"},
		personaItem{role: "qa", prompt: "a qa engineer"},
	)

	// Get current cursor from the active section.
	ps := m.settings.sections[0].(*personasSection)
	initial := ps.cursor
	m = drive(m, keyDown())
	ps = m.settings.sections[0].(*personasSection)
	if ps.cursor != initial+1 {
		t.Errorf("cursor after down: want %d, got %d", initial+1, ps.cursor)
	}
}

func TestSettings_PersonasNavUpDoesNotGoNegative(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = openSettings(m)
	m = injectPersonas(m, personaItem{role: "dev", prompt: "a developer"})
	m = drive(m, keyUp())
	ps := m.settings.sections[0].(*personasSection)
	if ps.cursor < 0 {
		t.Errorf("cursor went negative: %d", ps.cursor)
	}
}

// ─── US-ST.8: personas section renders persona names ─────────────────────

func TestSettings_PersonasRendersNames(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = openSettings(m)
	m = injectPersonas(m,
		personaItem{role: "senior-dev", prompt: "You are a senior developer"},
		personaItem{role: "qa-agent", prompt: "You are a QA engineer"},
	)

	assertView(t, m, "senior-dev")
	assertView(t, m, "qa-agent")
}

// ─── US-ST.9: personas 'e' key fires editor cmd ───────────────────────────

func TestSettings_PersonasEKeyFiresEditorCmd(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = openSettings(m)
	m = injectPersonas(m, personaItem{role: "dev-role", prompt: "The developer prompt"})

	// 'e' should return a cmd that produces tuiRolePromptEditMsg.
	_, cmds := driveCapture(m, keyRune('e'))
	if len(cmds) == 0 {
		t.Fatal("'e' on persona should return a cmd")
	}
	found := false
	for _, cmd := range cmds {
		if cmd == nil {
			continue
		}
		msg := cmd()
		if edit, ok := msg.(tuiRolePromptEditMsg); ok {
			if edit.role != "dev-role" {
				t.Errorf("edit.role: want %q, got %q", "dev-role", edit.role)
			}
			found = true
		}
	}
	if !found {
		t.Error("expected tuiRolePromptEditMsg from 'e' key on persona item")
	}
}
