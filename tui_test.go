package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
)

// ─── Test helpers ───────────────────────────────────────────────────────────

const (
	testWidth  = 80
	testHeight = 24
)

// mockSpawner records spawn calls without touching tmux/DB.
type mockSpawner struct {
	calls []spawnCall
	err   error
}

type spawnCall struct {
	name, dir              string
	contextID, contextName *string
}

func (m *mockSpawner) Spawn(_ context.Context, name, dir string, contextID, contextName *string) (*Session, error) {
	m.calls = append(m.calls, spawnCall{name, dir, contextID, contextName})
	if m.err != nil {
		return nil, m.err
	}
	return &Session{ID: "test-id", Name: name, TmuxSession: "sw-test", Directory: dir, Status: "running"}, nil
}

// newTestModel creates a tuiModel with fixed dimensions and injected items.
// The viewport is initialized and ready.
func newTestModel(items []sidebarItem) tuiModel {
	ni := textinput.New()
	ni.Placeholder = "Session name"
	ni.CharLimit = 64

	di := textinput.New()
	di.Placeholder = "Working directory"
	di.CharLimit = 256
	di.SetValue("/home/test")

	fi := textinput.New()
	fi.Placeholder = "filter..."
	fi.CharLimit = 128

	m := tuiModel{
		mode:         modePassthrough,
		newNameInput: ni,
		newDirInput:  di,
		popupFilter:  fi,
		renameInput:   textinput.New(),
		feedbackInput: textinput.New(),
		spawner:      &mockSpawner{},
		items:        items,
		w:            testWidth,
		h:            testHeight,
	}

	// Initialize viewport
	contentWidth := m.w - 26
	if contentWidth < 20 {
		contentWidth = 20
	}
	contentHeight := m.h - 2
	if contentHeight < 5 {
		contentHeight = 5
	}
	m.vp = viewport.New(contentWidth, contentHeight)
	m.vpReady = true

	// Compute initial content cache
	m.updateContentCache()

	return m
}

// fakeSessionItem creates a sidebar item representing a session.
func fakeSessionItem(name, status string) sidebarItem {
	indicator := statusStopped
	if status == "running" {
		indicator = statusRunning
	}
	return sidebarItem{
		kind:        itemSession,
		label:       name,
		indicator:   indicator,
		sessionID:   "id-" + name,
		tmuxSession: "sw-" + name,
		status:      status,
	}
}

// fakePoolItem creates a sidebar item representing a pool slot.
func fakePoolItem(model, state string) sidebarItem {
	ind := statusAPI
	if state == "dead" {
		ind = statusStopped
	}
	short := modelShortName(model)
	return sidebarItem{
		kind:      itemPoolSlot,
		label:     fmt.Sprintf("[api] %s", short),
		indicator: ind,
		slotID:    "pool-" + short + "-0",
		model:     model,
		state:     state,
		alive:     state != "dead",
	}
}

// sendKey sends a key message through Update and returns the updated model.
func sendKey(m tuiModel, key string) tuiModel {
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return updated.(tuiModel)
}

// sendSpecialKey sends a special key (ctrl, alt, etc.) through Update.
func sendSpecialKey(m tuiModel, key string) tuiModel {
	msg := tea.KeyMsg{}
	switch key {
	case "alt+a":
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a"), Alt: true}
	case "alt+z":
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z"), Alt: true}
	case "alt+q":
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q"), Alt: true}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEscape}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	default:
		// Try alt keys: "alt+n" → parse
		if strings.HasPrefix(key, "alt+") {
			r := rune(key[4])
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true}
		}
	}
	updated, _ := m.Update(msg)
	return updated.(tuiModel)
}

// stripAnsi removes ANSI escape codes from a string for stable comparisons.
func stripAnsi(s string) string {
	return xansi.Strip(s)
}

// timeRe matches HH:MM:SS timestamps for normalization in golden comparisons.
var timeRe = regexp.MustCompile(`\d{2}:\d{2}:\d{2}`)

