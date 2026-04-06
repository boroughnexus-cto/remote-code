package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── Styles ──────────────────────────────────────────────────────────────────

var (
	sidebarStyle = lipgloss.NewStyle().
			Width(24).
			BorderRight(true).
			BorderStyle(lipgloss.NormalBorder()).
			Padding(1, 1)

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#15a8a8"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	statusRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("#00ff00")).Render("●")
	statusStopped = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("○")
	statusAPI     = lipgloss.NewStyle().Foreground(lipgloss.Color("#fe5d26")).Render("◆")

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#023d60")).
			Padding(0, 1)
)

// ─── Sidebar item: unified type for sessions + pool slots ────────────────────

type sidebarItemKind int

const (
	itemSession sidebarItemKind = iota
	itemPoolSlot
)

type sidebarItem struct {
	kind      sidebarItemKind
	label     string
	indicator string
	// Session fields
	sessionID   string
	tmuxSession string
	status      string
	// Pool slot fields
	slotID   string
	model    string
	state    string // idle, busy, starting, dead
	requests int64
	costUSD  float64
	alive    bool
}

// ─── Messages ────────────────────────────────────────────────────────────────

type tickMsg time.Time
type sessionsMsg []Session
type terminalMsg string
type contextListMsg []contextItem
type itemsMsg []sidebarItem

type contextItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ─── Model ───────────────────────────────────────────────────────────────────

type tuiMode int

const (
	modePassthrough tuiMode = iota
	modeNewName
	modeNewDir
	modeContextPick
	modePlaneIssues
	modeIcingaAlerts
	modePopupAction
)

// Spawner abstracts session creation for testability.
type Spawner interface {
	Spawn(ctx context.Context, name, dir string, contextID, contextName *string) (*Session, error)
}

// defaultSpawner calls the real spawnSession function.
type defaultSpawner struct{}

func (defaultSpawner) Spawn(ctx context.Context, name, dir string, contextID, contextName *string) (*Session, error) {
	return spawnSession(ctx, name, dir, contextID, contextName)
}

type tuiModel struct {
	items  []sidebarItem
	cursor int
	mode   tuiMode

	// Right pane
	vp      viewport.Model
	vpReady bool

	// New session wizard
	newNameInput textinput.Model
	newDirInput  textinput.Model
	contexts     []contextItem
	ctxCursor    int

	// Terminal content cache
	termContent string

	// Pre-rendered content for the right pane (set in Update, read in View)
	contentCache string

	// Terminal size
	w, h int

	// Status message
	flash string

	// Popup data
	planeIssues    []planeIssue
	icingaProblems []icingaProblem
	popupErr       string
	popupCursor    int

	// Popup filter & sort
	popupFilter       textinput.Model
	popupFilterActive bool
	popupSortMode     int // 0=default, 1, 2 — meaning depends on popup type

	// Action picker (modePopupAction)
	actionTarget    string        // display label for the selected item
	actionPrompt    string        // text to inject into the session
	actionPrevMode  tuiMode       // mode to return to on Esc
	actionSessions  []sidebarItem // running sessions to choose from
	actionCursor    int           // cursor in action picker (sessions + "new" option)

	// Dependency injection for testing
	spawner Spawner
}

