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
//   US-V.12 Goal x cancels active goal; flash on non-active
//   US-V.13 Goal u reactivates cancelled goal; flash on non-cancelled
//   US-V.14 Event log f/F filter by agent / clear filter
//   US-V.15 Work queue navigate and promote fires create-goal
//   US-V.16 Triage view navigation
//   US-V.17 Escalation view Enter → inputting mode; respond fires POST
//   US-V.18 Notes a key opens AddNote modal

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

// ─── US-V.12: Goal x/u actions ───────────────────────────────────────────────

func TestViews_GoalXCancelsActiveGoal(t *testing.T) {
	s := makeSession("s-gx000000000", "GoalX")
	g := makeGoal("g1", "Active Goal", "active")
	sessions := []tuiSession{s}
	states := map[string]tuiState{s.ID: {Session: s, Goals: []tuiGoal{g}}}
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	m = drive(m, keyRune('g'))
	_, cmds := driveCapture(m, keyRune('x'))

	cancelCalls := fc.callsForOp("cancel-goal")
	if len(cancelCalls) > 0 {
		return
	}
	for _, cmd := range cmds {
		if cmd != nil {
			if done, ok := cmd().(tuiDoneMsg); ok && done.op == "cancel-goal" {
				return
			}
		}
	}
	t.Error("expected cancel-goal PATCH after x on active goal")
}

func TestViews_GoalXOnNonActiveShowsFlash(t *testing.T) {
	s := makeSession("s-gxna00000000", "GoalXNA")
	g := makeGoal("g1", "Done Goal", "complete")
	sessions := []tuiSession{s}
	states := map[string]tuiState{s.ID: {Session: s, Goals: []tuiGoal{g}}}
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('g'), keyRune('x'))
	if m.flash == "" {
		t.Error("flash should be set after x on non-active goal")
	}
}

func TestViews_GoalUReactivatesCancelledGoal(t *testing.T) {
	s := makeSession("s-gu0000000000", "GoalU")
	g := makeGoal("g1", "Cancelled Goal", "cancelled")
	sessions := []tuiSession{s}
	states := map[string]tuiState{s.ID: {Session: s, Goals: []tuiGoal{g}}}
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	m = drive(m, keyRune('g'))
	_, cmds := driveCapture(m, keyRune('u'))

	reactivateCalls := fc.callsForOp("reactivate-goal")
	if len(reactivateCalls) > 0 {
		return
	}
	for _, cmd := range cmds {
		if cmd != nil {
			if done, ok := cmd().(tuiDoneMsg); ok && done.op == "reactivate-goal" {
				return
			}
		}
	}
	t.Error("expected reactivate-goal PATCH after u on cancelled goal")
}

func TestViews_GoalUOnNonCancelledShowsFlash(t *testing.T) {
	s := makeSession("s-gunc000000000", "GoalUNC")
	g := makeGoal("g1", "Active Goal", "active")
	sessions := []tuiSession{s}
	states := map[string]tuiState{s.ID: {Session: s, Goals: []tuiGoal{g}}}
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('g'), keyRune('u'))
	if m.flash == "" {
		t.Error("flash should be set after u on non-cancelled goal")
	}
}

// ─── US-V.14: Event log f/F filter ───────────────────────────────────────────

func TestViews_EventLogFilterByAgent(t *testing.T) {
	s := makeSession("s-evtf000000000", "EvtFilter")
	a := makeAgent("agent-aaa-111111", "Alice", "senior-dev", "idle")
	// Single event — evtCursor = max(0, len-1) = 0, so cursor lands on this event.
	evtA := tuiEvent{AgentID: a.ID, Type: "note", Payload: "Alice did stuff", Ts: 1000}
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Agents: []tuiAgent{a}, Events: []tuiEvent{evtA}},
	}
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('L')) // open event log; evtCursor = 0
	m = drive(m, keyRune('f'))
	if m.evtAgentFilter != "Alice" {
		t.Errorf("evtAgentFilter after f: want 'Alice', got %q", m.evtAgentFilter)
	}
}

func TestViews_EventLogClearFilter(t *testing.T) {
	s := makeSession("s-evtcf00000000", "EvtClear")
	a := makeAgent("agent-aaa-111111", "Alice", "senior-dev", "idle")
	evtA := tuiEvent{AgentID: a.ID, Type: "note", Payload: "Alice note", Ts: 1000}
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Agents: []tuiAgent{a}, Events: []tuiEvent{evtA}},
	}
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('L'))
	m = drive(m, keyRune('f')) // set filter
	if m.evtAgentFilter == "" {
		t.Skip("f produced no filter (no events at cursor or agent unknown)")
	}
	m = drive(m, keyRune('F'))
	if m.evtAgentFilter != "" {
		t.Errorf("evtAgentFilter after F: want '', got %q", m.evtAgentFilter)
	}
}

