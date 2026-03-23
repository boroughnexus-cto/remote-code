// TUI test module 07 — Modal story tests
//
// User stories:
//   US-M.1  c opens new-session modal; esc cancels without side effects
//   US-M.2  New-session modal submit fires create-session command
//   US-M.3  Empty name field blocks submit (no command returned)
//   US-M.4  n opens new-agent modal; requires a session selected
//   US-M.5  New-agent modal submit fires create-agent command
//   US-M.6  Server error response sets flash error on model
//   US-M.7  Successful create-session cycles through tuiDoneMsg flash
//   US-M.8  Tab advances field focus in multi-field modals
//   US-M.9  t opens new-task modal; submit fires create-task command

package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// openModal opens a modal by pressing key, then verifies modal is active.
func openModal(t *testing.T, m tuiModel, key rune) tuiModel {
	t.Helper()
	m = drive(m, keyRune(key))
	if m.modal == nil {
		t.Fatalf("expected modal to open after %q, got nil", key)
	}
	if m.focus != tuiFocusModal {
		t.Fatalf("focus should be tuiFocusModal after %q, got %v", key, m.focus)
	}
	return m
}

// openAgentModal presses 'n' (opens role picker) then Enter (selects Custom…)
// to open the new-agent modal. Use this instead of openModal(t, m, 'n') since
// 'n' now routes through the role picker before opening the modal.
func openAgentModal(t *testing.T, m tuiModel) tuiModel {
	t.Helper()
	m = drive(m, keyRune('n'))
	if m.rolePicker == nil {
		t.Fatal("expected role picker to open after 'n'")
	}
	// Press Enter with empty items list → Custom… → opens modal
	m = drive(m, keyEnter())
	if m.modal == nil {
		t.Fatalf("expected modal to open after Enter in role picker, got nil")
	}
	if m.focus != tuiFocusModal {
		t.Fatalf("focus should be tuiFocusModal, got %v", m.focus)
	}
	return m
}

// typeInModal sends a string to the currently focused modal field.
func typeInModal(m tuiModel, text string) tuiModel {
	return drive(m, keyStr(text))
}

// submitModal presses Enter on the last field to trigger submission.
// For modals with multiple fields, press Enter once per additional field to
// advance, then once more on the final field.
func submitModal(m tuiModel, advanceCount int) (tuiModel, tea.Cmd) {
	for range advanceCount {
		m = drive(m, keyEnter())
	}
	m2, cmd := m.Update(keyEnter())
	return m2.(tuiModel), cmd
}

// ─── US-M.1: c opens new-session modal ───────────────────────────────────────

func TestModal_NewSessionOpens(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = openModal(t, m, 'c')
	if m.modal.kind != tuiModalNewSession {
		t.Errorf("modal kind: want tuiModalNewSession, got %v", m.modal.kind)
	}
}

func TestModal_EscCancelsModal(t *testing.T) {
	sessions, states := stdSessions()
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	callsBefore := len(fc.calls)
	m = openModal(t, m, 'c')
	m = drive(m, keyEsc())

	if m.modal != nil {
		t.Error("modal should be nil after esc")
	}
	if m.focus != tuiFocusSidebar {
		t.Error("focus should return to sidebar after esc")
	}
	// Esc must not have triggered any POST calls.
	for _, c := range fc.calls[callsBefore:] {
		if c.method == "POST" {
			t.Errorf("unexpected POST after esc: %+v", c)
		}
	}
}

// ─── US-M.2: New-session submit fires create-session ─────────────────────────

func TestModal_NewSessionSubmitFiresCommand(t *testing.T) {
	sessions, states := stdSessions()
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	m = openModal(t, m, 'c')
	m = typeInModal(m, "My New Session")

	// New-session has 2 fields (Name, Template).
	// First Enter advances to Template; second Enter submits.
	_, cmd := submitModal(m, 1)
	if cmd == nil {
		t.Fatal("submit should return a non-nil command")
	}

	// Execute the cmd — it should call fakeClient.post("create-session", ...)
	msg := cmd()
	done, ok := msg.(tuiDoneMsg)
	if !ok {
		t.Fatalf("expected tuiDoneMsg, got %T: %v", msg, msg)
	}
	if done.op != "create-session" {
		t.Errorf("done.op: want 'create-session', got %q", done.op)
	}

	calls := fc.callsForOp("create-session")
	if len(calls) == 0 {
		t.Error("fakeClient should have recorded a create-session call")
	}
}

