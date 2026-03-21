// TUI test module 06 — Real-time updates
//
// User stories:
//   US-RT.1  WS update adds a new agent to the model
//   US-RT.2  WS update adds a new task to the model
//   US-RT.3  WS update removes a task (task no longer in updated state)
//   US-RT.4  WS update for unknown session is stored and sidebar rebuilt
//   US-RT.5  Multiple rapid WS updates for the same session converge correctly
//   US-RT.6  Events from WS update appear in session event buffer
//   US-RT.7  Goal status change via WS update is reflected in model

package main

import (
	"testing"
)

func TestRealtime_WSAddsAgent(t *testing.T) {
	s := makeSession("s1", "RT Session")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Agents: []tuiAgent{}},
	}
	m := newTestModel(sessions, states)

	// Push update with new agent.
	a := makeAgent("a-new", "Carol", "qa-agent", "idle")
	m = drive(m, tuiWSUpdateMsg{
		sid:   s.ID,
		state: tuiState{Session: s, Agents: []tuiAgent{a}},
	})

	st := m.states[s.ID]
	if len(st.Agents) != 1 {
		t.Fatalf("expected 1 agent after WS update, got %d", len(st.Agents))
	}
	if st.Agents[0].Name != "Carol" {
		t.Errorf("agent name: want 'Carol', got %q", st.Agents[0].Name)
	}
}

func TestRealtime_WSAddsTask(t *testing.T) {
	s := makeSession("s1", "RT Session")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Tasks: []tuiTask{}},
	}
	m := newTestModel(sessions, states)

	task := makeTask("t-new", "New shiny task", "spec")
	m = drive(m, tuiWSUpdateMsg{
		sid:   s.ID,
		state: tuiState{Session: s, Tasks: []tuiTask{task}},
	})

	st := m.states[s.ID]
	if len(st.Tasks) != 1 {
		t.Fatalf("expected 1 task after WS update, got %d", len(st.Tasks))
	}
	if st.Tasks[0].Title != "New shiny task" {
		t.Errorf("task title: want 'New shiny task', got %q", st.Tasks[0].Title)
	}
}

func TestRealtime_WSRemovesTask(t *testing.T) {
	s := makeSession("s1", "RT Session")
	t1 := makeTask("t1", "Task A", "spec")
	t2 := makeTask("t2", "Task B", "implement")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Tasks: []tuiTask{t1, t2}},
	}
	m := newTestModel(sessions, states)

	// Push update with only t1 — t2 deleted.
	m = drive(m, tuiWSUpdateMsg{
		sid:   s.ID,
		state: tuiState{Session: s, Tasks: []tuiTask{t1}},
	})

	st := m.states[s.ID]
	if len(st.Tasks) != 1 {
		t.Fatalf("expected 1 task after removal WS update, got %d", len(st.Tasks))
	}
	if st.Tasks[0].ID != "t1" {
		t.Errorf("remaining task should be t1, got %q", st.Tasks[0].ID)
	}
}

func TestRealtime_MultipleRapidUpdatesConverge(t *testing.T) {
	s := makeSession("s1", "Rapid Updates")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s},
	}
	m := newTestModel(sessions, states)

	// Send 5 rapid updates — each one supersedes the previous.
	for i := range 5 {
		status := []string{"idle", "thinking", "coding", "waiting", "idle"}[i]
		a := makeAgent("a1", "Alice", "senior-dev", status)
		m = drive(m, tuiWSUpdateMsg{
			sid:   s.ID,
			state: tuiState{Session: s, Agents: []tuiAgent{a}},
		})
	}

	st := m.states[s.ID]
	if len(st.Agents) == 0 {
		t.Fatal("no agents after rapid updates")
	}
	// Final status should be "idle" (last update).
	if st.Agents[0].Status != "idle" {
		t.Errorf("final agent status: want 'idle', got %q", st.Agents[0].Status)
	}
}

func TestRealtime_EventsBuffered(t *testing.T) {
	s := makeSession("s1", "Events")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s},
	}
	m := newTestModel(sessions, states)

	evts := []tuiEvent{
		{AgentID: "a1", Type: "task_moved", Payload: "spec→implement", Ts: 1000},
		{AgentID: "a1", Type: "agent_message", Payload: "hello", Ts: 1001},
	}
	m = drive(m, tuiWSUpdateMsg{
		sid:   s.ID,
		state: tuiState{Session: s, Events: evts},
	})

	if _, ok := m.vpLines[s.ID]; !ok {
		t.Error("vpLines should be set after WS update with events")
	}
}

func TestRealtime_GoalStatusChange(t *testing.T) {
	s := makeSession("s1", "Goal RT")
	g := makeGoal("g1", "Build auth", "active")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Goals: []tuiGoal{g}},
	}
	m := newTestModel(sessions, states)

	// Goal completes via WS.
	gComplete := makeGoal("g1", "Build auth", "complete")
	m = drive(m, tuiWSUpdateMsg{
		sid:   s.ID,
		state: tuiState{Session: s, Goals: []tuiGoal{gComplete}},
	})

	st := m.states[s.ID]
	if len(st.Goals) == 0 {
		t.Fatal("no goals after WS update")
	}
	if st.Goals[0].Status != "complete" {
		t.Errorf("goal status: want 'complete', got %q", st.Goals[0].Status)
	}
}

func TestRealtime_SidebarRebuiltAfterWSUpdate(t *testing.T) {
	s := makeSession("s1", "Rebuild")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Agents: []tuiAgent{}},
	}
	m := newTestModel(sessions, states)

	countBefore := len(m.items)

	// Add an agent via WS.
	a := makeAgent("a1", "Dave", "worker", "idle")
	m = drive(m, tuiWSUpdateMsg{
		sid:   s.ID,
		state: tuiState{Session: s, Agents: []tuiAgent{a}},
	})

	if len(m.items) <= countBefore {
		t.Errorf("items should grow after WS adds agent: before=%d after=%d", countBefore, len(m.items))
	}
}