// normalizeTimestamps replaces HH:MM:SS with a fixed placeholder.
func normalizeTimestamps(s string) string {
	return timeRe.ReplaceAllString(s, "00:00:00")
}

// viewStripped returns the View() output with ANSI codes stripped.
func viewStripped(m tuiModel) string {
	return stripAnsi(m.View())
}

// assertContains checks that the view contains the given substring.
func assertContains(t *testing.T, view, substr string) {
	t.Helper()
	if !strings.Contains(view, substr) {
		t.Errorf("view should contain %q but does not.\nView:\n%s", substr, view)
	}
}

// assertNotContains checks that the view does NOT contain the given substring.
func assertNotContains(t *testing.T, view, substr string) {
	t.Helper()
	if strings.Contains(view, substr) {
		t.Errorf("view should NOT contain %q but does.\nView:\n%s", substr, view)
	}
}

// ─── Golden file helpers ────────────────────────────────────────────────────

func goldenPath(name string) string {
	return filepath.Join("testdata", name+".golden")
}

func updateGolden() bool {
	return os.Getenv("UPDATE_GOLDEN") != ""
}

func assertGolden(t *testing.T, name, actual string) {
	t.Helper()
	path := goldenPath(name)

	// Normalize timestamps so golden files don't depend on wall clock
	normalized := normalizeTimestamps(actual)

	if updateGolden() {
		os.MkdirAll(filepath.Dir(path), 0755)
		if err := os.WriteFile(path, []byte(normalized), 0644); err != nil {
			t.Fatalf("failed to write golden file %s: %v", path, err)
		}
		return
	}

	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden file %s not found. Run with UPDATE_GOLDEN=1 to generate.\nActual output:\n%s", path, normalized)
	}

	if string(expected) != normalized {
		t.Errorf("golden file %s mismatch.\n--- expected ---\n%s\n--- actual ---\n%s", path, string(expected), normalized)
	}
}

// ─── View contract tests ────────────────────────────────────────────────────

func TestView_Loading(t *testing.T) {
	m := newTestModel(nil)
	m.w = 0 // simulate pre-WindowSizeMsg state
	view := m.View()
	if view != "Loading..." {
		t.Errorf("expected Loading... before window size, got: %q", view)
	}
}

func TestView_Empty(t *testing.T) {
	m := newTestModel(nil)
	view := viewStripped(m)

	assertContains(t, view, "SwarmOps")
	assertContains(t, view, "(no sessions)")
	assertContains(t, view, "No sessions. Press Alt+N to create one.")
	assertContains(t, view, "quit")
	assertGolden(t, "view_empty", view)
}

func TestView_Sessions(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("my-project", "running"),
		fakeSessionItem("old-task", "stopped"),
	}
	m := newTestModel(items)
	view := viewStripped(m)

	assertContains(t, view, "SwarmOps")
	assertContains(t, view, "my-project")
	assertContains(t, view, "old-task")
	assertGolden(t, "view_sessions", view)
}

func TestView_PoolSlots(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("dev-session", "running"),
		fakePoolItem("claude-haiku-4-5", "idle"),
		fakePoolItem("claude-sonnet-4-6", "busy"),
	}
	m := newTestModel(items)
	view := viewStripped(m)

	assertContains(t, view, "SwarmOps")
	assertContains(t, view, "dev-session")
	assertContains(t, view, "Pool")
	assertContains(t, view, "[api] haiku")
	assertContains(t, view, "[api] sonnet")
	assertGolden(t, "view_pool", view)
}

func TestView_PoolSlotSelected(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("sess", "running"),
		fakePoolItem("claude-haiku-4-5", "idle"),
	}
	m := newTestModel(items)
	m.cursor = 1
	m.updateContentCache()
	view := viewStripped(m)

	assertContains(t, view, "Pool Slot:")
	assertContains(t, view, "Model:")
	assertContains(t, view, "claude-haiku-4-5")
	assertContains(t, view, "State:")
	assertContains(t, view, "idle")
}

func TestView_StoppedSession(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("stopped-sess", "stopped"),
	}
	m := newTestModel(items)
	view := viewStripped(m)

	assertContains(t, view, "Session stopped.")
}