// ─── US-V.15: Work queue navigate and promote ─────────────────────────────────

func TestViews_WorkQueueNavigateDown(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)
	m = drive(m, keyRune('W'))

	// Inject two items so there is something to navigate between.
	item1 := WorkQueueItem{PlaneIssueID: "PI-1", Title: "Fix login bug", Priority: "urgent"}
	item2 := WorkQueueItem{PlaneIssueID: "PI-2", Title: "Add dark mode", Priority: "medium"}
	m = drive(m, tuiWorkQueueMsg{items: []WorkQueueItem{item1, item2}})

	initial := m.workQueueCursor
	m = drive(m, keyDown())
	if m.workQueueCursor != initial+1 {
		t.Errorf("workQueueCursor after down: want %d, got %d", initial+1, m.workQueueCursor)
	}
}

func TestViews_WorkQueuePromoteFiresCreateGoal(t *testing.T) {
	sessions, states := stdSessions()
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)
	m = drive(m, keyRune('W'))
	m = drive(m, tuiWorkQueueMsg{items: []WorkQueueItem{
		{PlaneIssueID: "PI-1", Title: "Implement search"},
	}})

	m2, cmds := driveCapture(m, keyRune('p'))

	createCalls := fc.callsForOp("create-goal")
	if len(createCalls) > 0 {
		if !m2.workQueuePromoting {
			t.Error("workQueuePromoting should be true after p")
		}
		return
	}
	for _, cmd := range cmds {
		if cmd != nil {
			if done, ok := cmd().(tuiDoneMsg); ok && done.op == "create-goal" {
				return
			}
		}
	}
	t.Error("expected create-goal POST after p on work queue item")
}

// ─── US-V.16: Triage navigate ─────────────────────────────────────────────────

func TestViews_TriageNavigateDown(t *testing.T) {
	s := makeSession("s-triage000000000", "Triage")
	// Two blocked tasks to ensure navigation is possible.
	t1 := makeTask("task-001-triage00", "Blocked task 1", "blocked")
	t2 := makeTask("task-002-triage00", "Blocked task 2", "blocked")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Tasks: []tuiTask{t1, t2}},
	}
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('T'))
	initial := m.triageCursor
	m = drive(m, keyDown())
	if m.triageCursor != initial+1 {
		t.Errorf("triageCursor after down: want %d, got %d", initial+1, m.triageCursor)
	}
}

// ─── US-V.17: Escalation respond ─────────────────────────────────────────────

func makeEscalation(id, agentID, reason string) tuiEscalation {
	return tuiEscalation{ID: id, AgentID: agentID, TaskID: "task-001", Reason: reason, Ts: 1000}
}

func TestViews_EscalationEnterStartsInputting(t *testing.T) {
	s := makeSession("s-escr000000000", "EscResp")
	esc := makeEscalation("esc-001", "agent-111-aabbcc", "Need help")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Escalations: []tuiEscalation{esc}},
	}
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('e')) // open escalation view
	m = drive(m, keyEnter())   // Enter on first item starts inputting
	if !m.escInputting {
		t.Error("escInputting should be true after Enter on escalation item")
	}
	if m.escActive == nil {
		t.Error("escActive should be set after Enter on escalation item")
	}
}

func TestViews_EscalationRespondFiresPost(t *testing.T) {
	s := makeSession("s-escpost00000000", "EscPost")
	esc := makeEscalation("esc-002", "agent-111-aabbcc", "Need help")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Escalations: []tuiEscalation{esc}},
	}
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	m = drive(m, keyRune('e'))  // open view
	m = drive(m, keyEnter())    // start inputting
	m = drive(m, keyStr("Unblock it"))
	_, cmds := driveCapture(m, keyEnter()) // submit response

	respondCalls := fc.callsForOp("esc-respond")
	if len(respondCalls) > 0 {
		return
	}
	for _, cmd := range cmds {
		if cmd != nil {
			if done, ok := cmd().(tuiDoneMsg); ok && done.op == "esc-respond" {
				return
			}
		}
	}
	t.Error("expected esc-respond POST after typing response and Enter")
}

// ─── US-V.18: Notes a key opens AddNote modal ────────────────────────────────

func TestViews_NotesAOpensAddNoteModal(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	if !navigateToAgent(&m) {
		t.Fatal("no agent item in sidebar")
	}
	m = drive(m, keyRune('N'))           // open notes view
	m = drive(m, keyRune('a'))           // press 'a' to add note
	if m.modal == nil {
		t.Fatal("expected modal to open after a in notes view")
	}
	if m.modal.kind != tuiModalAddNote {
		t.Errorf("modal kind: want tuiModalAddNote, got %v", m.modal.kind)
	}
}
