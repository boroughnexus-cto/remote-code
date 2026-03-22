// Package main — TUI test harness helpers.
//
// Strategy:
//   - Drive the bubbletea model directly (no terminal, no network).
//   - Inject state via tuiDataMsg / tuiWSUpdateMsg (the same msgs the real client sends).
//   - Send key events via tea.KeyMsg.
//   - Assert on model fields (cursor, focus, flash…) and stripped View() output.
//
// Each test is isolated: it creates a fresh model and data, exercises one
// behaviour, and makes a tight assertion.  No shared global state.

package main

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// ─── fakeClient ───────────────────────────────────────────────────────────────

// fakeCall records a single call made to fakeClient.
type fakeCall struct {
	method string
	op     string
	path   string
	body   interface{}
}

// fakeClient implements TUIClient without touching the network.
// It records every call; callers can inspect calls to verify intent.
type fakeClient struct {
	calls   []fakeCall
	// nextResult, if set, is returned by the next cmd-returning call.
	// After use it is cleared. Defaults to tuiDoneMsg.
	nextResult func(op string) tea.Cmd
}

func newFakeClient() *fakeClient { return &fakeClient{} }

func (f *fakeClient) record(method, op, path string, body interface{}) tea.Cmd {
	f.calls = append(f.calls, fakeCall{method: method, op: op, path: path, body: body})
	if f.nextResult != nil {
		fn := f.nextResult
		f.nextResult = nil
		return fn(op)
	}
	return func() tea.Msg { return tuiDoneMsg{op: op} }
}

// LastCall returns the most recent recorded call, or zero value if none.
func (f *fakeClient) lastCall() fakeCall {
	if len(f.calls) == 0 {
		return fakeCall{}
	}
	return f.calls[len(f.calls)-1]
}