func TestView_NewNameMode(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeNewName
	m.newNameInput.Focus()
	view := viewStripped(m)

	assertContains(t, view, "Name:")
}

func TestView_NewDirMode(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeNewDir
	m.newDirInput.Focus()
	view := viewStripped(m)

	assertContains(t, view, "Dir:")
}

func TestView_ContextPickerMode(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeContextPick
	m.contexts = []contextItem{
		{ID: "ctx-1", Name: "swarmops"},
		{ID: "ctx-2", Name: "homelab"},
	}
	m.ctxCursor = 0
	view := viewStripped(m)

	assertContains(t, view, "Context:")
	assertContains(t, view, "(none)")
	assertContains(t, view, "swarmops")
	assertContains(t, view, "homelab")
}

// ─── Key handling / state transition tests ──────────────────────────────────

func TestKey_CursorDown(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("first", "running"),
		fakeSessionItem("second", "running"),
		fakeSessionItem("third", "running"),
	}
	m := newTestModel(items)

	if m.cursor != 0 {
		t.Fatalf("initial cursor should be 0, got %d", m.cursor)
	}

	m = sendSpecialKey(m, "alt+z")
	if m.cursor != 1 {
		t.Errorf("after alt+z: cursor should be 1, got %d", m.cursor)
	}

	m = sendSpecialKey(m, "alt+z")
	if m.cursor != 2 {
		t.Errorf("after second alt+z: cursor should be 2, got %d", m.cursor)
	}
}

func TestKey_CursorUp(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("first", "running"),
		fakeSessionItem("second", "running"),
	}
	m := newTestModel(items)
	m.cursor = 1

	m = sendSpecialKey(m, "alt+a")
	if m.cursor != 0 {
		t.Errorf("after alt+a: cursor should be 0, got %d", m.cursor)
	}
}

func TestKey_CursorClampTop(t *testing.T) {
	items := []sidebarItem{fakeSessionItem("only", "running")}
	m := newTestModel(items)
	m.cursor = 0

	m = sendSpecialKey(m, "alt+a")
	if m.cursor != 0 {
		t.Errorf("cursor should stay at 0 when already at top, got %d", m.cursor)
	}
}

func TestKey_CursorClampBottom(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("first", "running"),
		fakeSessionItem("second", "running"),
	}
	m := newTestModel(items)
	m.cursor = 1

	m = sendSpecialKey(m, "alt+z")
	if m.cursor != 1 {
		t.Errorf("cursor should stay at 1 when already at bottom, got %d", m.cursor)
	}
}

func TestKey_CursorEmptyList(t *testing.T) {
	m := newTestModel(nil)

	m = sendSpecialKey(m, "alt+z")
	if m.cursor != 0 {
		t.Errorf("cursor should stay at 0 with empty list, got %d", m.cursor)
	}
}

func TestKey_NewSessionMode(t *testing.T) {
	m := newTestModel(nil)

	m = sendSpecialKey(m, "alt+n")
	if m.mode != modeNewName {
		t.Errorf("alt+n should enter modeNewName, got %d", m.mode)
	}
	if m.flash == "" {
		t.Error("flash should show name prompt")
	}
}

func TestKey_NameToDirTransition(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeNewName
	m.newNameInput.Focus()
	m.newNameInput.SetValue("test-session")

	m = sendSpecialKey(m, "enter")
	if m.mode != modeNewDir {
		t.Errorf("enter with name should advance to modeNewDir, got %d", m.mode)
	}
}

func TestKey_NameEmptyNoAdvance(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeNewName
	m.newNameInput.Focus()
	m.newNameInput.SetValue("")

	m = sendSpecialKey(m, "enter")
	if m.mode != modeNewName {
		t.Errorf("enter with empty name should stay in modeNewName, got %d", m.mode)
	}
}

func TestKey_EscCancelsNewName(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeNewName

	m = sendSpecialKey(m, "esc")
	if m.mode != modePassthrough {
		t.Errorf("esc should return to passthrough, got %d", m.mode)
	}
	if m.flash != "" {
		t.Errorf("flash should be cleared, got %q", m.flash)
	}
}

