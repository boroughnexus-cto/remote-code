// TUI test module 08 — Role Picker story tests
//
// User stories:
//   US-RP.1  n opens role picker; no session selected → no-op
//   US-RP.2  n opens role picker when a session is selected
//   US-RP.3  Role picker renders title and Custom… option
//   US-RP.4  esc/q closes role picker without opening modal
//   US-RP.5  Enter on Custom… (no items) opens new-agent modal with blank role
//   US-RP.6  up/down navigates role picker cursor
//   US-RP.7  Enter on a role item pre-fills modal Role field
//   US-RP.8  Role picker replaced by modal after selection; focus on modal
//   US-RP.9  Post-session: after ctx picker closes, role picker auto-opens
//   US-RP.10 n does nothing when cursor is on session item (not a session either)

package main

import (
	"testing"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// injectRoles injects a set of role names into an open role picker via msg.
func injectRoles(m tuiModel, roles ...string) tuiModel {
	items := make([]rolePickerItem, 0, len(roles))
	for _, r := range roles {
		items = append(items, rolePickerItem{role: r, preview: "preview of " + r})
	}
	return drive(m, tuiRolePickerMsg{items: items})
}

// ─── US-RP.1: n without session selected does nothing ────────────────────────

func TestRolePicker_NKeyNoSessionsIsNoop(t *testing.T) {
	// Empty model — no sessions.
	m := newTestModel(nil, nil)

	m = drive(m, keyRune('n'))
	if m.rolePicker != nil {
		t.Error("rolePicker should remain nil when no sessions exist")
	}
}

// ─── US-RP.2: n with session selected opens role picker ──────────────────────

func TestRolePicker_NKeyOpensPicker(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('n'))
	if m.rolePicker == nil {
		t.Fatal("expected rolePicker to open after 'n'")
	}
}

// ─── US-RP.3: role picker renders title and Custom… ──────────────────────────

func TestRolePicker_RendersTitle(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('n'))
	assertView(t, m, "Select Agent Role")
}

func TestRolePicker_RendersCustomOption(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('n'))
	assertView(t, m, "Custom")
}

// ─── US-RP.4: esc/q cancels role picker ──────────────────────────────────────

func TestRolePicker_EscCloses(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('n'))
	if m.rolePicker == nil {
		t.Fatal("role picker should be open")
	}
	m = drive(m, keyEsc())
	if m.rolePicker != nil {
		t.Error("rolePicker should be nil after esc")
	}
	if m.modal != nil {
		t.Error("modal should not open after esc")
	}
}

func TestRolePicker_QCloses(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('n'), keyRune('q'))
	if m.rolePicker != nil {
		t.Error("rolePicker should be nil after q")
	}
	if m.modal != nil {
		t.Error("modal should not open after q")
	}
}

// ─── US-RP.5: Enter on Custom… opens new-agent modal with blank role ─────────

func TestRolePicker_EnterCustomOpensModalBlankRole(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	// Open picker with no items (fakeClient returns []).
	m = drive(m, keyRune('n'))
	if m.rolePicker == nil {
		t.Fatal("role picker should be open")
	}
	// Cursor defaults to 0; with no items, cursor >= len(items), so Custom… is selected.
	m = drive(m, keyEnter())

	if m.rolePicker != nil {
		t.Error("rolePicker should be nil after Enter")
	}
	if m.modal == nil {
		t.Fatal("modal should open after Enter")
	}
	if m.modal.kind != tuiModalNewAgent {
		t.Errorf("modal kind: want tuiModalNewAgent, got %v", m.modal.kind)
	}
	if m.focus != tuiFocusModal {
		t.Errorf("focus should be tuiFocusModal, got %v", m.focus)
	}
	// Role field (index 1) should be blank for Custom…
	roleVal := m.modal.fields[1].ti.Value()
	if roleVal != "" {
		t.Errorf("role field should be blank for Custom…, got %q", roleVal)
	}
}

// ─── US-RP.6: up/down navigates cursor ───────────────────────────────────────