func initialModel() tuiModel {
	ni := textinput.New()
	ni.Placeholder = "Session name"
	ni.CharLimit = 64

	di := textinput.New()
	di.Placeholder = "Working directory"
	di.CharLimit = 256
	di.SetValue(os.Getenv("HOME"))

	fi := textinput.New()
	fi.Placeholder = "filter..."
	fi.CharLimit = 128

	return tuiModel{
		mode:         modePassthrough,
		newNameInput: ni,
		newDirInput:  di,
		popupFilter:  fi,
		spawner:      defaultSpawner{},
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(tickCmd(), loadItems)
}

func tickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// loadItems builds the unified sidebar list: sessions first, then pool slots.
func loadItems() tea.Msg {
	ctx := context.Background()
	refreshSessionStatuses(ctx)
	sessions, _ := listSessions(ctx)

	var items []sidebarItem

	for _, s := range sessions {
		indicator := statusStopped
		if s.Status == "running" {
			indicator = statusRunning
		}
		items = append(items, sidebarItem{
			kind:        itemSession,
			label:       s.Name,
			indicator:   indicator,
			sessionID:   s.ID,
			tmuxSession: s.TmuxSession,
			status:      s.Status,
		})
	}

	if globalPool != nil {
		status := globalPool.Status()
		if models, ok := status["models"].(map[string]interface{}); ok {
			for model, info := range models {
				if minfo, ok := info.(map[string]interface{}); ok {
					if slots, ok := minfo["slots"].([]map[string]interface{}); ok {
						for _, slot := range slots {
							sid, _ := slot["id"].(string)
							state, _ := slot["state"].(string)
							reqs, _ := slot["requests"].(int64)
							cost, _ := slot["cost_usd"].(float64)
							alive, _ := slot["alive"].(bool)

							ind := statusAPI
							if !alive || state == "dead" {
								ind = statusStopped
							}

							short := modelShortName(model)
							items = append(items, sidebarItem{
								kind:      itemPoolSlot,
								label:     fmt.Sprintf("[api] %s", short),
								indicator: ind,
								slotID:   sid,
								model:    model,
								state:    state,
								requests: reqs,
								costUSD:  cost,
								alive:    alive,
							})
						}
					}
				}
			}
		}
	}

	return itemsMsg(items)
}

func loadTerminal(tmuxName string) tea.Cmd {
	return func() tea.Msg {
		content, err := captureTerminal(tmuxName)
		if err != nil {
			return terminalMsg("(error: " + err.Error() + ")")
		}
		return terminalMsg(content)
	}
}

func fetchContexts() tea.Cmd {
	return func() tea.Msg {
		url := "https://mcp-context.gate-hexatonic.ts.net/contexts?limit=50"
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(url)
		if err != nil {
			return contextListMsg(nil)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		var items []contextItem
		json.Unmarshal(body, &items)
		return contextListMsg(items)
	}
}

// ─── Update ──────────────────────────────────────────────────────────────────

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.w = msg.Width
		m.h = msg.Height
		contentWidth := m.w - 26
		if contentWidth < 20 {
			contentWidth = 20
		}
		contentHeight := m.h - 2
		if contentHeight < 5 {
			contentHeight = 5
		}
		m.vp = viewport.New(contentWidth, contentHeight)
		m.vp.MouseWheelEnabled = true
		m.vpReady = true
		m.updateContentCache()
		return m, nil

	case tickMsg:
		var cmds []tea.Cmd
		cmds = append(cmds, tickCmd(), loadItems)
		if m.cursor < len(m.items) {
			item := m.items[m.cursor]
			if item.kind == itemSession && item.status == "running" {
				cmds = append(cmds, loadTerminal(item.tmuxSession))
			}
		}
		return m, tea.Batch(cmds...)

	case itemsMsg:
		m.items = msg
		if m.cursor >= len(m.items) && len(m.items) > 0 {
			m.cursor = len(m.items) - 1
		}
		m.updateContentCache()
		return m, nil

	case terminalMsg:
		m.termContent = string(msg)
		m.contentCache = m.termContent
		if m.vpReady {
			m.vp.SetContent(m.contentCache)
			m.vp.GotoBottom() // auto-scroll for terminal output
		}
		return m, nil

	case planeIssuesMsg:
		if m.mode == modePlaneIssues {
			m.planeIssues = msg
			m.popupErr = ""
			if m.popupCursor >= len(m.planeIssues) {
				m.popupCursor = max(0, len(m.planeIssues)-1)
			}
		}
		return m, nil

	case icingaProblemsMsg:
		if m.mode == modeIcingaAlerts {
			m.icingaProblems = msg
			m.popupErr = ""
			if m.popupCursor >= len(m.icingaProblems) {
				m.popupCursor = max(0, len(m.icingaProblems)-1)
			}
		}
		return m, nil

	case popupErrMsg:
		if (msg.source == "plane" && m.mode == modePlaneIssues) ||
			(msg.source == "icinga" && m.mode == modeIcingaAlerts) {
			m.popupErr = msg.text
		}
		return m, nil

	case contextListMsg:
		m.contexts = msg
		if len(m.contexts) > 0 {
			m.mode = modeContextPick
			m.ctxCursor = 0
		} else {
			m.doSpawn(nil, nil)
			m.mode = modePassthrough
		}
		return m, loadItems

	case tea.MouseMsg:
		if m.mode == modePassthrough && m.vpReady {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch m.mode {
	case modePassthrough:
		switch key {
		case "ctrl+a":
			if m.cursor > 0 {
				m.cursor--
				m.flash = ""
				m.updateContentCache()
			}
			return m, nil
		case "ctrl+z":
			if m.cursor < len(m.items)-1 {
				m.cursor++
				m.flash = ""
				m.updateContentCache()
			}
			return m, nil
		case "alt+n":
			m.mode = modeNewName
			m.newNameInput.SetValue("")
			m.newNameInput.Focus()
			m.flash = "New session — enter name (esc to cancel)"
			return m, textinput.Blink
		case "alt+d":
			if m.cursor < len(m.items) && m.items[m.cursor].kind == itemSession {
				item := m.items[m.cursor]
				deleteSession(context.Background(), item.sessionID)
				m.flash = fmt.Sprintf("Deleted %s", item.label)
				return m, loadItems
			}
			return m, nil
		case "alt+p":
			m.mode = modePlaneIssues
			m.planeIssues = nil
			m.popupErr = ""
			m.popupCursor = 0
			m.flash = ""
			return m, fetchPlaneIssues()
		case "alt+i":
			m.mode = modeIcingaAlerts
			m.icingaProblems = nil
			m.popupErr = ""
			m.popupCursor = 0
			m.flash = ""
			return m, fetchIcingaProblems()
		case "ctrl+\\":
			return m, tea.Quit
		default:
			// Only pass keys to session items (not pool slots)
			if m.cursor < len(m.items) && m.items[m.cursor].kind == itemSession {
				m.sendKeyToSession(key)
			}
			return m, nil
		}

	case modeNewName:
		switch key {
		case "enter":
			if m.newNameInput.Value() != "" {
				m.mode = modeNewDir
				m.newDirInput.Focus()
				m.flash = "New session — enter directory (esc to cancel)"
				return m, textinput.Blink
			}
		case "esc":
			m.mode = modePassthrough
			m.flash = ""
		default:
			var cmd tea.Cmd
			m.newNameInput, cmd = m.newNameInput.Update(msg)
			return m, cmd
		}

	case modeNewDir:
		switch key {
		case "enter":
			m.flash = "Fetching contexts..."
			return m, fetchContexts()
		case "esc":
			m.mode = modePassthrough
			m.flash = ""
		default:
			var cmd tea.Cmd
			m.newDirInput, cmd = m.newDirInput.Update(msg)
			return m, cmd
		}

	case modeContextPick:
		switch key {
		case "ctrl+a", "up":
			if m.ctxCursor > 0 {
				m.ctxCursor--
			}
		case "ctrl+z", "down":
			if m.ctxCursor < len(m.contexts) {
				m.ctxCursor++
			}
		case "enter":
			if m.ctxCursor == 0 {
				m.doSpawn(nil, nil)
			} else {
				c := m.contexts[m.ctxCursor-1]
				m.doSpawn(&c.ID, &c.Name)
			}
			m.mode = modePassthrough
			m.flash = ""
			return m, loadItems
		case "esc":
			m.mode = modePassthrough
			m.flash = ""
		}

	case modePlaneIssues:
		if m.popupFilterActive {
			switch key {
			case "esc":
				m.popupFilterActive = false
				m.popupFilter.Blur()
				m.popupFilter.SetValue("")
				m.popupCursor = 0
			case "enter":
				m.popupFilterActive = false
				m.popupFilter.Blur()
				m.popupCursor = 0
			default:
				var cmd tea.Cmd
				m.popupFilter, cmd = m.popupFilter.Update(msg)
				m.popupCursor = 0
				return m, cmd
			}
		} else {
			filtered := filteredPlaneIssues(m)
			switch key {
			case "q", "esc":
				m.mode = modePassthrough
				m.popupErr = ""
				m.popupFilter.SetValue("")
				m.popupSortMode = 0
			case "ctrl+a", "up":
				if len(filtered) > 0 && m.popupCursor > 0 {
					m.popupCursor--
				}
			case "ctrl+z", "down":
				if len(filtered) > 0 && m.popupCursor < len(filtered)-1 {
					m.popupCursor++
				}
			case "/":
				m.popupFilterActive = true
				m.popupFilter.Focus()
				return m, textinput.Blink
			case "s":
				m.popupSortMode = (m.popupSortMode + 1) % len(planeSortLabels)
				m.popupCursor = 0
			case "enter":
				if len(filtered) > 0 && m.popupCursor < len(filtered) {
					issue := filtered[m.popupCursor]
					m.openActionPicker(issue.Title, planeIssuePrompt(issue), modePlaneIssues)
				}
			case "r":
				m.planeIssues = nil
				m.popupErr = ""
				m.popupCursor = 0
				return m, fetchPlaneIssues()
			}
		}

	case modeIcingaAlerts:
		if m.popupFilterActive {
			switch key {
			case "esc":
				m.popupFilterActive = false
				m.popupFilter.Blur()
				m.popupFilter.SetValue("")
				m.popupCursor = 0
			case "enter":
				m.popupFilterActive = false
				m.popupFilter.Blur()
				m.popupCursor = 0
			default:
				var cmd tea.Cmd
				m.popupFilter, cmd = m.popupFilter.Update(msg)
				m.popupCursor = 0
				return m, cmd
			}
		} else {
			filtered := filteredIcingaProblems(m)
			switch key {
			case "q", "esc":
				m.mode = modePassthrough
				m.popupErr = ""
				m.popupFilter.SetValue("")
				m.popupSortMode = 0
			case "ctrl+a", "up":
				if len(filtered) > 0 && m.popupCursor > 0 {
					m.popupCursor--
				}
			case "ctrl+z", "down":
				if len(filtered) > 0 && m.popupCursor < len(filtered)-1 {
					m.popupCursor++
				}
			case "/":
				m.popupFilterActive = true
				m.popupFilter.Focus()
				return m, textinput.Blink
			case "s":
				m.popupSortMode = (m.popupSortMode + 1) % len(icingaSortLabels)
				m.popupCursor = 0
			case "enter":
				if len(filtered) > 0 && m.popupCursor < len(filtered) {
					problem := filtered[m.popupCursor]
					label := fmt.Sprintf("%s / %s", problem.Host, problem.Service)
					m.openActionPicker(label, icingaProblemPrompt(problem), modeIcingaAlerts)
				}
			case "r":
				m.icingaProblems = nil
				m.popupErr = ""
				m.popupCursor = 0
				return m, fetchIcingaProblems()
			}
		}

	case modePopupAction:
		maxIdx := len(m.actionSessions) // last index is "new session"
		switch key {
		case "esc":
			m.mode = m.actionPrevMode
		case "ctrl+a", "up":
			if m.actionCursor > 0 {
				m.actionCursor--
			}
		case "ctrl+z", "down":
			if m.actionCursor < maxIdx {
				m.actionCursor++
			}
		case "enter":
			if m.actionCursor < len(m.actionSessions) {
				// Inject into existing session
				sess := m.actionSessions[m.actionCursor]
				if sess.status == "running" {
					injectToSession(sess.tmuxSession, m.actionPrompt)
					m.mode = modePassthrough
					m.flash = fmt.Sprintf("Sent to %s", sess.label)
				}
			} else {
				// Spawn new session
				name := sanitizeSessionName(m.actionTarget)
				s, err := m.spawner.Spawn(context.Background(), name, os.Getenv("HOME"), nil, nil)
				if err != nil {
					m.flash = "Spawn error: " + err.Error()
				} else {
					// Inject the prompt after a brief pause for claude to start
					go func() {
						time.Sleep(2 * time.Second)
						injectToSession(s.TmuxSession, m.actionPrompt)
					}()
					m.flash = fmt.Sprintf("Spawned %s — injecting task", s.Name)
				}
				m.mode = modePassthrough
				return m, loadItems
			}
		}
	}

	return m, nil
}

// sendKeyToSession translates a Bubbletea key string to tmux send-keys.
func (m *tuiModel) sendKeyToSession(key string) {
	if m.cursor >= len(m.items) {
		return
	}
	item := m.items[m.cursor]
	if item.kind != itemSession || item.status != "running" {
		return
	}

	tmuxKey := key
	switch key {
	case "enter":
		tmuxKey = "Enter"
	case "tab":
		tmuxKey = "Tab"
	case "backspace":
		tmuxKey = "BSpace"
	case "delete":
		tmuxKey = "DC"
	case "up":
		tmuxKey = "Up"
	case "down":
		tmuxKey = "Down"
	case "left":
		tmuxKey = "Left"
	case "right":
		tmuxKey = "Right"
	case "home":
		tmuxKey = "Home"
	case "end":
		tmuxKey = "End"
	case "pgup":
		tmuxKey = "PPage"
	case "pgdown":
		tmuxKey = "NPage"
	case "esc":
		tmuxKey = "Escape"
	case "space":
		tmuxKey = "Space"
	case "ctrl+c":
		tmuxKey = "C-c"
	case "ctrl+l":
		tmuxKey = "C-l"
	case "ctrl+r":
		tmuxKey = "C-r"
	case "ctrl+p":
		tmuxKey = "C-p"
	case "ctrl+e":
		tmuxKey = "C-e"
	case "ctrl+w":
		tmuxKey = "C-w"
	case "ctrl+u":
		tmuxKey = "C-u"
	case "ctrl+k":
		tmuxKey = "C-k"
	}

	if len(key) == 1 {
		exec.Command("tmux", "send-keys", "-t", item.tmuxSession, "-l", key).Run()
		return
	}
	exec.Command("tmux", "send-keys", "-t", item.tmuxSession, tmuxKey).Run()
}

func (m *tuiModel) doSpawn(contextID, contextName *string) {
	name := m.newNameInput.Value()
	dir := m.newDirInput.Value()
	if dir == "" {
		dir = os.Getenv("HOME")
	}
	s, err := m.spawner.Spawn(context.Background(), name, dir, contextID, contextName)
	if err != nil {
		m.flash = "Spawn error: " + err.Error()
	} else {
		m.flash = fmt.Sprintf("Spawned %s", s.Name)
	}
}

// openActionPicker prepares the action picker overlay with running sessions.
func (m *tuiModel) openActionPicker(target, prompt string, prevMode tuiMode) {
	m.actionTarget = target
	m.actionPrompt = prompt
	m.actionPrevMode = prevMode
	m.actionCursor = 0
	m.mode = modePopupAction

	// Collect running sessions for the picker
	m.actionSessions = nil
	for _, item := range m.items {
		if item.kind == itemSession && item.status == "running" {
			m.actionSessions = append(m.actionSessions, item)
		}
	}
}

// sanitizeSessionName creates a safe tmux session name from a title.
func sanitizeSessionName(title string) string {
	name := strings.ToLower(title)
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, name)
	// Collapse repeated hyphens and trim
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	name = strings.Trim(name, "-")
	if len(name) > 30 {
		name = name[:30]
	}
	if name == "" {
		name = "task"
	}
	return name
}

// ─── View ────────────────────────────────────────────────────────────────────

func (m tuiModel) View() string {
	if m.w == 0 {
		return "Loading..."
	}

	// Full-screen popup modes
	switch m.mode {
	case modePlaneIssues:
		return renderPlanePopup(m)
	case modeIcingaAlerts:
		return renderIcingaPopup(m)
	case modePopupAction:
		return renderActionPicker(m)
	}

	sidebar := m.renderSidebar()
	content := m.renderContent()

	main := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, content)

	var statusLine string
	switch m.mode {
	case modeNewName:
		statusLine = "Name: " + m.newNameInput.View()
	case modeNewDir:
		statusLine = "Dir: " + m.newDirInput.View()
	case modeContextPick:
		statusLine = m.renderContextPicker()
	default:
		if m.flash != "" {
			statusLine = dimStyle.Render(m.flash)
		} else {
			statusLine = dimStyle.Render("^A/^Z switch │ Alt+N new │ Alt+D delete │ Alt+P plane │ Alt+I icinga │ ^\\ quit")
		}
	}

	return lipgloss.JoinVertical(lipgloss.Left, main, statusLine)
}