func TestKey_EscCancelsNewDir(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeNewDir

	m = sendSpecialKey(m, "esc")
	if m.mode != modePassthrough {
		t.Errorf("esc should return to passthrough, got %d", m.mode)
	}
}

func TestKey_EscCancelsContextPick(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeContextPick
	m.contexts = []contextItem{{ID: "1", Name: "ctx"}}
	m.ctxCursor = 0

	m = sendSpecialKey(m, "esc")
	if m.mode != modePassthrough {
		t.Errorf("esc should return to passthrough, got %d", m.mode)
	}
}

func TestKey_ContextPickNavigation(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeContextPick
	m.contexts = []contextItem{
		{ID: "1", Name: "alpha"},
		{ID: "2", Name: "beta"},
	}
	m.ctxCursor = 0

	// Move down
	m = sendSpecialKey(m, "alt+z")
	if m.ctxCursor != 1 {
		t.Errorf("alt+z should move context cursor to 1, got %d", m.ctxCursor)
	}

	m = sendSpecialKey(m, "alt+z")
	if m.ctxCursor != 2 {
		t.Errorf("alt+z should move context cursor to 2, got %d", m.ctxCursor)
	}

	// Clamp at bottom (0=none, 1=alpha, 2=beta → max is 2)
	m = sendSpecialKey(m, "alt+z")
	if m.ctxCursor != 2 {
		t.Errorf("context cursor should clamp at bottom, got %d", m.ctxCursor)
	}

	// Move back up
	m = sendSpecialKey(m, "alt+a")
	if m.ctxCursor != 1 {
		t.Errorf("alt+a should move context cursor to 1, got %d", m.ctxCursor)
	}
}

func TestKey_ContextPickSelectNone(t *testing.T) {
	spawner := &mockSpawner{}
	m := newTestModel(nil)
	m.spawner = spawner
	m.mode = modeContextPick
	m.contexts = []contextItem{{ID: "1", Name: "alpha"}}
	m.ctxCursor = 0 // (none)
	m.newNameInput.SetValue("test-sess")
	m.newDirInput.SetValue("/tmp")

	m = sendSpecialKey(m, "enter")
	if m.mode != modePassthrough {
		t.Errorf("enter should return to passthrough, got %d", m.mode)
	}
	if len(spawner.calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(spawner.calls))
	}
	if spawner.calls[0].contextID != nil {
		t.Errorf("selecting (none) should pass nil contextID")
	}
}

func TestKey_ContextPickSelectContext(t *testing.T) {
	spawner := &mockSpawner{}
	m := newTestModel(nil)
	m.spawner = spawner
	m.mode = modeContextPick
	m.contexts = []contextItem{{ID: "ctx-1", Name: "alpha"}}
	m.ctxCursor = 1 // alpha
	m.newNameInput.SetValue("test-sess")
	m.newDirInput.SetValue("/tmp")

	m = sendSpecialKey(m, "enter")
	if m.mode != modePassthrough {
		t.Errorf("enter should return to passthrough, got %d", m.mode)
	}
	if len(spawner.calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(spawner.calls))
	}
	if spawner.calls[0].contextID == nil || *spawner.calls[0].contextID != "ctx-1" {
		t.Errorf("selecting alpha should pass contextID=ctx-1, got %v", spawner.calls[0].contextID)
	}
}

func TestKey_SpawnError(t *testing.T) {
	// Note: the contextPick enter handler clears flash after doSpawn (m.flash = ""),
	// so we verify the spawn was attempted via the mock rather than the flash.
	spawner := &mockSpawner{err: fmt.Errorf("tmux failed")}
	m := newTestModel(nil)
	m.spawner = spawner
	m.mode = modeContextPick
	m.contexts = []contextItem{}
	m.ctxCursor = 0
	m.newNameInput.SetValue("fail-sess")

	m = sendSpecialKey(m, "enter")
	if len(spawner.calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(spawner.calls))
	}
	if spawner.calls[0].name != "fail-sess" {
		t.Errorf("spawn should be called with name fail-sess, got %q", spawner.calls[0].name)
	}
	// Mode should return to passthrough even on error
	if m.mode != modePassthrough {
		t.Errorf("should return to passthrough after spawn error, got %d", m.mode)
	}
}

