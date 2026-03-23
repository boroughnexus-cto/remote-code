// TUI test module 11 — Layout and rendering story tests
//
// User stories:
//   US-LY.1  View() does not panic at 200×50
//   US-LY.2  View() does not panic at 80×24 (standard terminal)
//   US-LY.3  View() does not panic at 40×16 (small terminal)
//   US-LY.4  View() returns "too small" guard at 28×15
//   US-LY.5  View() returns "Loading…" when w==0
//   US-LY.6  WindowSizeMsg updates model dimensions
//   US-LY.7  All overlays render without panic at 80×24
//   US-LY.8  Help screen renders without panic

package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// modelAtSize creates a model sized to the given terminal dimensions.
func modelAtSize(w, h int, sessions []tuiSession, states map[string]tuiState) tuiModel {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = m2.(tuiModel)
	if sessions != nil {
		m2, _ = m.Update(tuiDataMsg{sessions: sessions, states: states})
		m = m2.(tuiModel)
	}
	return m
}

// noPanic calls f and fails the test if it panics.
func noPanic(t *testing.T, label string, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s panicked: %v", label, r)
		}
	}()
	f()
}

// ─── US-LY.1: no panic at 200×50 ──────────────────────────────────────────

func TestLayout_NoPanicAt200x50(t *testing.T) {
	sessions, states := stdSessions()
	m := modelAtSize(200, 50, sessions, states)
	noPanic(t, "View() at 200×50", func() { _ = m.View() })
}

// ─── US-LY.2: no panic at 80×24 ───────────────────────────────────────────

func TestLayout_NoPanicAt80x24(t *testing.T) {
	sessions, states := stdSessions()
	m := modelAtSize(80, 24, sessions, states)
	noPanic(t, "View() at 80×24", func() { _ = m.View() })
}

// ─── US-LY.3: no panic at 40×16 ───────────────────────────────────────────

func TestLayout_NoPanicAt40x16(t *testing.T) {
	sessions, states := stdSessions()
	m := modelAtSize(40, 16, sessions, states)
	noPanic(t, "View() at 40×16", func() { _ = m.View() })
}

// ─── US-LY.4: too-small guard at 28×15 ────────────────────────────────────

func TestLayout_TooSmallGuard(t *testing.T) {
	sessions, states := stdSessions()
	m := modelAtSize(28, 15, sessions, states)
	noPanic(t, "View() at 28×15", func() {
		v := m.View()
		if !strings.Contains(v, "small") && !strings.Contains(v, "resize") {
			t.Errorf("expected too-small message at 28×15, got: %q", v)
		}
	})
}

// ─── US-LY.5: "Loading…" when w==0 ───────────────────────────────────────

func TestLayout_LoadingWhenNoSize(t *testing.T) {
	m := newTUIModel() // no WindowSizeMsg — w==0
	v := m.View()
	if !strings.Contains(v, "Loading") {
		t.Errorf("expected Loading message when w==0, got: %q", v)
	}
}

// ─── US-LY.6: WindowSizeMsg updates dimensions ────────────────────────────

func TestLayout_WindowSizeMsgUpdatesDimensions(t *testing.T) {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = m2.(tuiModel)
	if m.w != 120 {
		t.Errorf("m.w: want 120, got %d", m.w)
	}
	if m.h != 40 {
		t.Errorf("m.h: want 40, got %d", m.h)
	}
}

// ─── US-LY.7: all overlays render without panic at 80×24 ──────────────────

func TestLayout_GoalViewNoPanic(t *testing.T) {
	sessions, states := stdSessions()
	m := modelAtSize(80, 24, sessions, states)
	m = drive(m, keyRune('g'))
	noPanic(t, "goalView at 80×24", func() { _ = m.View() })
}

func TestLayout_WorkQueueNoPanic(t *testing.T) {
	sessions, states := stdSessions()
	m := modelAtSize(80, 24, sessions, states)
	m = drive(m, keyRune('W'))
	noPanic(t, "workQueueView at 80×24", func() { _ = m.View() })
}

func TestLayout_EventLogNoPanic(t *testing.T) {
	sessions, states := stdSessions()
	m := modelAtSize(80, 24, sessions, states)
	m = drive(m, keyRune('L'))
	noPanic(t, "evtLogView at 80×24", func() { _ = m.View() })
}

func TestLayout_TriageViewNoPanic(t *testing.T) {
	sessions, states := stdSessions()
	m := modelAtSize(80, 24, sessions, states)
	m = drive(m, keyRune('T'))
	noPanic(t, "triageView at 80×24", func() { _ = m.View() })
}

func TestLayout_EscalationViewNoPanic(t *testing.T) {
	sessions, states := stdSessions()
	m := modelAtSize(80, 24, sessions, states)
	m = drive(m, keyRune('e'))
	noPanic(t, "escView at 80×24", func() { _ = m.View() })
}

func TestLayout_IcingaViewNoPanic(t *testing.T) {
	sessions, states := stdSessions()
	m := modelAtSize(80, 24, sessions, states)
	m = drive(m, keyRune('I'))
	noPanic(t, "icingaView at 80×24", func() { _ = m.View() })
}

func TestLayout_ModalNoPanic(t *testing.T) {
	sessions, states := stdSessions()
	m := modelAtSize(80, 24, sessions, states)
	m = drive(m, keyRune('c'))
	noPanic(t, "modal at 80×24", func() { _ = m.View() })
}

func TestLayout_CmdPaletteNoPanic(t *testing.T) {
	sessions, states := stdSessions()
	m := modelAtSize(80, 24, sessions, states)
	m = drive(m, keyRune(':'))
	noPanic(t, "cmdPalette at 80×24", func() { _ = m.View() })
}

func TestLayout_SettingsNoPanic(t *testing.T) {
	sessions, states := stdSessions()
	m := modelAtSize(80, 24, sessions, states)
	m = drive(m, keyEsc())
	noPanic(t, "settings at 80×24", func() { _ = m.View() })
}

func TestLayout_CtxPickerNoPanic(t *testing.T) {
	sessions, states := stdSessions()
	m := modelAtSize(80, 24, sessions, states)
	m = drive(m, keyDown(), keyRune('C'))
	noPanic(t, "ctxPicker at 80×24", func() { _ = m.View() })
}

func TestLayout_RolePickerNoPanic(t *testing.T) {
	sessions, states := stdSessions()
	m := modelAtSize(80, 24, sessions, states)
	m = drive(m, keyRune('n'))
	noPanic(t, "rolePicker at 80×24", func() { _ = m.View() })
}

// ─── US-LY.8: help screen renders without panic ───────────────────────────

func TestLayout_HelpScreenNoPanic(t *testing.T) {
	sessions, states := stdSessions()
	m := modelAtSize(80, 24, sessions, states)
	m.helpVisible = true
	noPanic(t, "helpScreen at 80×24", func() { _ = m.View() })
}

func TestLayout_HelpScreenNoPanicAt200x50(t *testing.T) {
	sessions, states := stdSessions()
	m := modelAtSize(200, 50, sessions, states)
	m.helpVisible = true
	noPanic(t, "helpScreen at 200×50", func() { _ = m.View() })
}
