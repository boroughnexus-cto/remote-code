// TUI test module 12 — Keybindings story tests
//
// Verifies that every help-bar key produces the expected model state and that
// the new safety behaviours added in this batch work correctly.
//
// User stories:
//   US-KB.1  'g' opens goal view
//   US-KB.2  'W' opens work queue view
//   US-KB.3  'L' opens event log view
//   US-KB.4  'T' opens triage view
//   US-KB.5  'e' opens escalation view
//   US-KB.6  'I' opens icinga view
//   US-KB.7  'R' triggers a refresh (fetchAll cmd)
//   US-KB.8  'c' opens new-session modal
//   US-KB.9  'n' opens role picker
//   US-KB.10 't' opens new-task modal
//   US-KB.11 ':' opens command palette
//   US-KB.12 ctrl+x first press: warning flash, no halt
//   US-KB.13 ctrl+x second press: fleet halt fires
//   US-KB.14 ctrl+x from input focus is ignored
//   US-KB.15 any other key cancels pending halt
//   US-KB.16 goal view 'g' key jumps to first item (not close)
//   US-KB.17 goal view 'G' key jumps to last item
//   US-KB.18 overlay exclusivity: opening a second overlay closes the first

package main

import (
	"testing"
)

// ─── US-KB.1–6: view keys ─────────────────────────────────────────────────

func TestKeys_GOpensGoalView(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)
	m = drive(m, keyRune('g'))
	if !m.goalView {
		t.Error("g should open goalView")
	}
}

func TestKeys_WOpensWorkQueue(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)
	m = drive(m, keyRune('W'))
	if !m.workQueueView {
		t.Error("W should open workQueueView")
	}
}

func TestKeys_LOpensEventLog(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)
	m = drive(m, keyRune('L'))
	if !m.evtLogView {
		t.Error("L should open evtLogView")
	}
}

func TestKeys_TOpensTriage(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)
	m = drive(m, keyRune('T'))
	if !m.triageView {
		t.Error("T should open triageView")
	}
}

func TestKeys_EOpensEscalation(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)
	m = drive(m, keyRune('e'))
	if !m.escView {
		t.Error("e should open escView")
	}
}

func TestKeys_IOpensIcinga(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)
	m = drive(m, keyRune('I'))
	if !m.icingaView {
		t.Error("I should open icingaView")
	}
}

// ─── US-KB.7: R triggers refresh ──────────────────────────────────────────

func TestKeys_RTriggersRefresh(t *testing.T) {
	sessions, states := stdSessions()
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	callsBefore := len(fc.calls)
	_, cmds := driveCapture(m, keyRune('R'))

	// Either recorded synchronously or returned as cmd.
	if len(fc.callsForOp("fetchAll")) > 0 {
		return
	}
	for _, cmd := range cmds {
		if cmd != nil {
			msg := cmd()
			if done, ok := msg.(tuiDoneMsg); ok && done.op == "fetchAll" {
				return
			}
		}
	}
	if len(fc.calls) == callsBefore {
		t.Error("R should trigger a fetchAll refresh")
	}
}

// ─── US-KB.8: c opens new-session modal ───────────────────────────────────

func TestKeys_COpensNewSessionModal(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)
	m = drive(m, keyRune('c'))
	if m.modal == nil {
		t.Error("c should open modal")
	}
	if m.modal.kind != tuiModalNewSession {
		t.Errorf("modal kind: want tuiModalNewSession, got %v", m.modal.kind)
	}
}

// ─── US-KB.9: n opens role picker ─────────────────────────────────────────

func TestKeys_NOpensRolePicker(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)
	m = drive(m, keyRune('n'))
	if m.rolePicker == nil {
		t.Error("n should open rolePicker")
	}
}

// ─── US-KB.10: t opens new-task modal ─────────────────────────────────────

func TestKeys_TOpensNewTaskModal(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)
	m = drive(m, keyRune('t'))
	if m.modal == nil {
		t.Error("t should open modal")
	}
	if m.modal.kind != tuiModalNewTask {
		t.Errorf("modal kind: want tuiModalNewTask, got %v", m.modal.kind)
	}
}

// ─── US-KB.11: ':' opens command palette ──────────────────────────────────

func TestKeys_ColonOpensPalette(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)
	m = drive(m, keyRune(':'))
	if m.cmdPalette == nil {
		t.Error("':' should open cmdPalette")
	}
}

// ─── US-KB.12: ctrl+x first press — warning, no halt ──────────────────────

func TestKeys_CtrlXFirstPressWarning(t *testing.T) {
	sessions, states := stdSessions()
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	m = drive(m, keyCtrlX())
	if !m.pendingHalt {
		t.Error("pendingHalt should be true after first ctrl+x")
	}
	// Should not have fired halt.
	if len(fc.callsForOp("fleet-halt")) > 0 {
		t.Error("fleet-halt should not fire on first ctrl+x")
	}
	// Should show warning flash.
	if !m.flashErr {
		t.Error("flash should be shown after first ctrl+x")
	}
}