func TestRolePicker_DownMovesDown(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('n'))
	m = injectRoles(m, "senior-dev", "qa-agent")
	initial := m.rolePicker.cursor
	m = drive(m, keyDown())
	if m.rolePicker.cursor != initial+1 {
		t.Errorf("cursor after down: want %d, got %d", initial+1, m.rolePicker.cursor)
	}
}

func TestRolePicker_UpDoesNotGoNegative(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('n'))
	m = drive(m, keyUp()) // cursor was 0 → should stay 0
	if m.rolePicker == nil {
		t.Fatal("role picker should still be open")
	}
	if m.rolePicker.cursor < 0 {
		t.Errorf("cursor went negative: %d", m.rolePicker.cursor)
	}
}

func TestRolePicker_DownClampsAtBottom(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('n'))
	m = injectRoles(m, "dev") // 1 role + Custom… = 2 items total (indices 0 and 1)
	// Move to bottom (Custom… = index 1) and try to go further.
	m = drive(m, keyDown(), keyDown(), keyDown())
	total := len(m.rolePicker.items) + 1 // +1 for Custom…
	if m.rolePicker.cursor >= total {
		t.Errorf("cursor out of bounds: %d >= %d", m.rolePicker.cursor, total)
	}
}

// ─── US-RP.7: Enter on role item pre-fills Role field ────────────────────────

func TestRolePicker_EnterRolePreFillsModal(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('n'))
	m = injectRoles(m, "senior-dev", "qa-agent")

	// Cursor is at 0 ("senior-dev"). Press Enter to select it.
	m = drive(m, keyEnter())

	if m.modal == nil {
		t.Fatal("modal should open after Enter on role item")
	}
	roleVal := m.modal.fields[1].ti.Value()
	if roleVal != "senior-dev" {
		t.Errorf("role field: want %q, got %q", "senior-dev", roleVal)
	}
}

func TestRolePicker_EnterSecondRolePreFills(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('n'))
	m = injectRoles(m, "senior-dev", "qa-agent")
	m = drive(m, keyDown()) // move to qa-agent
	m = drive(m, keyEnter())

	if m.modal == nil {
		t.Fatal("modal should open")
	}
	roleVal := m.modal.fields[1].ti.Value()
	if roleVal != "qa-agent" {
		t.Errorf("role field: want %q, got %q", "qa-agent", roleVal)
	}
}

// ─── US-RP.8: After selection modal is open, picker is gone ──────────────────

func TestRolePicker_AfterSelectionPickerClosed(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('n'))
	m = injectRoles(m, "dev")
	m = drive(m, keyEnter())

	if m.rolePicker != nil {
		t.Error("rolePicker should be nil after selection")
	}
	if m.modal == nil {
		t.Error("modal should be open after selection")
	}
}

// ─── US-RP.9: Post-session: ctx picker close triggers role picker ─────────────

func TestRolePicker_PostSessionFlowAutoOpens(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	// Simulate the post-session flow: postSessionSID is set and ctxPicker is open.
	sid := sessions[0].ID
	m.postSessionSID = sid
	m.ctxPicker = newCtxPicker(sid)

	// Closing ctx picker (esc) should trigger role picker.
	m = drive(m, keyEsc())

	if m.ctxPicker != nil {
		t.Error("ctxPicker should be nil after esc")
	}
	if m.rolePicker == nil {
		t.Error("rolePicker should auto-open after ctx picker closes in post-session flow")
	}
	if m.rolePicker.sid != sid {
		t.Errorf("rolePicker sid: want %q, got %q", sid, m.rolePicker.sid)
	}
	if !m.rolePicker.isPostSession {
		t.Error("rolePicker.isPostSession should be true in post-session flow")
	}
	if m.postSessionSID != "" {
		t.Error("postSessionSID should be cleared after role picker opens")
	}
}

// ─── US-RP.10: Role picker renders role names after injection ─────────────────

func TestRolePicker_RendersInjectedRoles(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m = drive(m, keyRune('n'))
	m = injectRoles(m, "senior-dev", "qa-agent", "devops")

	assertView(t, m, "senior-dev")
	assertView(t, m, "qa-agent")
	assertView(t, m, "devops")
}