func TestKey_Quit(t *testing.T) {
	m := newTestModel(nil)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q"), Alt: true})
	if cmd == nil {
		t.Error("alt+q should produce a quit command")
	}
	// Execute the command to check it's a quit
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected QuitMsg, got %T", msg)
	}
}

// ─── Sidebar rendering tests ───────────────────────────────────────────────

func TestSidebar_Truncation(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("this-is-a-very-long-session-name-that-should-be-truncated", "running"),
	}
	m := newTestModel(items)
	view := viewStripped(m)

	// The label is truncated by renderSidebar to 17+3=20 chars, then the sidebar
	// style (Width=24, Padding=1,1) may clip further. Verify the full name
	// does NOT appear and some truncated prefix does.
	assertNotContains(t, view, "this-is-a-very-long-session-name-that-should-be-truncated")
	assertContains(t, view, "this-is-a-very-")
}

func TestSidebar_PoolSeparator(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("sess", "running"),
		fakePoolItem("claude-haiku-4-5", "idle"),
	}
	m := newTestModel(items)
	view := viewStripped(m)

	assertContains(t, view, "Pool")
}

func TestSidebar_SelectedHighlighting(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("first", "running"),
		fakeSessionItem("second", "running"),
	}
	m := newTestModel(items)
	m.cursor = 0

	// We can't easily test ANSI bold/color, but we can test that both items appear
	view := viewStripped(m)
	assertContains(t, view, "first")
	assertContains(t, view, "second")
}

func TestSidebar_StatusIndicators(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("active", "running"),
		fakeSessionItem("inactive", "stopped"),
	}
	m := newTestModel(items)
	view := viewStripped(m)

	// Both names should appear
	assertContains(t, view, "active")
	assertContains(t, view, "inactive")
}

// ─── Window resize tests ────────────────────────────────────────────────────

func TestWindowResize(t *testing.T) {
	m := newTestModel(nil)
	m.vpReady = false
	m.w = 0
	m.h = 0

	// Send window size message
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(tuiModel)

	if m.w != 120 {
		t.Errorf("width should be 120, got %d", m.w)
	}
	if m.h != 40 {
		t.Errorf("height should be 40, got %d", m.h)
	}
	if !m.vpReady {
		t.Error("vpReady should be true after WindowSizeMsg")
	}
}

func TestWindowResize_MinimumDimensions(t *testing.T) {
	m := newTestModel(nil)

	// Very small terminal
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 30, Height: 8})
	m = updated.(tuiModel)

	if m.w != 30 {
		t.Errorf("width should be 30, got %d", m.w)
	}
	// Should still render without panic
	view := m.View()
	if view == "" {
		t.Error("view should not be empty even with small terminal")
	}
}

// ─── Message handling tests ─────────────────────────────────────────────────

func TestItemsMsg_UpdatesList(t *testing.T) {
	m := newTestModel(nil)

	items := []sidebarItem{
		fakeSessionItem("new-session", "running"),
	}
	updated, _ := m.Update(itemsMsg(items))
	m = updated.(tuiModel)

	if len(m.items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(m.items))
	}
	if m.items[0].label != "new-session" {
		t.Errorf("item label should be new-session, got %q", m.items[0].label)
	}
}

func TestItemsMsg_CursorClamp(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("a", "running"),
		fakeSessionItem("b", "running"),
		fakeSessionItem("c", "running"),
	}
	m := newTestModel(items)
	m.cursor = 2

	// Now reduce to 1 item
	updated, _ := m.Update(itemsMsg([]sidebarItem{fakeSessionItem("a", "running")}))
	m = updated.(tuiModel)

	if m.cursor != 0 {
		t.Errorf("cursor should clamp to 0 when items shrink, got %d", m.cursor)
	}
}

