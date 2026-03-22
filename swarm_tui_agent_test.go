// TUI test module 05 — Agent interactions
//
// User stories:
//   US-A.1  Agent item shows name and status in sidebar
//   US-A.2  alt+s on idle agent (no tmux) produces spawn command
//   US-A.3  alt+s on already-running agent shows flash "Agent already running"
//   US-A.4  D on running agent requires confirmation (first D → flash; second D → despawn)
//   US-A.5  Agent status changes via WS update are reflected in model
//   US-A.6  r on agent item triggers resume command
//   US-A.7  i on agent with tmux opens terminal pane (produces command)

package main

import (
	"testing"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// navigateToAgent navigates the cursor to the first agent item in the sidebar.
// Returns false if no agent item is found.
func navigateToAgent(m *tuiModel) bool {
	for i, it := range m.items {
		if it.kind == tuiItemAgent {
			m.cursor = i
			return true
		}
	}
	return false
}

// ─── US-A.1: Agent visible ───────────────────────────────────────────────────

func TestAgent_NameVisibleInSidebar(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	assertView(t, m, "Alice")
}

func TestAgent_StatusVisibleInSidebar(t *testing.T) {
	s := makeSession("s1", "TestSession")
	a := makeAgent("a1", "Alice", "senior-dev", "thinking")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Agents: []tuiAgent{a}},
	}
	m := newTestModel(sessions, states)

	// tuiStatusConfig maps "thinking" → "Thinking…"
	assertView(t, m, "Thinking")
}

// ─── US-A.2: Spawn idle agent ────────────────────────────────────────────────

func TestAgent_SpawnIdleAgentProducesCommand(t *testing.T) {
	s := makeSession("s1", "Spawn Test")
	// No TmuxSession — agent is idle/unspawned.
	a := makeAgent("a1", "Alice", "senior-dev", "idle")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Agents: []tuiAgent{a}},
	}
	m := newTestModel(sessions, states)

	if !navigateToAgent(&m) {
		t.Fatal("no agent item in sidebar")
	}

	_, cmd := m.Update(keyAltRune('s'))
	if cmd == nil {
		t.Error("alt+s on idle agent should return a spawn command")
	}
}

// ─── US-A.3: alt+s on already-running agent shows flash ─────────────────────

func TestAgent_SpawnRunningAgentShowsFlash(t *testing.T) {
	s := makeSession("s1", "Already Running")
	tmux := "tmux-alice"
	a := tuiAgent{ID: "a1", Name: "Alice", Role: "senior-dev", Status: "coding", TmuxSession: &tmux}
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Agents: []tuiAgent{a}},
	}
	m := newTestModel(sessions, states)

	if !navigateToAgent(&m) {
		t.Fatal("no agent item in sidebar")
	}
	m = drive(m, keyAltRune('s'))
	if m.flash == "" {
		t.Error("flash should be set when spawning already-running agent")
	}
}

// ─── US-A.4: D requires double-press for despawn ─────────────────────────────

func TestAgent_StopFirstPressShowsConfirm(t *testing.T) {
	s := makeSession("s1", "Despawn Test")
	tmux := "tmux-alice"
	a := tuiAgent{ID: "a1", Name: "Alice", Role: "senior-dev", Status: "idle", TmuxSession: &tmux}
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Agents: []tuiAgent{a}},
	}
	m := newTestModel(sessions, states)

	if !navigateToAgent(&m) {
		t.Fatal("no agent item in sidebar")
	}

	m = drive(m, keyRune('d'))

	// First press: pending confirm set, flash shows prompt.
	if m.pendingConfirm == nil {
		t.Error("first d should set pendingConfirm")
	}
	if m.flash == "" {
		t.Error("first d should set a flash confirmation prompt")
	}
}

func TestAgent_StopSecondPressConfirms(t *testing.T) {
	s := makeSession("s1", "Despawn Confirm")
	tmux := "tmux-alice"
	a := tuiAgent{ID: "a1", Name: "Alice", Role: "senior-dev", Status: "idle", TmuxSession: &tmux}
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Agents: []tuiAgent{a}},
	}
	m := newTestModel(sessions, states)

	if !navigateToAgent(&m) {
		t.Fatal("no agent item in sidebar")
	}

	// First d — sets pendingConfirm.
	m = drive(m, keyRune('d'))
	if m.pendingConfirm == nil {
		t.Fatal("first d did not set pendingConfirm")
	}

	// Second d — fires despawn command and clears pendingConfirm.
	m2, cmd := m.Update(keyRune('d'))
	mAfter := m2.(tuiModel)

	if mAfter.pendingConfirm != nil {
		t.Error("pendingConfirm should be cleared after second D")
	}
	if cmd == nil {
		t.Error("second D should return a despawn command")
	}
}

func TestAgent_OtherKeyAfterStopCancelsConfirm(t *testing.T) {
	s := makeSession("s1", "Cancel Confirm")
	tmux := "tmux-alice"
	a := tuiAgent{ID: "a1", Name: "Alice", Role: "senior-dev", Status: "idle", TmuxSession: &tmux}
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Agents: []tuiAgent{a}},
	}
	m := newTestModel(sessions, states)

	if !navigateToAgent(&m) {
		t.Fatal("no agent item in sidebar")
	}

	m = drive(m, keyRune('d'))
	m = drive(m, keyRune('j')) // cancel with j

	if m.pendingConfirm != nil {
		t.Error("pendingConfirm should be cleared by non-confirm key")
	}
}

// ─── US-A.5: WS update reflects status change ────────────────────────────────

func TestAgent_WSUpdateChangesStatus(t *testing.T) {
	s := makeSession("s1", "WS Update")
	a := makeAgent("a1", "Alice", "senior-dev", "idle")
	sessions := []tuiSession{s}
	states := map[string]tuiState{
		s.ID: {Session: s, Agents: []tuiAgent{a}},
	}
	m := newTestModel(sessions, states)

	// WS pushes updated state with new status.
	updated := makeAgent("a1", "Alice", "senior-dev", "coding")
	newState := tuiState{
		Session: s,
		Agents:  []tuiAgent{updated},
	}
	m = drive(m, tuiWSUpdateMsg{sid: s.ID, state: newState})

	// Model state should reflect the update.
	st := m.states[s.ID]
	if len(st.Agents) == 0 {
		t.Fatal("no agents in updated state")
	}
	if st.Agents[0].Status != "coding" {
		t.Errorf("agent status after WS update: want 'coding', got %q", st.Agents[0].Status)
	}
}

// ─── US-A.6: r resumes a session ─────────────────────────────────────────────

func TestAgent_RKeyOnSessionProducesResumeCommand(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	// Cursor at first item (session).
	if len(m.items) == 0 || m.items[0].kind != tuiItemSession {
		t.Fatal("first item is not a session")
	}

	_, cmd := m.Update(keyRune('r'))
	if cmd == nil {
		t.Error("r on session should return a command")
	}
}