func (m tuiModel) renderSidebar() string {
	var lines []string
	lines = append(lines, headerStyle.Render("SwarmOps"))
	lines = append(lines, "")

	// Track whether we've printed the pool separator
	printedPoolSep := false

	for i, item := range m.items {
		if item.kind == itemPoolSlot && !printedPoolSep {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, dimStyle.Render("─── Pool ───"))
			printedPoolSep = true
		}

		label := item.label
		if len(label) > 20 {
			label = label[:17] + "..."
		}

		line := fmt.Sprintf(" %s %s", item.indicator, label)

		// Show slot state inline for pool items
		if item.kind == itemPoolSlot {
			stateTag := item.state
			line = fmt.Sprintf(" %s %s %s", item.indicator, label, dimStyle.Render(stateTag))
		}

		if i == m.cursor {
			line = selectedStyle.Render(line)
		}
		lines = append(lines, line)
	}

	if len(m.items) == 0 {
		lines = append(lines, dimStyle.Render(" (no sessions)"))
	}

	for len(lines) < m.h-2 {
		lines = append(lines, "")
	}

	return sidebarStyle.Height(m.h - 2).Render(strings.Join(lines, "\n"))
}

// updateContentCache computes the right-pane content string based on current state
// and updates the viewport. Called from Update() handlers when state changes that
// affect the right pane. View() only reads from the viewport — no mutations.
func (m *tuiModel) updateContentCache() {
	if len(m.items) == 0 {
		m.contentCache = dimStyle.Render("\n  No sessions. Press Alt+N to create one.")
	} else if m.cursor < len(m.items) {
		item := m.items[m.cursor]
		switch item.kind {
		case itemPoolSlot:
			m.contentCache = m.renderPoolSlotDetail(item)
		case itemSession:
			if item.status != "running" {
				m.contentCache = dimStyle.Render("\n  Session stopped.")
			}
			// running sessions get contentCache set via terminalMsg
		}
	}

	if m.vpReady {
		m.vp.SetContent(m.contentCache)
	}
}