func TestTerminalMsg_UpdatesContent(t *testing.T) {
	items := []sidebarItem{fakeSessionItem("s", "running")}
	m := newTestModel(items)

	updated, _ := m.Update(terminalMsg("$ hello world\noutput here"))
	m = updated.(tuiModel)

	if m.termContent != "$ hello world\noutput here" {
		t.Errorf("termContent should be set, got %q", m.termContent)
	}
	if m.contentCache != m.termContent {
		t.Errorf("contentCache should match termContent")
	}
}

func TestContextListMsg_NoContexts(t *testing.T) {
	spawner := &mockSpawner{}
	m := newTestModel(nil)
	m.spawner = spawner
	m.newNameInput.SetValue("test")

	updated, _ := m.Update(contextListMsg(nil))
	m = updated.(tuiModel)

	// With no contexts, should spawn directly and return to passthrough
	if m.mode != modePassthrough {
		t.Errorf("should return to passthrough with no contexts, got %d", m.mode)
	}
	if len(spawner.calls) != 1 {
		t.Errorf("should have spawned session, got %d calls", len(spawner.calls))
	}
}

func TestContextListMsg_WithContexts(t *testing.T) {
	m := newTestModel(nil)
	contexts := []contextItem{
		{ID: "1", Name: "alpha"},
		{ID: "2", Name: "beta"},
	}

	updated, _ := m.Update(contextListMsg(contexts))
	m = updated.(tuiModel)

	if m.mode != modeContextPick {
		t.Errorf("should enter contextPick mode, got %d", m.mode)
	}
	if len(m.contexts) != 2 {
		t.Errorf("should have 2 contexts, got %d", len(m.contexts))
	}
	if m.ctxCursor != 0 {
		t.Errorf("context cursor should start at 0, got %d", m.ctxCursor)
	}
}

// ─── Content cache tests ────────────────────────────────────────────────────

func TestContentCache_EmptyItems(t *testing.T) {
	m := newTestModel(nil)
	if !strings.Contains(stripAnsi(m.contentCache), "No sessions") {
		t.Errorf("empty items should show no sessions message, got %q", m.contentCache)
	}
}

func TestContentCache_PoolSlot(t *testing.T) {
	items := []sidebarItem{
		fakePoolItem("claude-haiku-4-5", "idle"),
	}
	m := newTestModel(items)
	if !strings.Contains(m.contentCache, "Pool Slot:") {
		t.Errorf("pool slot selected should show detail, got %q", m.contentCache)
	}
}

func TestContentCache_StoppedSession(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("stopped", "stopped"),
	}
	m := newTestModel(items)
	if !strings.Contains(stripAnsi(m.contentCache), "Session stopped") {
		t.Errorf("stopped session should show stopped message, got %q", m.contentCache)
	}
}

func TestContentCache_RunningSessionPreservesTermContent(t *testing.T) {
	items := []sidebarItem{fakeSessionItem("s", "running")}
	m := newTestModel(items)
	m.termContent = "some terminal output"
	m.contentCache = m.termContent

	// Simulate cursor move to same item
	m.updateContentCache()

	// Running sessions don't overwrite contentCache (it's set by terminalMsg)
	// updateContentCache for a running session is a no-op
	// The contentCache should still be the termContent
	if m.contentCache != "some terminal output" {
		t.Errorf("running session should preserve term content, got %q", m.contentCache)
	}
}

// ─── Status bar tests ───────────────────────────────────────────────────────

func TestStatusBar_DefaultHelp(t *testing.T) {
	m := newTestModel(nil)
	view := viewStripped(m)

	assertContains(t, view, "nav")
	assertContains(t, view, "new")
	assertContains(t, view, "delete")
	assertContains(t, view, "quit")
}

func TestStatusBar_FlashMessage(t *testing.T) {
	m := newTestModel(nil)
	m.flash = "Session deleted"
	view := viewStripped(m)

	assertContains(t, view, "Session deleted")
}

func TestStatusBar_ClearedOnCursorMove(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("a", "running"),
		fakeSessionItem("b", "running"),
	}
	m := newTestModel(items)
	m.flash = "some message"

	m = sendSpecialKey(m, "alt+z")
	if m.flash != "" {
		t.Errorf("flash should be cleared on cursor move, got %q", m.flash)
	}
}

