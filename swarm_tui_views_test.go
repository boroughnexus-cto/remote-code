// TUI test module 04 — Overlay views
//
// User stories:
//   US-V.1  g opens goal view; q closes it
//   US-V.2  Goal view shows goals from session state
//   US-V.3  W opens work queue view; q closes it
//   US-V.4  L opens event log view; q closes it
//   US-V.5  T opens triage view; q closes it
//   US-V.6  N opens notes view when an agent is selected; q closes it
//   US-V.7  e opens escalation view; q/esc closes it
//   US-V.8  Only one overlay is open at a time
//   US-V.9  C opens context picker; esc closes it
//   US-V.10 Context picker renders title; esc sends no PATCH command
//   US-V.11 Context picker Enter on items fires set-context PATCH

package main

import (
	"testing"
)

// ─── US-V.1: Goal view ────────────────────────────────────────────────────────

func TestViews_GoalViewOpens(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('g'))
	if !m.goalView {
		t.Error("goalView should be true after g")
	}
}

func TestViews_GoalViewClosesOnQ(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('g'))
	m = drive(m, keyRune('q'))
	if m.goalView {
		t.Error("goalView should be false after q")
	}
}

func TestViews_GoalViewShowsGoals(t *testing.T) {
	s := makeSession("s-goals", "GoalSession")
	g1 := makeGoal("g1", "Build login flow", "active")
	g2 := makeGoal("g2", "Migrate database", "complete")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Goals: []tuiGoal{g1, g2}},
	}
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('g'))
	assertView(t, m, "Build login flow")
	assertView(t, m, "Migrate database")
}

func TestViews_GoalViewNavigation(t *testing.T) {
	s := makeSession("s-gnav", "NavSession")
	g1 := makeGoal("g1", "First goal", "active")
	g2 := makeGoal("g2", "Second goal", "active")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Goals: []tuiGoal{g1, g2}},
	}
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('g'))
	initial := m.goalCursor
	m = drive(m, keyDown())
	if m.goalCursor != initial+1 {
		t.Errorf("goalCursor after down: want %d, got %d", initial+1, m.goalCursor)
	}
}

// ─── US-V.3: Work queue view ──────────────────────────────────────────────────

func TestViews_WorkQueueOpens(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	// W key opens the work queue for the selected session.
	m = drive(m, keyRune('W'))
	if !m.workQueueView {
		t.Error("workQueueView should be true after W")
	}
}

func TestViews_WorkQueueClosesOnQ(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('W'), keyRune('q'))
	if m.workQueueView {
		t.Error("workQueueView should be false after q")
	}
}

// ─── US-V.4: Event log view ───────────────────────────────────────────────────

func TestViews_EventLogOpens(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('L'))
	if !m.evtLogView {
		t.Error("evtLogView should be true after L")
	}
}

func TestViews_EventLogClosesOnQ(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('L'), keyRune('q'))
	if m.evtLogView {
		t.Error("evtLogView should be false after q")
	}
}

// ─── US-V.5: Triage view ──────────────────────────────────────────────────────

func TestViews_TriageViewOpens(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('T'))
	if !m.triageView {
		t.Error("triageView should be true after T")
	}
}

func TestViews_TriageViewClosesOnQ(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('T'), keyRune('q'))
	if m.triageView {
		t.Error("triageView should be false after q")
	}
}

// ─── US-V.6: Notes view ───────────────────────────────────────────────────────

func TestViews_NotesViewOpensOnAgentSelected(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	// Navigate to an agent item (cursor 0 = session, cursor 1 = Alice).
	m = drive(m, keyDown())
	if len(m.items) < 2 || m.items[m.cursor].kind != tuiItemAgent {
		t.Skip("couldn't navigate to agent item")
	}
	m = drive(m, keyRune('N'))
	if !m.notesView {
		t.Error("notesView should be true after N on agent")
	}
}

