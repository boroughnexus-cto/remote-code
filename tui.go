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
)

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

	// Terminal size
	w, h int

	// Status message
	flash string
}

func initialModel() tuiModel {
	ni := textinput.New()
	ni.Placeholder = "Session name"
	ni.CharLimit = 64

	di := textinput.New()
	di.Placeholder = "Working directory"
	di.CharLimit = 256
	di.SetValue(os.Getenv("HOME"))

	return tuiModel{
		mode:         modePassthrough,
		newNameInput: ni,
		newDirInput:  di,
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
		return m, nil

	case terminalMsg:
		m.termContent = string(msg)
		if m.vpReady {
			m.vp.SetContent(m.termContent)
			m.vp.GotoBottom()
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
			}
			return m, nil
		case "ctrl+z":
			if m.cursor < len(m.items)-1 {
				m.cursor++
				m.flash = ""
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
	s, err := spawnSession(context.Background(), name, dir, contextID, contextName, "")
	if err != nil {
		m.flash = "Spawn error: " + err.Error()
	} else {
		m.flash = fmt.Sprintf("Spawned %s", s.Name)
	}
}

// ─── View ────────────────────────────────────────────────────────────────────

func (m tuiModel) View() string {
	if m.w == 0 {
		return "Loading..."
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
			statusLine = dimStyle.Render("^A/^Z switch │ Alt+N new │ Alt+D delete │ ^\\ quit")
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

func (m tuiModel) renderContent() string {
	if !m.vpReady {
		return ""
	}

	if len(m.items) == 0 {
		m.vp.SetContent(dimStyle.Render("\n  No sessions. Press Alt+N to create one."))
		return m.vp.View()
	}

	if m.cursor < len(m.items) {
		item := m.items[m.cursor]
		switch item.kind {
		case itemPoolSlot:
			m.vp.SetContent(m.renderPoolSlotDetail(item))
		case itemSession:
			if item.status != "running" {
				m.vp.SetContent(dimStyle.Render("\n  Session stopped."))
			}
			// running sessions get content set via terminalMsg
		}
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