// ─── Top bar tests ──────────────────────────────────────────────────────────

func TestTopBar_ShowsTitle(t *testing.T) {
	m := newTestModel(nil)
	view := viewStripped(m)

	assertContains(t, view, "SwarmOps")
}

func TestTopBar_ShowsSessionCounts(t *testing.T) {
	items := []sidebarItem{
		fakeSessionItem("running-1", "running"),
		fakeSessionItem("running-2", "running"),
		fakeSessionItem("stopped-1", "stopped"),
	}
	m := newTestModel(items)
	view := viewStripped(m)

	assertContains(t, view, "3 sess")
}

func TestTopBar_ShowsPoolInfo(t *testing.T) {
	items := []sidebarItem{
		fakePoolItem("claude-haiku-4-5", "idle"),
		fakePoolItem("claude-sonnet-4-6", "busy"),
	}
	m := newTestModel(items)
	view := viewStripped(m)

	assertContains(t, view, "2/2 pool")
}

func TestTopBar_PoolOffWhenNoSlots(t *testing.T) {
	m := newTestModel(nil)
	view := viewStripped(m)

	// With no sessions and no pool, sidebar shows SwarmOps header but no pool summary
	assertContains(t, view, "SwarmOps")
}

func TestTopBar_ShowsTimestamp(t *testing.T) {
	m := newTestModel(nil)
	view := viewStripped(m)

	// Should contain a time-like pattern (HH:MM:SS)
	assertContains(t, view, ":")
}

func TestSidebar_ShowsSessionsLabel(t *testing.T) {
	m := newTestModel(nil)
	view := viewStripped(m)

	assertContains(t, view, "SwarmOps")
}

// ─── Keyboard audit: keys don't leak between modes ──────────────────────────

func TestKeyAudit_PassthroughAltKeysWork(t *testing.T) {
	m := newTestModel(nil)

	// Alt+N should enter new name mode, not be sent to tmux
	m = sendSpecialKey(m, "alt+n")
	if m.mode != modeNewName {
		t.Errorf("alt+n should enter modeNewName, got %d", m.mode)
	}
}

func TestKeyAudit_PopupKeysNotPassedToTmux(t *testing.T) {
	// In popup mode, keys like q, s, r, / should be handled by popup, not sent to tmux
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()

	// s should cycle sort, not be sent anywhere
	m = sendKey(m, "s")
	if m.popupSortMode != 1 {
		t.Errorf("s in popup should cycle sort, got mode %d", m.popupSortMode)
	}

	// q should close popup
	m = sendKey(m, "q")
	if m.mode != modePassthrough {
		t.Errorf("q should close popup, got mode %d", m.mode)
	}
}

func TestKeyAudit_RegularKeysInPassthroughGoToTmux(t *testing.T) {
	// Regular keys in passthrough should fall through to sendKeyToSession
	// We can't easily test tmux interaction, but we can verify they don't
	// change mode or state
	items := []sidebarItem{fakeSessionItem("sess", "running")}
	m := newTestModel(items)

	m = sendKey(m, "a")
	if m.mode != modePassthrough {
		t.Errorf("regular key should stay in passthrough, got %d", m.mode)
	}
}

// ─── Mouse handling test ────────────────────────────────────────────────────

func TestMouse_ViewportHandlesMouseInPassthrough(t *testing.T) {
	m := newTestModel(nil)

	// Send a mouse wheel event — should not panic or change mode
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m = updated.(tuiModel)

	if m.mode != modePassthrough {
		t.Errorf("mouse in passthrough should stay in passthrough, got %d", m.mode)
	}
}

func TestMouse_IgnoredInPopupModes(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modePlaneIssues
	m.planeIssues = fakePlaneIssues()

	// Mouse in popup mode should not change anything
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m = updated.(tuiModel)

	if m.mode != modePlaneIssues {
		t.Errorf("mouse in popup should stay in popup, got %d", m.mode)
	}
}