// ─── US-M.3: Empty name blocks submit ────────────────────────────────────────

func TestModal_EmptyNameBlocksSubmit(t *testing.T) {
	sessions, states := stdSessions()
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	// Open modal but don't type anything.
	m = openModal(t, m, 'c')

	// Advance to Template field then submit.
	_, cmd := submitModal(m, 1)

	// submitModal returns a non-nil cmd from the batch (textarea focus cmd),
	// but fakeClient should NOT have recorded a create-session call.
	calls := fc.callsForOp("create-session")
	if len(calls) > 0 {
		t.Error("create-session should not fire with empty name")
	}
	_ = cmd // don't care about focus cmds
}

// ─── US-M.4: n opens role picker then new-agent modal ────────────────────────

func TestModal_NewAgentOpens(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	// Cursor starts at the first session — valid context for new agent.
	m = openAgentModal(t, m)
	if m.modal.kind != tuiModalNewAgent {
		t.Errorf("modal kind: want tuiModalNewAgent, got %v", m.modal.kind)
	}
}

// ─── US-M.5: New-agent modal submit fires create-agent ───────────────────────

func TestModal_NewAgentSubmitFiresCommand(t *testing.T) {
	sessions, states := stdSessions()
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	m = openAgentModal(t, m)
	m = typeInModal(m, "Charlie")

	// New-agent has 5 fields (Name, Role, Mission, Project, RepoPath).
	// Advance through fields 2-5, then submit on the last.
	_, cmd := submitModal(m, 4)
	if cmd == nil {
		t.Fatal("submit should return a non-nil command")
	}

	msg := cmd()
	done, ok := msg.(tuiDoneMsg)
	if !ok {
		t.Fatalf("expected tuiDoneMsg, got %T", msg)
	}
	if done.op != "create-agent" {
		t.Errorf("done.op: want 'create-agent', got %q", done.op)
	}

	calls := fc.callsForOp("create-agent")
	if len(calls) == 0 {
		t.Error("fakeClient should have recorded a create-agent call")
	}
}

// ─── US-M.6: Server error response sets flash ────────────────────────────────

func TestModal_ServerErrorSetsFlash(t *testing.T) {
	sessions, states := stdSessions()
	fc := newFakeClient()
	// Configure next post to return a server error.
	fc.nextResult = func(op string) tea.Cmd {
		return func() tea.Msg {
			return tuiErrMsg{op: op, text: "name already taken"}
		}
	}
	m := newTestModelWithClient(sessions, states, fc)

	m = openModal(t, m, 'c')
	m = typeInModal(m, "DuplicateSession")
	_, cmd := submitModal(m, 1)
	if cmd == nil {
		t.Fatal("expected non-nil command")
	}

	// Execute cmd and feed result back into model.
	m = drive(m, cmd())

	if !m.flashErr {
		t.Error("flashErr should be true after server error")
	}
	if !strings.Contains(m.flash, "name already taken") {
		t.Errorf("flash should contain error text, got %q", m.flash)
	}
}

// ─── US-M.7: Successful create-session cycles through DoneMsg flash ───────────

func TestModal_SuccessfulCreateSessionFlash(t *testing.T) {
	sessions, states := stdSessions()
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	m = openModal(t, m, 'c')
	m = typeInModal(m, "MySession")
	_, cmd := submitModal(m, 1)
	if cmd == nil {
		t.Fatal("expected non-nil command")
	}

	// Execute cmd and feed tuiDoneMsg back.
	msg := cmd()
	m = drive(m, msg)

	if m.flashErr {
		t.Error("flashErr should be false after success")
	}
	if m.flash == "" {
		t.Error("flash should be set after successful create-session")
	}
}

// ─── US-M.8: Tab advances field focus in multi-field modal ───────────────────

