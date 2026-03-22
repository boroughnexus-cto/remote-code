// TUI test module 02 — Rendering
//
// User stories:
//   US-R.1  Session names appear in the sidebar
//   US-R.2  Agent names and roles appear in the sidebar
//   US-R.3  Task titles appear in the sidebar
//   US-R.4  Flash message appears in View() output
//   US-R.5  No-session empty state shows placeholder text
//   US-R.6  Status chars rendered for known statuses
//   US-R.7  Role emoji/label rendered for known roles
//   US-R.8  WS update replaces stale data in View()

package main

import (
	"strings"
	"testing"
)

func TestRender_SessionNamesInSidebar(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	assertView(t, m, "Alpha")
	assertView(t, m, "Beta")
}

func TestRender_AgentNamesInSidebar(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	assertView(t, m, "Alice")
}

func TestRender_TaskTitlesInSidebar(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	assertView(t, m, "Implement auth")
}

func TestRender_FlashMessage(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m.setFlash("Task created", false)
	assertView(t, m, "Task created")
}

func TestRender_FlashError(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	m.setFlash("something went wrong", true)
	assertView(t, m, "something went wrong")
}

func TestRender_EmptyState(t *testing.T) {
	m := newTestModel(nil, nil)
	v := stripANSI(m.View())
	// With no sessions, sidebar should be empty or show a hint.
	// At minimum the view must render without panic.
	if v == "" {
		t.Error("View() returned empty string for empty model")
	}
}

func TestRender_WSUpdateAppearsInView(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	s1id := sessions[0].ID
	// Agent starts idle — update to coding.
	newAgent := makeAgent("agent-111-aabbcc", "Alice", "senior-dev", "coding")
	newState := tuiState{
		Session: states[s1id].Session,
		Agents:  []tuiAgent{newAgent},
		Tasks:   states[s1id].Tasks,
	}
	m = drive(m, tuiWSUpdateMsg{sid: s1id, state: newState})

	// The updated agent status should be reflected somewhere in the view.
	v := stripANSI(m.View())
	if !strings.Contains(v, "Alice") {
		t.Error("Alice should still appear after WS update")
	}
	// Status in model should be updated.
	st := m.states[s1id]
	if len(st.Agents) == 0 || st.Agents[0].Status != "coding" {
		t.Errorf("agent status in model should be 'coding', got %q", func() string {
			if len(st.Agents) == 0 {
				return "(no agents)"
			}
			return st.Agents[0].Status
		}())
	}
}

func TestRender_MultipleSessionsAllVisible(t *testing.T) {
	s1 := makeSession("s1", "ProjectFoo")
	s2 := makeSession("s2", "ProjectBar")
	s3 := makeSession("s3", "ProjectBaz")
	sessions := []tuiSession{s1, s2, s3}
	states := map[string]tuiState{
		s1.ID: {Session: s1},
		s2.ID: {Session: s2},
		s3.ID: {Session: s3},
	}
	m := newTestModel(sessions, states)

	assertView(t, m, "ProjectFoo")
	assertView(t, m, "ProjectBar")
	assertView(t, m, "ProjectBaz")
}

func TestRender_StatusConfigKnownStatuses(t *testing.T) {
	cases := []struct {
		status   string
		wantLabel string
	}{
		{"coding", "Coding"},
		{"thinking", "Thinking…"},
		{"waiting", "Waiting"},
		{"stuck", "STUCK"},
		{"done", "Done"},
		{"idle", "Idle"},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			_, label, _ := tuiStatusConfig(tc.status, 0)
			if label != tc.wantLabel {
				t.Errorf("status=%q: want label %q, got %q", tc.status, tc.wantLabel, label)
			}
		})
	}
}

func TestRender_RoleConfigKnownRoles(t *testing.T) {
	knownRoles := []string{"orchestrator", "senior-dev", "qa-agent", "devops-agent", "researcher", "reviewer"}
	for _, role := range knownRoles {
		t.Run(role, func(t *testing.T) {
			emoji, color := tuiRoleConfig(role)
			if emoji == "" {
				t.Errorf("role=%q: got empty emoji", role)
			}
			if color == "" {
				t.Errorf("role=%q: got empty color", role)
			}
		})
	}
}

func TestRender_CollapsedSessionHidesChildren(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	// Collapse the first session (items[1] after Control Tower).
	if len(m.items) < 2 || m.items[1].kind != tuiItemSession {
		t.Fatal("second item is not a session")
	}
	m = drive(m, keyDown(), keyEnter())

	// Alice is in session Alpha — she should not appear in items after collapse.
	for _, it := range m.items {
		if it.eid == "agent-111" {
			t.Error("agent-111 (Alice) should not be in items after collapse")
		}
	}
}

func TestRender_GoalAppearsInGoalView(t *testing.T) {
	s := makeSession("sess-x", "GoalSession")
	g := makeGoal("goal-001", "Ship the feature", "active")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Goals: []tuiGoal{g}},
	}
	m := newTestModel(sessions, states)

	// Navigate to the session, then open goal view (g key).
	m = drive(m, keyRune('g'))

	if !m.goalView {
		t.Fatal("goalView should be true after g key")
	}
	assertView(t, m, "Ship the feature")
}