// CallsForOp returns all calls with a matching op.
func (f *fakeClient) callsForOp(op string) []fakeCall {
	var out []fakeCall
	for _, c := range f.calls {
		if c.op == op {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeClient) fetchAll() tea.Cmd {
	return f.record("GET", "fetchAll", "/api/swarm/dashboard", nil)
}
func (f *fakeClient) fetchTerminal(sid, agentID string) tea.Cmd {
	return func() tea.Msg { return tuiTermMsg{agentID: agentID, content: ""} }
}
func (f *fakeClient) fetchGitStatus(_, agentID string) tea.Cmd {
	return func() tea.Msg { return tuiGitStatusMsg{agentID: agentID} }
}
func (f *fakeClient) fetchNotes(sid, agentID string) tea.Cmd {
	return f.record("GET", "fetch-notes", "/api/swarm/sessions/"+sid+"/agents/"+agentID+"/note", nil)
}
func (f *fakeClient) post(op, path string, body interface{}) tea.Cmd {
	return f.record("POST", op, path, body)
}
func (f *fakeClient) patch(op, path string, body interface{}) tea.Cmd {
	return f.record("PATCH", op, path, body)
}
func (f *fakeClient) get(op, path string) tea.Cmd {
	return f.record("GET", op, path, nil)
}
func (f *fakeClient) deleteItem(op, path string) tea.Cmd {
	return f.record("DELETE", op, path, nil)
}
func (f *fakeClient) putSync(path string, body []byte) error {
	f.calls = append(f.calls, fakeCall{method: "PUT", path: path, body: body})
	return nil
}
func (f *fakeClient) getSync(path string) ([]byte, error) {
	f.calls = append(f.calls, fakeCall{method: "GET", path: path})
	return []byte(`{}`), nil
}

// ─── ANSI stripping ───────────────────────────────────────────────────────────

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// stripANSI removes terminal escape sequences so we can do plain string asserts.
func stripANSI(s string) string {
	// Try the charmbracelet ansi package first; fall back to regex.
	stripped := ansi.Strip(s)
	if stripped == "" && s != "" {
		stripped = ansiRE.ReplaceAllString(s, "")
	}
	return stripped
}

// ─── Model construction ───────────────────────────────────────────────────────

const tuiTestW, tuiTestH = 200, 50

// newTestModel returns a model primed with window-size and data messages.
// No actual network calls are made: the WS manager and HTTP client are
// constructed but their goroutines only fire when Init() is run, which we
// never do in unit tests.
func newTestModel(sessions []tuiSession, states map[string]tuiState) tuiModel {
	// Disable lipgloss colour rendering so View() output is plain text.
	lipgloss.SetColorProfile(termenv.Ascii)

	m := newTUIModel()
	// Set terminal size first (viewport needs dimensions before content is set).
	m2, _ := m.Update(tea.WindowSizeMsg{Width: tuiTestW, Height: tuiTestH})
	m = m2.(tuiModel)
	// Inject data.
	if sessions != nil {
		m2, _ = m.Update(tuiDataMsg{sessions: sessions, states: states})
		m = m2.(tuiModel)
	}
	return m
}

// ─── Driver ───────────────────────────────────────────────────────────────────

// drive applies a sequence of messages to a model and returns the final state.
func drive(m tuiModel, msgs ...tea.Msg) tuiModel {
	for _, msg := range msgs {
		m2, _ := m.Update(msg)
		m = m2.(tuiModel)
	}
	return m
}

// driveCapture applies messages and collects every non-nil tea.Cmd returned.
// Use this when a test needs to verify side-effect commands.
func driveCapture(m tuiModel, msgs ...tea.Msg) (tuiModel, []tea.Cmd) {
	var cmds []tea.Cmd
	for _, msg := range msgs {
		m2, cmd := m.Update(msg)
		m = m2.(tuiModel)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return m, cmds
}

// ─── Model construction (with injected client) ────────────────────────────────

// newTestModelWithClient is like newTestModel but injects a custom TUIClient.
// Use it together with fakeClient to inspect outbound HTTP intent.
func newTestModelWithClient(sessions []tuiSession, states map[string]tuiState, client TUIClient) tuiModel {
	lipgloss.SetColorProfile(termenv.Ascii)
	m := newTUIModel()
	m.client = client
	m2, _ := m.Update(tea.WindowSizeMsg{Width: tuiTestW, Height: tuiTestH})
	m = m2.(tuiModel)
	if sessions != nil {
		m2, _ = m.Update(tuiDataMsg{sessions: sessions, states: states})
		m = m2.(tuiModel)
	}
	return m
}

// ─── Key helpers ──────────────────────────────────────────────────────────────

func keyRune(r rune) tea.KeyMsg     { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }
func keyStr(s string) tea.KeyMsg    { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
func keyAltRune(r rune) tea.KeyMsg  { return tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{r}} }

func keyUp() tea.KeyMsg    { return tea.KeyMsg{Type: tea.KeyUp} }
func keyDown() tea.KeyMsg  { return tea.KeyMsg{Type: tea.KeyDown} }
func keyEnter() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEnter} }
func keyEsc() tea.KeyMsg   { return tea.KeyMsg{Type: tea.KeyEsc} }
func keyTab() tea.KeyMsg   { return tea.KeyMsg{Type: tea.KeyTab} }

// ─── View assertion helpers ───────────────────────────────────────────────────

func assertView(t *testing.T, m tuiModel, substr string) {
	t.Helper()
	v := stripANSI(m.View())
	if !strings.Contains(v, substr) {
		// Print a short excerpt to ease debugging.
		excerpt := v
		if len(excerpt) > 400 {
			excerpt = excerpt[:400] + "…"
		}
		t.Errorf("View() does not contain %q\nView excerpt:\n%s", substr, excerpt)
	}
}

func assertNoView(t *testing.T, m tuiModel, substr string) {
	t.Helper()
	v := stripANSI(m.View())
	if strings.Contains(v, substr) {
		t.Errorf("View() should NOT contain %q but does", substr)
	}
}

// ─── Test data builders ───────────────────────────────────────────────────────

func makeSession(id, name string) tuiSession {
	// Pad to at least 12 chars — viewSessionDetail does sess.ID[:12].
	for len(id) < 12 {
		id += "0"
	}
	return tuiSession{ID: id, Name: name}
}

func makeAgent(id, name, role, status string) tuiAgent {
	return tuiAgent{ID: id, Name: name, Role: role, Status: status}
}

func makeTask(id, title, stage string) tuiTask {
	return tuiTask{ID: id, Title: title, Stage: stage}
}

func makeGoal(id, desc, status string) tuiGoal {
	return tuiGoal{ID: id, Description: desc, Status: status}
}

// stdSessions returns a simple 2-session, 2-agent, 2-task dataset used by
// multiple tests.  IDs are UUID-length (≥12 chars) to satisfy render code
// that slices [:12] for the session detail view.
func stdSessions() ([]tuiSession, map[string]tuiState) {
	s1 := makeSession("aaaabbbbcccc0001", "Alpha")
	s2 := makeSession("aaaabbbbcccc0002", "Beta")
	a1 := makeAgent("agent-111-aabbcc", "Alice", "senior-dev", "idle")
	a2 := makeAgent("agent-222-aabbcc", "Bob", "qa-agent", "thinking")
	t1 := makeTask("task-001-aabbcc00", "Implement auth", "implement")
	t2 := makeTask("task-002-aabbcc00", "Write tests", "spec")
	sessions := []tuiSession{s1, s2}
	states := map[string]tuiState{
		s1.ID: {Session: s1, Agents: []tuiAgent{a1}, Tasks: []tuiTask{t1}},
		s2.ID: {Session: s2, Agents: []tuiAgent{a2}, Tasks: []tuiTask{t2}},
	}
	return sessions, states
}