func TestModal_TabAdvancesField(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = openAgentModal(t, m) // role picker → 5-field new-agent modal
	initialCursor := m.modal.cursor // should be 0

	m = drive(m, keyTab())
	if m.modal == nil {
		t.Fatal("modal should still be open after tab")
	}
	if m.modal.cursor != initialCursor+1 {
		t.Errorf("modal cursor after tab: want %d, got %d", initialCursor+1, m.modal.cursor)
	}
}

// ─── US-M.9: t opens new-task modal; submit fires create-task ────────────────

func TestModal_NewTaskSubmitFiresCommand(t *testing.T) {
	sessions, states := stdSessions()
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	m = openModal(t, m, 't')
	if m.modal.kind != tuiModalNewTask {
		t.Errorf("modal kind: want tuiModalNewTask, got %v", m.modal.kind)
	}

	m = typeInModal(m, "Fix the flaky test")

	// New-task has 3 fields (Title, Description, Project).
	_, cmd := submitModal(m, 2)
	if cmd == nil {
		t.Fatal("submit should return a non-nil command")
	}

	msg := cmd()
	done, ok := msg.(tuiDoneMsg)
	if !ok {
		t.Fatalf("expected tuiDoneMsg, got %T", msg)
	}
	if done.op != "create-task" {
		t.Errorf("done.op: want 'create-task', got %q", done.op)
	}
}

// ─── US-M.10: E key opens edit modals ────────────────────────────────────────

// navigateToTask sets the cursor to the first task item in the sidebar.
func navigateToTask(m *tuiModel) bool {
	for i, it := range m.items {
		if it.kind == tuiItemTask {
			m.cursor = i
			return true
		}
	}
	return false
}

func TestModal_EOnSessionOpensEditSession(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyDown()) // cursor → first session (Alpha)
	m = drive(m, keyRune('E'))
	if m.modal == nil {
		t.Fatal("expected modal after E on session")
	}
	if m.modal.kind != tuiModalEditSession {
		t.Errorf("modal kind: want tuiModalEditSession, got %v", m.modal.kind)
	}
}

func TestModal_EOnAgentOpensEditAgent(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	if !navigateToAgent(&m) {
		t.Fatal("no agent item in sidebar")
	}
	m = drive(m, keyRune('E'))
	if m.modal == nil {
		t.Fatal("expected modal after E on agent")
	}
	if m.modal.kind != tuiModalEditAgent {
		t.Errorf("modal kind: want tuiModalEditAgent, got %v", m.modal.kind)
	}
}

func TestModal_EOnTaskOpensEditTask(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	if !navigateToTask(&m) {
		t.Fatal("no task item in sidebar")
	}
	m = drive(m, keyRune('E'))
	if m.modal == nil {
		t.Fatal("expected modal after E on task")
	}
	if m.modal.kind != tuiModalEditTask {
		t.Errorf("modal kind: want tuiModalEditTask, got %v", m.modal.kind)
	}
}

// ─── US-M.11: Edit session submit fires PATCH ─────────────────────────────────

func TestModal_EditSessionSubmitFiresPatch(t *testing.T) {
	sessions, states := stdSessions()
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	m = drive(m, keyDown())    // cursor → first session
	m = drive(m, keyRune('E')) // open EditSession modal (1 field, pre-filled "Alpha")

	// Field is pre-filled; Enter submits directly (1-field modal).
	_, cmd := m.Update(keyEnter())
	if cmd == nil {
		t.Fatal("expected command from edit-session submit")
	}
	msg := cmd()
	done, ok := msg.(tuiDoneMsg)
	if !ok {
		t.Fatalf("expected tuiDoneMsg, got %T", msg)
	}
	if done.op != "edit-session" {
		t.Errorf("done.op: want 'edit-session', got %q", done.op)
	}
}

// ─── US-M.12: S on task opens SetStage modal ──────────────────────────────────

func TestModal_SOnTaskOpensEditTaskStage(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	if !navigateToTask(&m) {
		t.Fatal("no task item in sidebar")
	}
	m = drive(m, keyRune('S'))
	if m.modal == nil {
		t.Fatal("expected modal after S on task")
	}
	if m.modal.kind != tuiModalEditTaskStage {
		t.Errorf("modal kind: want tuiModalEditTaskStage, got %v", m.modal.kind)
	}
}

