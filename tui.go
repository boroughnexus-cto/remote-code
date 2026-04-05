package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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

// ─── Messages ────────────────────────────────────────────────────────────────

type tickMsg time.Time
type sessionsMsg []Session
type terminalMsg string
type poolStatusMsg string
type contextListMsg []contextItem

type contextItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ─── Model ───────────────────────────────────────────────────────────────────

type tuiFocus int

const (
	focusSidebar tuiFocus = iota
	focusInput
	focusNewName
	focusNewDir
	focusContextPicker
)

type tuiModel struct {
	sessions []Session
	cursor   int
	focus    tuiFocus

	// Right pane
	vp      viewport.Model
	vpReady bool

	// Input for sending text to session
	chatInput textinput.Model

	// New session wizard
	newNameInput textinput.Model
	newDirInput  textinput.Model
	contexts     []contextItem
	ctxCursor    int

	// Terminal content
	termContent string

	// Pool status
	poolStatus string

	// Terminal size
	w, h int

	// Status message
	flash string
}

func initialModel() tuiModel {
	ci := textinput.New()
	ci.Placeholder = "Type to send to session..."
	ci.CharLimit = 4096

	ni := textinput.New()
	ni.Placeholder = "Session name"
	ni.CharLimit = 64

	di := textinput.New()
	di.Placeholder = "Working directory"
	di.CharLimit = 256
	di.SetValue(os.Getenv("HOME"))

	return tuiModel{
		focus:        focusSidebar,
		chatInput:    ci,
		newNameInput: ni,
		newDirInput:  di,
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(tickCmd(), loadSessions)
}

func tickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func loadSessions() tea.Msg {
	ctx := context.Background()
	refreshSessionStatuses(ctx)
	sessions, err := listSessions(ctx)
	if err != nil {
		return sessionsMsg(nil)
	}
	return sessionsMsg(sessions)
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

func loadPoolStatus() tea.Cmd {
	return func() tea.Msg {
		if globalPool == nil {
			return poolStatusMsg("Pool: disabled")
		}
		status := globalPool.Status()
		b, _ := json.MarshalIndent(status, "", "  ")
		return poolStatusMsg(string(b))
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

		// Parse response — mcp-context returns array of objects
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
		contentWidth := m.w - 26 // sidebar width + border
		if contentWidth < 20 {
			contentWidth = 20
		}
		contentHeight := m.h - 4 // header + input + status
		if contentHeight < 5 {
			contentHeight = 5
		}
		m.vp = viewport.New(contentWidth, contentHeight)
		m.vpReady = true
		return m, nil

	case tickMsg:
		var cmds []tea.Cmd
		cmds = append(cmds, tickCmd(), loadSessions)
		if len(m.sessions) > 0 && m.cursor < len(m.sessions) {
			s := m.sessions[m.cursor]
			if s.Hidden {
				cmds = append(cmds, loadPoolStatus())
			} else if s.Status == "running" {
				cmds = append(cmds, loadTerminal(s.TmuxSession))
			}
		}
		return m, tea.Batch(cmds...)

	case sessionsMsg:
		m.sessions = msg
		if m.cursor >= len(m.sessions) && len(m.sessions) > 0 {
			m.cursor = len(m.sessions) - 1
		}
		return m, nil

	case terminalMsg:
		m.termContent = string(msg)
		if m.vpReady {
			m.vp.SetContent(m.termContent)
			m.vp.GotoBottom()
		}
		return m, nil

	case poolStatusMsg:
		m.poolStatus = string(msg)
		return m, nil

	case contextListMsg:
		m.contexts = msg
		if len(m.contexts) > 0 {
			m.focus = focusContextPicker
			m.ctxCursor = 0
		} else {
			m.doSpawn(nil, nil)
			m.focus = focusSidebar
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch m.focus {
	case focusSidebar:
		switch key {
		case "ctrl+a", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "ctrl+z", "down":
			if m.cursor < len(m.sessions)-1 {
				m.cursor++
			}
		case "enter":
			m.focus = focusInput
			m.chatInput.Focus()
			return m, textinput.Blink
		case "n":
			m.focus = focusNewName
			m.newNameInput.SetValue("")
			m.newNameInput.Focus()
			m.flash = "New session: enter name"
			return m, textinput.Blink
		case "d":
			if len(m.sessions) > 0 && m.cursor < len(m.sessions) {
				s := m.sessions[m.cursor]
				deleteSession(context.Background(), s.ID)
				m.flash = fmt.Sprintf("Deleted %s", s.Name)
				return m, loadSessions
			}
		case "q", "ctrl+c":
			return m, tea.Quit
		}

	case focusInput:
		switch key {
		case "enter":
			text := m.chatInput.Value()
			if text != "" && len(m.sessions) > 0 && m.cursor < len(m.sessions) {
				s := m.sessions[m.cursor]
				if err := injectToSession(s.TmuxSession, text); err != nil {
					m.flash = "Error: " + err.Error()
				}
				m.chatInput.SetValue("")
			}
		case "esc":
			m.focus = focusSidebar
			m.chatInput.Blur()
			return m, nil
		default:
			var cmd tea.Cmd
			m.chatInput, cmd = m.chatInput.Update(msg)
			return m, cmd
		}

	case focusNewName:
		switch key {
		case "enter":
			if m.newNameInput.Value() != "" {
				m.focus = focusNewDir
				m.newDirInput.Focus()
				m.flash = "New session: enter directory"
				return m, textinput.Blink
			}
		case "esc":
			m.focus = focusSidebar
			m.flash = ""
			return m, nil
		default:
			var cmd tea.Cmd
			m.newNameInput, cmd = m.newNameInput.Update(msg)
			return m, cmd
		}

	case focusNewDir:
		switch key {
		case "enter":
			m.flash = "Fetching contexts..."
			return m, fetchContexts()
		case "esc":
			m.focus = focusSidebar
			m.flash = ""
			return m, nil
		default:
			var cmd tea.Cmd
			m.newDirInput, cmd = m.newDirInput.Update(msg)
			return m, cmd
		}

	case focusContextPicker:
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
				// "None" selected
				m.doSpawn(nil, nil)
			} else {
				c := m.contexts[m.ctxCursor-1]
				m.doSpawn(&c.ID, &c.Name)
			}
			m.focus = focusSidebar
			m.flash = ""
			return m, loadSessions
		case "esc":
			m.focus = focusSidebar
			m.flash = ""
			return m, nil
		}
	}

	return m, nil
}

func (m *tuiModel) doSpawn(contextID, contextName *string) {
	name := m.newNameInput.Value()
	dir := m.newDirInput.Value()
	if dir == "" {
		dir = os.Getenv("HOME")
	}
	s, err := spawnSession(context.Background(), name, dir, contextID, contextName)
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

	// Input bar at the bottom
	var inputBar string
	switch m.focus {
	case focusInput:
		inputBar = m.chatInput.View()
	case focusNewName:
		inputBar = "Name: " + m.newNameInput.View()
	case focusNewDir:
		inputBar = "Dir: " + m.newDirInput.View()
	case focusContextPicker:
		inputBar = m.renderContextPicker()
	default:
		inputBar = dimStyle.Render("Enter=type │ n=new │ d=delete │ q=quit")
	}

	// Status
	statusLine := ""
	if m.flash != "" {
		statusLine = dimStyle.Render(m.flash)
	}

	main := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, content)
	return lipgloss.JoinVertical(lipgloss.Left, main, inputBar, statusLine)
}

func (m tuiModel) renderSidebar() string {
	var lines []string
	lines = append(lines, headerStyle.Render("SwarmOps"))
	lines = append(lines, "")

	for i, s := range m.sessions {
		var indicator string
		if s.Hidden {
			indicator = statusAPI
		} else if s.Status == "running" {
			indicator = statusRunning
		} else {
			indicator = statusStopped
		}

		label := s.Name
		if s.Hidden {
			label = "[api] " + label
		}

		// Truncate to sidebar width
		if len(label) > 20 {
			label = label[:17] + "..."
		}

		line := fmt.Sprintf(" %s %s", indicator, label)
		if i == m.cursor {
			line = selectedStyle.Render(line)
		}
		lines = append(lines, line)
	}

	if len(m.sessions) == 0 {
		lines = append(lines, dimStyle.Render(" (no sessions)"))
	}

	// Pool status at bottom
	if globalPool != nil {
		lines = append(lines, "")
		lines = append(lines, dimStyle.Render("─── Pool ───"))
		status := globalPool.Status()
		if models, ok := status["models"].(map[string]interface{}); ok {
			for model, info := range models {
				if m, ok := info.(map[string]interface{}); ok {
					idle, _ := m["idle"].(float64)
					busy, _ := m["busy"].(float64)
					short := modelShortName(model)
					lines = append(lines, dimStyle.Render(fmt.Sprintf(" %s %d/%d", short, int(idle), int(idle+busy))))
				}
			}
		}
	}

	// Pad to fill height
	for len(lines) < m.h-2 {
		lines = append(lines, "")
	}

	return sidebarStyle.Height(m.h - 2).Render(strings.Join(lines, "\n"))
}

func (m tuiModel) renderContent() string {
	if !m.vpReady {
		return ""
	}

	if len(m.sessions) == 0 {
		m.vp.SetContent(dimStyle.Render("\n  No sessions. Press 'n' to create one."))
		return m.vp.View()
	}

	if m.cursor < len(m.sessions) {
		s := m.sessions[m.cursor]
		if s.Hidden {
			m.vp.SetContent(m.poolStatus)
		} else if s.Status != "running" {
			m.vp.SetContent(dimStyle.Render("\n  Session stopped."))
		}
		// termContent is set via terminalMsg for running sessions
	}

	return m.vp.View()
}

func (m tuiModel) renderContextPicker() string {
	var parts []string
	parts = append(parts, "Context: ")

	// Option 0: None
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
	// Initialize database for TUI mode
	database = initDatabase()
	defer database.Close()

	globalConfigService = newConfigService(database)

	// Start pool if enabled (for sidebar display)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	initPool(ctx)

	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}