func (m tuiModel) renderContent() string {
	if !m.vpReady {
		return ""
	}
	return m.vp.View()
}

func (m tuiModel) renderPoolSlotDetail(item sidebarItem) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  Pool Slot: %s\n", item.slotID))
	b.WriteString(fmt.Sprintf("  Model:     %s\n", item.model))
	b.WriteString(fmt.Sprintf("  State:     %s\n", item.state))
	b.WriteString(fmt.Sprintf("  Alive:     %v\n", item.alive))
	b.WriteString(fmt.Sprintf("  Requests:  %d\n", item.requests))
	b.WriteString(fmt.Sprintf("  Cost:      $%.4f\n", item.costUSD))
	return b.String()
}

func (m tuiModel) renderContextPicker() string {
	var parts []string
	parts = append(parts, "Context: ")

	label := "(none)"
	if m.ctxCursor == 0 {
		label = selectedStyle.Render("> " + label)
	}
	parts = append(parts, label)

	for i, c := range m.contexts {
		label := c.Name
		if len(label) > 30 {
			label = label[:27] + "..."
		}
		if i+1 == m.ctxCursor {
			label = selectedStyle.Render("> " + label)
		}
		parts = append(parts, label)
	}

	return strings.Join(parts, " │ ")
}

// RunSwarmTUI starts the Bubbletea TUI.
func RunSwarmTUI() {
	database = initDatabase()
	defer database.Close()

	globalConfigService = newConfigService(database)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	initPool(ctx)

	p := tea.NewProgram(initialModel(), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}