func TestModal_EditTaskStageSubmitFiresPatch(t *testing.T) {
	sessions, states := stdSessions()
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	if !navigateToTask(&m) {
		t.Fatal("no task item in sidebar")
	}
	m = drive(m, keyRune('S')) // open SetStage modal (1 field pre-filled with current stage)
	// Clear pre-filled value and type a new stage.
	m = drive(m, keyStr("done"))

	_, cmd := m.Update(keyEnter())
	if cmd == nil {
		t.Fatal("expected command from edit-task-stage submit")
	}
	msg := cmd()
	done, ok := msg.(tuiDoneMsg)
	if !ok {
		t.Fatalf("expected tuiDoneMsg, got %T", msg)
	}
	if done.op != "edit-task-stage" {
		t.Errorf("done.op: want 'edit-task-stage', got %q", done.op)
	}
}

// ─── US-M.13: + opens quick-agent modal ──────────────────────────────────────

func TestModal_PlusOpensQuickAgent(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('+'))
	if m.modal == nil {
		t.Fatal("expected modal after +")
	}
	if m.modal.kind != tuiModalQuickAgent {
		t.Errorf("modal kind: want tuiModalQuickAgent, got %v", m.modal.kind)
	}
}

func TestModal_QuickAgentSubmitFiresCreateAgent(t *testing.T) {
	sessions, states := stdSessions()
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	m = drive(m, keyRune('+'))
	m = typeInModal(m, "Danika")

	_, cmd := m.Update(keyEnter())
	if cmd == nil {
		t.Fatal("expected command from quick-agent submit")
	}
	msg := cmd()
	done, ok := msg.(tuiDoneMsg)
	if !ok {
		t.Fatalf("expected tuiDoneMsg, got %T", msg)
	}
	if done.op != "create-agent" {
		t.Errorf("done.op: want 'create-agent', got %q", done.op)
	}
	calls := fc.callsForOp("create-agent")
	if len(calls) == 0 {
		t.Error("fakeClient should have recorded a create-agent call")
	}
}

// ─── US-M.14: X on session opens typed-confirm modal ─────────────────────────

func TestModal_XOnSessionOpensConfirmModal(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyDown()) // cursor → first session (Alpha)
	m = drive(m, keyRune('X'))
	if m.modal == nil {
		t.Fatal("expected modal after X on session")
	}
	if m.modal.kind != tuiModalConfirmTyped {
		t.Errorf("modal kind: want tuiModalConfirmTyped, got %v", m.modal.kind)
	}
	if m.pendingConfirm == nil {
		t.Error("pendingConfirm should be set after X on session")
	}
}

func TestModal_XConfirmWrongNameShowsError(t *testing.T) {
	sessions, states := stdSessions()
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	m = drive(m, keyDown(), keyRune('X')) // open confirm modal for "Alpha"
	m = typeInModal(m, "wrong-name")

	callsBefore := len(fc.calls)
	m = drive(m, keyEnter())

	// Modal should stay open (validator failed) and show error.
	if m.modal == nil {
		t.Error("modal should stay open after wrong name")
	}
	if m.modal.err == "" {
		t.Error("modal.err should be set after wrong name")
	}
	for _, c := range fc.calls[callsBefore:] {
		if c.method == "DELETE" {
			t.Errorf("should not DELETE with wrong name: %+v", c)
		}
	}
}

func TestModal_XConfirmCorrectNameFiresDelete(t *testing.T) {
	sessions, states := stdSessions()
	fc := newFakeClient()
	m := newTestModelWithClient(sessions, states, fc)

	m = drive(m, keyDown(), keyRune('X')) // open confirm modal for "Alpha"
	m = typeInModal(m, "Alpha")

	_, cmd := m.Update(keyEnter())

	// Validator passes → submitModal fires pendingConfirm.onConfirm.
	deleteCalls := fc.callsForOp("delete-session")
	if len(deleteCalls) > 0 {
		return
	}
	if cmd != nil {
		msg := cmd()
		if done, ok := msg.(tuiDoneMsg); ok && done.op == "delete-session" {
			return
		}
	}
	t.Error("expected delete-session after correct name confirm")
}
