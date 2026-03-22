// TUI test module 08 — Robustness: edge cases + fuzz
//
// Covers:
//   US-Fz.1  Tiny window does not panic
//   US-Fz.2  Zero sessions, then rapid WS updates do not panic
//   US-Fz.3  Session/agent IDs shorter than 12 chars do not panic
//   US-Fz.4  Agent with nil TmuxSession handled everywhere
//   US-Fz.5  Cursor never goes out of bounds after data changes
//   US-Fz.6  Arbitrary key sequence does not panic (fuzz)

package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ─── Edge-case table tests ────────────────────────────────────────────────────

func TestRobust_TinyWindow(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	// Drive the model down to a very small terminal.
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 10, Height: 5})
	m = m2.(tuiModel)

	// Must not panic; View must return something.
	v := m.View()
	if v == "" {
		t.Error("View() returned empty string for tiny window")
	}
}

func TestRobust_ZeroSessionsThenWSUpdate(t *testing.T) {
	// Start with no sessions.
	m := newTestModel(nil, nil)

	// Inject a WS update for an unknown session.
	s := makeSession("newbie000000", "NewSession")
	m = drive(m, tuiWSUpdateMsg{
		sid: s.ID,
		state: tuiState{
			Session: s,
			Agents:  []tuiAgent{makeAgent("a1", "Dave", "worker", "idle")},
		},
	})

	// Should not panic; state stored.
	if _, ok := m.states[s.ID]; !ok {
		t.Error("state should be stored after WS update for unknown session")
	}
}

func TestRobust_ShortIDsNoIndexPanic(t *testing.T) {
	// IDs shorter than 12 chars caused a slice-out-of-bounds in viewSessionDetail
	// before makeSession() was updated to pad. This test exercises the render path.
	s := makeSession("s1", "ShortID")   // makeSession pads to ≥12
	a := makeAgent("ag1", "X", "worker", "idle")
	sessions := []tuiSession{s}
	states := map[string]tuiState{s.ID: {Session: s, Agents: []tuiAgent{a}}}
	m := newTestModel(sessions, states)

	v := m.View()
	if v == "" {
		t.Error("View() should not be empty")
	}
}

func TestRobust_NilTmuxSession(t *testing.T) {
	s := makeSession("s1", "NilTmux")
	// Agent explicitly has nil TmuxSession (zero value).
	a := tuiAgent{ID: "a1", Name: "Ghost", Role: "worker", Status: "idle", TmuxSession: nil}
	sessions := []tuiSession{s}
	states := map[string]tuiState{s.ID: {Session: s, Agents: []tuiAgent{a}}}
	m := newTestModel(sessions, states)

	// Navigate to the agent and send keys that would dereference TmuxSession.
	if !navigateToAgent(&m) {
		t.Skip("no agent item")
	}
	// 'alt+s' on no-tmux agent should produce a spawn command (not nil-deref).
	_, cmd := m.Update(keyAltRune('s'))
	if cmd == nil {
		t.Error("alt+s on nil-tmux agent should return a spawn command")
	}
}

func TestRobust_CursorBoundedAfterDataShrinks(t *testing.T) {
	// Build model with 3 sessions.
	s1 := makeSession("s1", "One")
	s2 := makeSession("s2", "Two")
	s3 := makeSession("s3", "Three")
	sessions := []tuiSession{s1, s2, s3}
	states := map[string]tuiState{
		s1.ID: {Session: s1},
		s2.ID: {Session: s2},
		s3.ID: {Session: s3},
	}
	m := newTestModel(sessions, states)

	// Move cursor to the last item.
	for range len(m.items) - 1 {
		m = drive(m, keyDown())
	}
	lastCursor := m.cursor

	// Now inject data with only 1 session — sidebar shrinks.
	m = drive(m, tuiDataMsg{
		sessions: []tuiSession{s1},
		states:   map[string]tuiState{s1.ID: {Session: s1}},
	})

	if m.cursor < 0 || m.cursor >= len(m.items) {
		t.Errorf("cursor %d out of bounds [0, %d) after data shrink (was %d)",
			m.cursor, len(m.items), lastCursor)
	}
}

func TestRobust_EmptyStatesMap(t *testing.T) {
	sessions := []tuiSession{makeSession("s1", "Stateless")}
	// states map is empty — no state for the session.
	m := newTestModel(sessions, map[string]tuiState{})

	v := m.View()
	if v == "" {
		t.Error("View() should not be empty even with no state")
	}
}

func TestRobust_RapidCollapseThenExpand(t *testing.T) {
	sessions, states := stdSessions()
	m := newTestModel(sessions, states)

	// Rapidly toggle collapse on the first session 10 times.
	for range 10 {
		m = drive(m, keyEnter())
	}
	_ = m.View() // must not panic
}

// ─── Fuzz: arbitrary key sequences ────────────────────────────────────────────

// FuzzTUIKeySequence verifies the model never panics on arbitrary input and
// always produces non-empty View() output.
func FuzzTUIKeySequence(f *testing.F) {
	// Seed corpus: representative interaction patterns.
	f.Add([]byte("jjkkjk"))
	f.Add([]byte("c\x00\n\n"))       // open modal, enter nulls
	f.Add([]byte("gq"))              // open/close goal view
	f.Add([]byte("Lq"))              // open/close event log
	f.Add([]byte("Wq"))              // open/close work queue
	f.Add([]byte("Tq"))              // open/close triage
	f.Add([]byte("\t\t\x1b"))        // tab, tab, esc
	f.Add([]byte("?\x00jkq"))        // help overlay then keys
	f.Add([]byte("ddj"))             // double-d confirm flow
	f.Add([]byte("aAlice\n\n\n\n\n")) // open agent modal, type, multi-enter

	f.Fuzz(func(t *testing.T, input []byte) {
		sessions, states := stdSessions()
		m := newTestModel(sessions, states)

		for _, b := range input {
			var msg tea.Msg
			switch {
			case b >= 0x20 && b <= 0x7e: // printable ASCII
				msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune(b)}}
			case b == '\n':
				msg = tea.KeyMsg{Type: tea.KeyEnter}
			case b == '\t':
				msg = tea.KeyMsg{Type: tea.KeyTab}
			case b == 0x1b:
				msg = tea.KeyMsg{Type: tea.KeyEsc}
			case b == 0x00:
				msg = tea.KeyMsg{Type: tea.KeyCtrlC}
			default:
				continue
			}
			m2, _ := m.Update(msg)
			m = m2.(tuiModel)
		}

		// The invariant: View must not panic and must not be empty.
		v := m.View()
		if v == "" {
			t.Error("View() returned empty string after fuzz input")
		}
	})
}