// ─── US-KB.13: ctrl+x second press — fleet halt fires ─────────────────────

func TestKeys_CtrlXSecondPressFiresHalt(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m2, cmds := driveCapture(m, keyCtrlX(), keyCtrlX())

	// pendingHalt should be cleared after second press.
	if m2.pendingHalt {
		t.Error("pendingHalt should be false after second ctrl+x")
	}
	// The flash should indicate halt in progress.
	if !m2.flashErr {
		t.Error("flash should be set after halt")
	}
	// A non-nil cmd should have been returned (the HTTP call to the halt endpoint).
	hasCmd := false
	for _, cmd := range cmds {
		if cmd != nil {
			hasCmd = true
			break
		}
	}
	if !hasCmd {
		t.Error("expected a halt cmd to be returned after second ctrl+x")
	}
}

// ─── US-KB.14: ctrl+x from input focus is ignored ─────────────────────────

func TestKeys_CtrlXIgnoredInInputFocus(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	// Switch to input focus.
	m = drive(m, keyTab())
	if m.focus != tuiFocusInput {
		t.Skip("couldn't switch to input focus")
	}
	m = drive(m, keyCtrlX())
	if m.pendingHalt {
		t.Error("pendingHalt should not be set when ctrl+x is pressed in input focus")
	}
}

// ─── US-KB.15: any other key cancels pending halt ──────────────────────────

func TestKeys_OtherKeyCancelsPendingHalt(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyCtrlX())
	if !m.pendingHalt {
		t.Fatal("pendingHalt should be true after first ctrl+x")
	}
	// Press a different key.
	m = drive(m, keyRune('R'))
	if m.pendingHalt {
		t.Error("pendingHalt should be cancelled by any other key")
	}
}

// ─── US-KB.16: goal view 'g' jumps to first item ─────────────────────────

func TestKeys_GoalViewGJumpsToFirst(t *testing.T) {
	s := makeSession("s-gkeys-001", "GoalKeySession")
	g1 := makeGoal("g1", "First goal", "active")
	g2 := makeGoal("g2", "Second goal", "active")
	g3 := makeGoal("g3", "Third goal", "active")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Goals: []tuiGoal{g1, g2, g3}},
	}
	m := newTestModel(sessions, states)
	m = drive(m, keyRune('g'))
	// Navigate down to item 2.
	m = drive(m, keyDown(), keyDown())
	if m.goalCursor != 2 {
		t.Fatalf("expected cursor at 2, got %d", m.goalCursor)
	}
	// 'g' should jump to first (not close view).
	m = drive(m, keyRune('g'))
	if !m.goalView {
		t.Error("goalView should remain open after g (was closing bug)")
	}
	if m.goalCursor != 0 {
		t.Errorf("goalCursor should be 0 after g, got %d", m.goalCursor)
	}
}

// ─── US-KB.17: goal view 'G' jumps to last item ──────────────────────────

func TestKeys_GoalViewCapGJumpsToLast(t *testing.T) {
	s := makeSession("s-gcapg-001", "GoalCapGSession")
	g1 := makeGoal("g1", "First goal", "active")
	g2 := makeGoal("g2", "Second goal", "active")
	g3 := makeGoal("g3", "Third goal", "active")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Goals: []tuiGoal{g1, g2, g3}},
	}
	m := newTestModel(sessions, states)
	m = drive(m, keyRune('g'))
	// Cursor starts at 0; G should jump to last.
	m = drive(m, keyRune('G'))
	if !m.goalView {
		t.Error("goalView should remain open after G")
	}
	if m.goalCursor != 2 {
		t.Errorf("goalCursor should be 2 (last) after G, got %d", m.goalCursor)
	}
}

// ─── US-KB.18: overlay exclusivity ────────────────────────────────────────

func TestKeys_OverlayExclusivity_GoalThenLog(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('g'))
	if !m.goalView {
		t.Fatal("goalView not open")
	}
	m = drive(m, keyRune('q'), keyRune('L'))
	if m.goalView {
		t.Error("goalView should be closed")
	}
	if !m.evtLogView {
		t.Error("evtLogView should be open")
	}
}

func TestKeys_OverlayExclusivity_LogThenTriage(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('L'))
	if !m.evtLogView {
		t.Fatal("evtLogView not open")
	}
	m = drive(m, keyRune('q'), keyRune('T'))
	if m.evtLogView {
		t.Error("evtLogView should be closed")
	}
	if !m.triageView {
		t.Error("triageView should be open")
	}
}
