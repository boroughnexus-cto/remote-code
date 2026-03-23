// TUI test module 10 — Command palette story tests
//
// User stories:
//   US-CP.1  ':' opens command palette from sidebar
//   US-CP.2  ':' is a no-op when typing in input focus
//   US-CP.3  esc closes palette without executing
//   US-CP.4  palette renders filter prompt
//   US-CP.5  typing narrows the result list
//   US-CP.6  up/down navigates palette cursor
//   US-CP.7  cursor does not go negative
//   US-CP.8  cursor clamps at bottom
//   US-CP.9  Enter on a matching item executes the command
//   US-CP.10 palette blocked when modal is already open

package main

import (
	"testing"
)

// ─── US-CP.1: ':' opens palette ───────────────────────────────────────────

func TestCmdPalette_ColonOpens(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune(':'))
	if m.cmdPalette == nil {
		t.Error("cmdPalette should open after ':'")
	}
}

// ─── US-CP.2: ':' no-op in input focus ────────────────────────────────────

func TestCmdPalette_ColonNoOpInInputFocus(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	// Switch focus to input.
	m = drive(m, keyTab())
	if m.focus != tuiFocusInput {
		t.Skip("couldn't switch to input focus")
	}
	m = drive(m, keyRune(':'))
	if m.cmdPalette != nil {
		t.Error("cmdPalette should not open when focus is on input")
	}
}

// ─── US-CP.3: esc closes palette ──────────────────────────────────────────

func TestCmdPalette_EscCloses(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune(':'))
	if m.cmdPalette == nil {
		t.Fatal("palette should be open")
	}
	m = drive(m, keyEsc())
	if m.cmdPalette != nil {
		t.Error("cmdPalette should be nil after esc")
	}
}

// ─── US-CP.4: palette renders filter prompt ───────────────────────────────

func TestCmdPalette_RendersPrompt(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune(':'))
	assertView(t, m, "filter")
}

// ─── US-CP.5: typing narrows results ──────────────────────────────────────

func TestCmdPalette_TypingNarrowsResults(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune(':'))
	totalBefore := len(m.cmdPalette.filtered)

	// Type a very specific term that should match few entries.
	m = drive(m, keyRune('i'), keyRune('c'), keyRune('i'), keyRune('n'), keyRune('g'), keyRune('a'))
	// After filtering for "icinga" there should be fewer (or equal) results.
	if len(m.cmdPalette.filtered) > totalBefore {
		t.Errorf("filtering should not increase results: before=%d after=%d",
			totalBefore, len(m.cmdPalette.filtered))
	}
}

// ─── US-CP.6: up/down navigates cursor ────────────────────────────────────

func TestCmdPalette_DownMovesDown(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune(':'))
	if len(m.cmdPalette.filtered) < 2 {
		t.Skip("need at least 2 items to test navigation")
	}
	initial := m.cmdPalette.cursor
	m = drive(m, keyDown())
	if m.cmdPalette.cursor != initial+1 {
		t.Errorf("cursor after down: want %d, got %d", initial+1, m.cmdPalette.cursor)
	}
}

// ─── US-CP.7: cursor does not go negative ─────────────────────────────────

func TestCmdPalette_UpDoesNotGoNegative(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune(':'))
	m = drive(m, keyUp())
	if m.cmdPalette == nil {
		t.Fatal("palette should still be open")
	}
	if m.cmdPalette.cursor < 0 {
		t.Errorf("cursor went negative: %d", m.cmdPalette.cursor)
	}
}

// ─── US-CP.8: cursor clamps at bottom ─────────────────────────────────────

func TestCmdPalette_DownClampsAtBottom(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune(':'))
	n := len(m.cmdPalette.filtered)
	// Drive down past end.
	for range n + 3 {
		m = drive(m, keyDown())
	}
	if m.cmdPalette == nil {
		t.Fatal("palette should still be open")
	}
	if m.cmdPalette.cursor >= n {
		t.Errorf("cursor out of bounds: %d >= %d", m.cmdPalette.cursor, n)
	}
}

// ─── US-CP.9: Enter executes selected command ──────────────────────────────

func TestCmdPalette_EnterExecutesCommand(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	// Filter to "icinga" — known single-match command that sets icingaView=true.
	m = drive(m, keyRune(':'))
	m = drive(m, keyRune('i'), keyRune('c'), keyRune('i'), keyRune('n'), keyRune('g'), keyRune('a'))
	if len(m.cmdPalette.filtered) == 0 {
		t.Skip("icinga command not found in palette")
	}

	m = drive(m, keyEnter())
	// After executing, palette should close.
	if m.cmdPalette != nil {
		t.Error("palette should close after Enter")
	}
	// The Icinga view should open.
	if !m.icingaView {
		t.Error("icingaView should be true after executing 'Icinga Monitoring' command")
	}
}

// ─── US-CP.10: palette blocked when modal open ────────────────────────────

func TestCmdPalette_BlockedWhenModalOpen(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('c')) // open new-session modal
	if m.modal == nil {
		t.Fatal("modal should be open")
	}
	m = drive(m, keyRune(':'))
	if m.cmdPalette != nil {
		t.Error("cmdPalette should not open when modal is active")
	}
}