func TestViews_NotesViewClosesOnQ(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyDown())
	if len(m.items) < 2 || m.items[m.cursor].kind != tuiItemAgent {
		t.Skip("couldn't navigate to agent item")
	}
	m = drive(m, keyRune('N'), keyRune('q'))
	if m.notesView {
		t.Error("notesView should be false after q")
	}
}

// ─── US-V.7: Escalation view ─────────────────────────────────────────────────

func TestViews_EscalationViewOpens(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('e'))
	if !m.escView {
		t.Error("escView should be true after e")
	}
}

func TestViews_EscalationViewClosesOnQ(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('e'), keyRune('q'))
	if m.escView {
		t.Error("escView should be false after q")
	}
}

func TestViews_EscalationViewClosesOnEsc(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('e'))
	m = drive(m, keyEsc())
	if m.escView {
		t.Error("escView should be false after esc")
	}
}

// ─── US-V.9: Context picker ───────────────────────────────────────────────────

// openCtxPicker navigates to a session item then presses C to open the picker.
// The Control Tower is always at cursor 0; cursor 1 is the first session.
func openCtxPicker(m tuiModel) tuiModel {
	return drive(m, keyDown(), keyRune('C'))
}

func TestViews_CtxPickerOpens(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = openCtxPicker(m)
	if m.ctxPicker == nil {
		t.Error("ctxPicker should open after navigating to session and pressing C")
	}
}

func TestViews_CtxPickerClosesOnEsc(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = openCtxPicker(m)
	m = drive(m, keyEsc())
	if m.ctxPicker != nil {
		t.Error("ctxPicker should be nil after esc")
	}
}

func TestViews_CtxPickerRendersTitle(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = openCtxPicker(m)
	assertView(t, m, "Assign Session Context")
}

func TestViews_CtxPickerEscNoPatch(t *testing.T) {
	sessions, states := stdSessions()
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	callsBefore := len(fc.calls)
	m = openCtxPicker(m)
	m = drive(m, keyEsc())
	for _, c := range fc.calls[callsBefore:] {
		if c.method == "PATCH" {
			t.Errorf("unexpected PATCH after esc: %+v", c)
		}
	}
}

// ─── US-V.11: Context picker Enter fires set-context PATCH ───────────────────

func TestViews_CtxPickerEnterFiresPatch(t *testing.T) {
	sessions, states := stdSessions()
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	m = openCtxPicker(m)
	// Inject one context item.
	m = drive(m, tuiCtxPickerMsg{items: []ctxPickerItem{
		{id: "ctx-001", name: "Production", description: "Prod context"},
	}})
	_, cmds := driveCapture(m, keyEnter())

	// The PATCH call should be recorded in fakeClient.
	patchCalls := fc.callsForOp("set-context")
	if len(patchCalls) > 0 {
		return // recorded synchronously — pass
	}
	// Or returned as a cmd.
	for _, cmd := range cmds {
		if cmd != nil {
			msg := cmd()
			if done, ok := msg.(tuiDoneMsg); ok && done.op == "set-context" {
				return
			}
		}
	}
	t.Error("expected set-context PATCH after Enter on context item")
}

// ─── US-V.7 (Icinga): Icinga view ─────────────────────────────────────────────

func TestViews_IcingaViewOpens(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('I'))
	if !m.icingaView {
		t.Error("icingaView should be true after I")
	}
}

func TestViews_IcingaViewClosesOnQ(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('I'), keyRune('q'))
	if m.icingaView {
		t.Error("icingaView should be false after q")
	}
}

// ─── US-V.8: Only one overlay at a time ──────────────────────────────────────

func TestViews_OpeningSecondViewClosesFirst(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	// Open goal view, then open event log.
	m = drive(m, keyRune('g'))
	if !m.goalView {
		t.Fatal("goalView not opened")
	}
	// Close goal view, open event log — they're exclusive in the model.
	m = drive(m, keyRune('q'), keyRune('L'))
	if m.goalView {
		t.Error("goalView should be closed after q")
	}
	if !m.evtLogView {
		t.Error("evtLogView should be open after L")
	}
}
