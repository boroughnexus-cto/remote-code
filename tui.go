package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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

	selectedLabelStyle = lipgloss.NewStyle().
				Bold(true).
				Underline(true).
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

	topBarStyle = lipgloss.NewStyle().
			BorderBottom(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("241")).
			Padding(0, 1)

	topBarTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#15a8a8"))
)

const headerHeight = 0 // top bar integrated into sidebar

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
	activity    string // "stopped", "working", "awaiting_input", "idle"
	mission     string // optional mission statement
	directory        string // working directory for session restart
	claudeSessionID string // Claude session ID for resume
	// Pool slot fields
	slotID   string
	model    string
	state    string // idle, busy, starting, dead
	requests int64
	costUSD  float64
	alive    bool
}

// ─── Messages ────────────────────────────────────────────────────────────────

type tickMsg time.Time         // fast animation tick (150ms)
type dataTickMsg time.Time     // slow data refresh tick (2s)
type activityTickMsg time.Time // activity detection tick (1s)
type flashClearMsg struct{}    // auto-clear flash message
type sessionsMsg []Session
type terminalMsg string
type contextListMsg []contextItem
type itemsMsg struct {
	items    []sidebarItem
	captures []sessionCapture
}
type activityCaptureMsg struct {
	captures []sessionCapture
}

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
	modeNewMission
	modeContextPick
	modePlaneIssues
	modeIcingaAlerts
	modePopupAction
	modeRename
	modeEditMission
	modeActionContext // context picker within action/dispatch flow
	modeFeedbackType
	modeFeedbackText
	modeAuditLog
)

// Spawner abstracts session creation for testability.
type Spawner interface {
	Spawn(ctx context.Context, name, dir string, contextID, contextName, mission *string, model string) (*Session, error)
}

// defaultSpawner calls the real spawnSession function.
type defaultSpawner struct{}

func (defaultSpawner) Spawn(ctx context.Context, name, dir string, contextID, contextName, mission *string, model string) (*Session, error) {
	return spawnSession(ctx, name, dir, contextID, contextName, mission, model)
}

type tuiModel struct {
	items  []sidebarItem
	cursor int
	mode   tuiMode

	// Right pane
	vp      viewport.Model
	vpReady bool

	// New session wizard
	newNameInput    textinput.Model
	newDirInput     textinput.Model
	newMissionInput textinput.Model
	contexts        []contextItem
	ctxCursor       int

	// Pool section display
	poolExpanded bool // expanded in sidebar; default false (collapsed, SWM-49)

	// Per-session activity state for diff detection and 1-tick damper
	activityStates    map[string]*activityState
	activityInflight  bool // true while a captureActivityCmd is running

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
	auditEvents    []ManagedSessionEvent
	auditScrollback string // scrollback for selected audit event's session
	popupErr       string
	popupCursor    int
	planeReqID     uint64 // incremented on each fetch; stale responses ignored
	icingaReqID    uint64

	// Popup filter & sort
	popupFilter       textinput.Model
	popupFilterActive bool
	popupSortMode     int  // 0=default, 1, 2 — meaning depends on popup type
	popupTriageMode   int  // Plane triage preset: 0=all, 1=started, 2=high+urgent, 3=backlog
	icingaGroupByHost bool // Icinga: group problems by host
	planeStates       map[string]string // state group → state ID for transitions

	// Action picker (modePopupAction)
	actionTarget    string        // display label for the selected item
	actionPrompt    string        // text to inject into the session
	actionPrevMode  tuiMode       // mode to return to on Esc
	actionSessions  []sidebarItem // running sessions to choose from
	actionCursor    int           // cursor in action picker (sessions + "new" option)
	actionChosenIdx int           // which session was chosen (-1 = not yet, len(actionSessions) = new)
	actionCtxCursor int           // cursor in context picker during dispatch

	// Scroll state
	userScrolled bool

	// Animation frame (cycles on tick)
	animFrame int

	// Rename session
	renameInput textinput.Model

	// Feedback submission
	feedbackInput    textinput.Model
	feedbackType     int // 0=bug, 1=feature
	feedbackSnapshot string  // TUI state captured at Alt+F press
	feedbackPrevMode tuiMode // mode to return to after feedback cancel

	// Dependency injection for testing
	spawner Spawner

	// HTTP client for backend API (client mode)
	api swarmClient
}

func initialModel(api swarmClient) tuiModel {
	ni := textinput.New()
	ni.Placeholder = "Session name"
	ni.CharLimit = 64

	di := textinput.New()
	di.Placeholder = "Working directory"
	di.CharLimit = 256
	di.SetValue(os.Getenv("HOME"))

	mi := textinput.New()
	mi.Placeholder = "Mission (optional, enter to skip)"
	mi.CharLimit = 256

	fi := textinput.New()
	fi.Placeholder = "filter..."
	fi.CharLimit = 128

	ri := textinput.New()
	ri.Placeholder = "New name"
	ri.CharLimit = 64

	fi2 := textinput.New()
	fi2.Placeholder = "Describe the bug or feature..."
	fi2.CharLimit = 256

	var spawner Spawner
	if api != nil {
		spawner = api
	} else {
		spawner = defaultSpawner{}
	}

	return tuiModel{
		mode:            modePassthrough,
		newNameInput:    ni,
		newDirInput:     di,
		newMissionInput: mi,
		popupFilter:     fi,
		renameInput:     ri,
		feedbackInput:   fi2,
		activityStates:  make(map[string]*activityState),
		spawner:         spawner,
		api:             api,
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(tickCmd(), dataTickCmd(), activityTickCmd(), loadItemsCmd(m.api))
}

func tickCmd() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func dataTickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return dataTickMsg(t)
	})
}

func activityTickCmd() tea.Cmd {
	return tea.Tick(1*time.Second, func(t time.Time) tea.Msg {
		return activityTickMsg(t)
	})
}

// captureActivityCmd captures tmux panes for all running sessions without reloading from DB.
func captureActivityCmd(items []sidebarItem) tea.Cmd {
	return func() tea.Msg {
		var captures []sessionCapture
		for _, item := range items {
			if item.kind == itemSession && item.status == "running" {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				out, err := exec.CommandContext(ctx, "tmux", "capture-pane", "-p", "-S", "-100", "-t", item.tmuxSession).Output()
				cancel()
				cap := sessionCapture{tmuxSession: item.tmuxSession, alive: err == nil}
				if err == nil {
					cap.capture = string(out)
				}
				captures = append(captures, cap)
			}
		}
		return activityCaptureMsg{captures: captures}
	}
}

func flashClearCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
		return flashClearMsg{}
	})
}

// sessionCapture holds raw tmux capture for a session, captured in the command goroutine.
// Classification happens in the Update loop where activityStates is safely accessed.
type sessionCapture struct {
	tmuxSession string
	capture     string // raw tmux capture-pane output (empty if stopped/failed)
	alive       bool   // whether tmux capture succeeded
}

// loadItemsCmd returns a tea.Cmd that builds the unified sidebar list.
// Captures raw tmux pane content but does NOT classify activity (that happens in Update
// via applyActivityClassification to avoid sharing the activityStates map with goroutines).
func loadItemsCmd(api swarmClient) tea.Cmd {
	return func() tea.Msg {
		var sessions []Session
		if api != nil {
			sessions, _ = api.listSessions()
		} else {
			ctx := context.Background()
			refreshSessionStatuses(ctx)
			sessions, _ = listSessions(ctx)
		}

		var items []sidebarItem
		var captures []sessionCapture

		for _, s := range sessions {
			activity := "stopped"
			indicator := statusStopped
			if s.Status == "running" {
				indicator = statusRunning
				// Capture tmux content in the goroutine (safe — no shared state)
				out, err := exec.Command("tmux", "capture-pane", "-p", "-S", "-100", "-t", s.TmuxSession).Output()
				cap := sessionCapture{tmuxSession: s.TmuxSession, alive: err == nil}
				if err == nil {
					cap.capture = string(out)
				}
				captures = append(captures, cap)
				activity = "pending" // placeholder — classified in Update
			}
			mission := ""
			if s.Mission != nil {
				mission = *s.Mission
			}
			items = append(items, sidebarItem{
				kind:        itemSession,
				label:       s.Name,
				indicator:   indicator,
				sessionID:   s.ID,
				tmuxSession: s.TmuxSession,
				status:      s.Status,
				activity:    activity,
				mission:     mission,
				directory:        s.Directory,
				claudeSessionID: func() string { if s.ClaudeSessionID != nil { return *s.ClaudeSessionID }; return "" }(),
			})
		}

		var poolData map[string]interface{}
		if api != nil {
			poolData, _ = api.poolStatus()
		} else if globalPool != nil {
			poolData = globalPool.Status()
		}

		if poolData != nil {
			if models, ok := poolData["models"].(map[string]interface{}); ok {
				// Sort model names for stable sidebar order
				modelNames := make([]string, 0, len(models))
				for name := range models {
					modelNames = append(modelNames, name)
				}
				sort.Strings(modelNames)
				for _, model := range modelNames {
					info := models[model]
					if minfo, ok := info.(map[string]interface{}); ok {
						// Handle slots as []interface{} (JSON unmarshal) or []map[string]interface{} (in-process)
						var slotMaps []map[string]interface{}
						if typed, ok := minfo["slots"].([]map[string]interface{}); ok {
							slotMaps = typed
						} else if raw, ok := minfo["slots"].([]interface{}); ok {
							for _, r := range raw {
								if m, ok := r.(map[string]interface{}); ok {
									slotMaps = append(slotMaps, m)
								}
							}
						}
						// Sort slots by ID for stable order
						sort.Slice(slotMaps, func(i, j int) bool {
							a, _ := slotMaps[i]["id"].(string)
							b, _ := slotMaps[j]["id"].(string)
							return a < b
						})
						for _, slot := range slotMaps {
							sid, _ := slot["id"].(string)
							state, _ := slot["state"].(string)
							// JSON numbers are float64; in-process are int64
							reqs := toInt64(slot["requests"])
							cost, _ := slot["cost_usd"].(float64)
							alive, _ := slot["alive"].(bool)

							ind := statusAPI
							if state == "starting" {
								ind = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffd700")).Render("↺")
							} else if !alive || state == "dead" {
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

		return itemsMsg{items: items, captures: captures}
	}
}

// toInt64 converts a value that may be int64 (in-process) or float64 (JSON) to int64.
func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	default:
		return 0
	}
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

// Context fetching and MCP client helpers are in mcp_client.go

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
		// Match sidebar: status line (2) + sidebar padding (2) + content header (2)
		contentHeight := m.h - headerHeight - 2 - 2 - 2
		if contentHeight < 5 {
			contentHeight = 5
		}
		m.vp = viewport.New(contentWidth, contentHeight)
		// Resize tmux sessions to viewport size (not including content header)
		go m.resizeTmuxSessions(contentWidth, contentHeight)
		m.vp.MouseWheelEnabled = true
		m.vpReady = true
		m.updateContentCache()
		return m, nil

	case tickMsg:
		// Fast tick (150ms): animation frames + terminal refresh only
		m.animFrame++
		var cmds []tea.Cmd
		cmds = append(cmds, tickCmd())
		if m.cursor < len(m.items) {
			item := m.items[m.cursor]
			if item.kind == itemSession && item.status == "running" {
				cmds = append(cmds, loadTerminal(item.tmuxSession))
			}
		}
		return m, tea.Batch(cmds...)

	case activityTickMsg:
		// 1s tick: capture tmux panes for activity classification only (no DB reload)
		// Skip if a previous capture is still in flight to prevent overlap/stale results
		if m.activityInflight {
			return m, activityTickCmd()
		}
		m.activityInflight = true
		return m, tea.Batch(activityTickCmd(), captureActivityCmd(m.items))

	case activityCaptureMsg:
		m.activityInflight = false
		// Classify activity from the 1s capture tick
		for i := range m.items {
			item := &m.items[i]
			if item.kind != itemSession || item.status != "running" {
				continue
			}
			for _, cap := range msg.captures {
				if cap.tmuxSession == item.tmuxSession {
					if !cap.alive {
						item.activity = "stopped"
					} else {
						st, ok := m.activityStates[cap.tmuxSession]
						if !ok {
							st = &activityState{}
							m.activityStates[cap.tmuxSession] = st
						}
						item.activity = classifyActivity(cap.capture, st)
					}
					break
				}
			}
		}
		return m, nil

	case dataTickMsg:
		// Slow tick (2s): HTTP data refresh (sessions, pool status)
		return m, tea.Batch(dataTickCmd(), loadItemsCmd(m.api))

	case flashClearMsg:
		m.flash = ""
		return m, nil

	case itemsMsg:
		// Classify activity in the Update loop (single-threaded) using captures from the command
		for i := range msg.items {
			item := &msg.items[i]
			if item.kind == itemSession && item.activity == "pending" {
				// Find matching capture
				for _, cap := range msg.captures {
					if cap.tmuxSession == item.tmuxSession {
						if !cap.alive {
							item.activity = "stopped"
						} else {
							st, ok := m.activityStates[cap.tmuxSession]
							if !ok {
								st = &activityState{}
								m.activityStates[cap.tmuxSession] = st
							}
							item.activity = classifyActivity(cap.capture, st)
						}
						break
					}
				}
			}
		}
		m.items = msg.items
		if m.cursor >= len(m.items) && len(m.items) > 0 {
			m.cursor = len(m.items) - 1
		}
		m.updateContentCache()
		// Resize tmux sessions to match content pane on data refresh
		if m.w > 0 {
			contentWidth := m.w - 26
			if contentWidth < 20 {
				contentWidth = 20
			}
			contentHeight := m.h - headerHeight - 2 - 2 - 2
			if contentHeight < 5 {
				contentHeight = 5
			}
			go m.resizeTmuxSessions(contentWidth, contentHeight)
		}
		return m, nil

	case terminalMsg:
		m.termContent = string(msg)
		m.contentCache = m.termContent
		if m.vpReady {
			m.vp.SetContent(m.contentCache)
			if !m.userScrolled {
				m.vp.GotoBottom()
			}
		}
		return m, nil

	case planeIssuesMsg:
		if m.mode == modePlaneIssues && msg.reqID == m.planeReqID {
			m.planeIssues = msg.issues
			m.popupErr = ""
			if m.popupCursor >= len(m.planeIssues) {
				m.popupCursor = max(0, len(m.planeIssues)-1)
			}
		}
		return m, nil

	case icingaProblemsMsg:
		if m.mode == modeIcingaAlerts && msg.reqID == m.icingaReqID {
			m.icingaProblems = msg.problems
			m.popupErr = ""
			if m.popupCursor >= len(m.icingaProblems) {
				m.popupCursor = max(0, len(m.icingaProblems)-1)
			}
		}
		return m, nil

	case auditEventsMsg:
		if m.mode == modeAuditLog {
			m.auditEvents = msg.events
			m.popupErr = ""
			m.popupCursor = 0
			// Fetch scrollback for the first event's session if any
			if len(msg.events) > 0 {
				return m, fetchAuditScrollback(msg.events[0].SessionID)
			}
		}
		return m, nil

	case auditScrollbackMsg:
		if m.mode == modeAuditLog {
			m.auditScrollback = msg.content
		}
		return m, nil

	case popupErrMsg:
		if (msg.source == "plane" && m.mode == modePlaneIssues && msg.reqID == m.planeReqID) ||
			(msg.source == "icinga" && m.mode == modeIcingaAlerts && msg.reqID == m.icingaReqID) {
			m.popupErr = msg.text
		}

	case planeStatesMsg:
		m.planeStates = msg.states
		return m, nil

	case popupActionDoneMsg:
		m.flash = msg.flash
		// Refresh data after write action
		if m.mode == modePlaneIssues {
			m.planeIssues = nil
			m.planeReqID++
			return m, tea.Batch(flashClearCmd(), fetchPlaneIssues(m.planeReqID, m.api))
		}
		if m.mode == modeIcingaAlerts {
			m.icingaProblems = nil
			m.icingaReqID++
			return m, tea.Batch(flashClearCmd(), fetchIcingaProblems(m.icingaReqID, m.api))
		}
		return m, flashClearCmd()

	case contextListMsg:
		m.contexts = msg
		if m.mode == modeActionContext {
			// Context list arrived for dispatch flow — stay in picker
			m.actionCtxCursor = 0
			return m, nil
		}
		// Spawn flow
		if len(m.contexts) > 0 {
			m.mode = modeContextPick
			m.ctxCursor = 0
		} else {
			m.doSpawn(nil, nil)
			m.mode = modePassthrough
		}
		return m, loadItemsCmd(m.api)

	case contextContentMsg:
		if msg.err != nil {
			m.flash = "Context error: " + msg.err.Error()
			// Dispatch without context on error
			m.doDispatch(m.actionPrompt)
		} else {
			// Prepend context content to prompt
			enrichedPrompt := msg.content + "\n\n---\n\n" + m.actionPrompt
			m.doDispatch(enrichedPrompt)
		}
		return m, loadItemsCmd(m.api)

	case tea.MouseMsg:
		if m.mode == modePassthrough && m.vpReady {
			oldOffset := m.vp.YOffset
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			if m.vp.YOffset != oldOffset {
				m.userScrolled = true
			}
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
		case "alt+a":
			if m.cursor > 0 {
				m.cursor--
				m.flash = ""
				m.userScrolled = false
				m.updateContentCache()
			}
			return m, nil
		case "alt+z":
			if m.cursor < len(m.items)-1 {
				m.cursor++
				m.flash = ""
				m.userScrolled = false
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
				if m.api != nil {
					m.api.deleteSession(item.sessionID)
				} else {
					deleteSession(context.Background(), item.sessionID)
				}
				m.flash = fmt.Sprintf("✓ Deleted %s", item.label)
				return m, tea.Batch(loadItemsCmd(m.api), flashClearCmd())
			}
			return m, nil
		case "alt+s":
			if m.cursor < len(m.items) && m.items[m.cursor].kind == itemSession {
				item := m.items[m.cursor]
				if item.tmuxSession != "" {
					if item.status == "running" {
						// Running: send Ctrl+C to interrupt
						exec.Command("tmux", "send-keys", "-t", item.tmuxSession, "C-c").Run()
						m.flash = fmt.Sprintf("Sent interrupt to %s (Alt+Shift+S to kill & restart)", item.label)
					} else {
						// Stopped: restart claude — recreate tmux session if needed
						if !isTmuxAlive(item.tmuxSession) {
							dir := item.directory
							if dir == "" {
								if h, err := os.UserHomeDir(); err == nil {
									dir = h
								}
							}
							cArgs := resumeClaudeCmd(item.claudeSessionID)
							args := append([]string{"new-session", "-d", "-s", item.tmuxSession, "-c", dir, "-x", "200", "-y", "50", "--"}, cArgs...)
							if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
								m.flash = fmt.Sprintf("Failed to recreate tmux session: %s", strings.TrimSpace(string(out)))
								return m, flashClearCmd()
							}
						}
						m.flash = fmt.Sprintf("Resuming Claude in %s", item.label)
					}
				}
				return m, flashClearCmd()
			}
			return m, nil
		case "alt+S": // Shift variant — kill and restart claude in the session
			if m.cursor < len(m.items) && m.items[m.cursor].kind == itemSession {
				item := m.items[m.cursor]
				if item.tmuxSession != "" {
					// Kill all processes in the tmux pane, then restart claude
					exec.Command("tmux", "send-keys", "-t", item.tmuxSession, "C-c").Run()
					time.Sleep(200 * time.Millisecond)
					exec.Command("tmux", "send-keys", "-t", item.tmuxSession, "C-c").Run()
					time.Sleep(200 * time.Millisecond)
					exec.Command("tmux", "send-keys", "-t", item.tmuxSession, "exit", "Enter").Run()
					time.Sleep(500 * time.Millisecond)
					exec.Command("tmux", "send-keys", "-t", item.tmuxSession, "claude --dangerously-skip-permissions", "Enter").Run() // Alt+Shift+S: fresh start
					m.flash = fmt.Sprintf("Restarted Claude in %s", item.label)
				}
				return m, nil
			}
			return m, nil
		case "alt+r":
			if m.cursor < len(m.items) && m.items[m.cursor].kind == itemSession {
				m.mode = modeRename
				m.renameInput.SetValue(m.items[m.cursor].label)
				m.renameInput.Focus()
				m.flash = "Rename session (esc to cancel)"
				return m, textinput.Blink
			}
			return m, nil
		case "alt+m":
			if m.cursor < len(m.items) && m.items[m.cursor].kind == itemSession {
				m.mode = modeEditMission
				m.newMissionInput.SetValue(m.items[m.cursor].mission)
				m.newMissionInput.Focus()
				m.flash = "Edit mission (esc to cancel, enter to save)"
				return m, textinput.Blink
			}
			return m, nil
		case "alt+f":
			// Capture TUI state before switching to feedback mode
			m.feedbackSnapshot = m.View()
			m.feedbackPrevMode = modePassthrough
			m.mode = modeFeedbackType
			m.feedbackType = 0
			m.flash = "Feedback: ←/→ Bug or Feature, Enter to continue, Esc to cancel"
			return m, nil
		case "alt+o":
			m.poolExpanded = !m.poolExpanded
			return m, nil
		case "alt+p":
			m.mode = modePlaneIssues
			m.planeIssues = nil
			m.popupErr = ""
			m.popupCursor = 0
			m.flash = ""
			m.planeReqID++
			cmds := []tea.Cmd{fetchPlaneIssues(m.planeReqID, m.api)}
			if m.planeStates == nil {
				cmds = append(cmds, fetchPlaneStates(m.api))
			}
			return m, tea.Batch(cmds...)
		case "alt+i":
			m.mode = modeIcingaAlerts
			m.icingaProblems = nil
			m.popupErr = ""
			m.popupCursor = 0
			m.flash = ""
			m.icingaReqID++
			return m, fetchIcingaProblems(m.icingaReqID, m.api)
		case "alt+q":
			return m, tea.Quit
		case "alt+l":
			m.mode = modeAuditLog
			m.auditEvents = nil
			m.auditScrollback = ""
			m.popupCursor = 0
			m.popupErr = ""
			return m, fetchAuditEvents(m.api)
		case "alt+w":
			// Close the Plane issue referenced in the current session's name (SWM-26)
			if m.cursor < len(m.items) && m.items[m.cursor].kind == itemSession {
				label := m.items[m.cursor].label
				m.flash = "Closing Plane issue for session..."
				return m, planeCloseSessionIssue(label, m.api)
			}
			return m, nil
		case "alt+b":
			// Snap viewport to bottom and resume auto-scroll
			if m.vpReady {
				m.userScrolled = false
				m.vp.GotoBottom()
			}
			return m, nil
		default:
			// Only pass keys to session items (not pool slots)
			if m.cursor < len(m.items) && m.items[m.cursor].kind == itemSession {
				m.sendKeyToSession(key)
				// Immediately refresh terminal content after sending a key
				return m, loadTerminal(m.items[m.cursor].tmuxSession)
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
			m.mode = modeNewMission
			m.newMissionInput.SetValue("")
			m.newMissionInput.Focus()
			m.flash = "Mission statement (optional, enter to skip)"
			return m, textinput.Blink
		case "esc":
			m.mode = modePassthrough
			m.flash = ""
		case "tab":
			// Directory tab-completion
			val := m.newDirInput.Value()
			if val != "" {
				matches, _ := filepath.Glob(val + "*")
				if len(matches) == 1 {
					// Single match — complete it
					completed := matches[0]
					info, err := os.Stat(completed)
					if err == nil && info.IsDir() {
						completed += "/"
					}
					m.newDirInput.SetValue(completed)
					m.newDirInput.SetCursor(len(completed))
				} else if len(matches) > 1 {
					// Multiple matches — find common prefix
					prefix := matches[0]
					for _, match := range matches[1:] {
						for i := 0; i < len(prefix) && i < len(match); i++ {
							if prefix[i] != match[i] {
								prefix = prefix[:i]
								break
							}
						}
						if len(match) < len(prefix) {
							prefix = prefix[:len(match)]
						}
					}
					if len(prefix) > len(val) {
						m.newDirInput.SetValue(prefix)
						m.newDirInput.SetCursor(len(prefix))
					}
					// Show matches in flash
					var names []string
					for _, match := range matches {
						names = append(names, filepath.Base(match))
					}
					m.flash = strings.Join(names, "  ")
				}
			}
			return m, nil
		default:
			var cmd tea.Cmd
			m.newDirInput, cmd = m.newDirInput.Update(msg)
			return m, cmd
		}

	case modeNewMission:
		switch key {
		case "enter":
			m.flash = "Fetching contexts..."
			return m, fetchContexts()
		case "esc":
			m.mode = modePassthrough
			m.flash = ""
		default:
			var cmd tea.Cmd
			m.newMissionInput, cmd = m.newMissionInput.Update(msg)
			return m, cmd
		}

	case modeRename:
		switch key {
		case "enter":
			newName := m.renameInput.Value()
			if newName != "" && m.cursor < len(m.items) {
				item := m.items[m.cursor]
				if m.api != nil {
					m.api.renameSession(item.sessionID, newName)
				} else {
					renameSession(context.Background(), item.sessionID, newName)
				}
				m.flash = fmt.Sprintf("Renamed to %s", newName)
			}
			m.mode = modePassthrough
			return m, loadItemsCmd(m.api)
		case "esc":
			m.mode = modePassthrough
			m.flash = ""
		default:
			var cmd tea.Cmd
			m.renameInput, cmd = m.renameInput.Update(msg)
			return m, cmd
		}

	case modeEditMission:
		switch key {
		case "enter":
			mission := m.newMissionInput.Value()
			if m.cursor < len(m.items) {
				item := m.items[m.cursor]
				if m.api != nil {
					m.api.setMission(item.sessionID, mission)
				} else {
					updateSessionMission(context.Background(), item.sessionID, mission)
				}
				if mission == "" {
					m.flash = "Mission cleared"
				} else {
					m.flash = "Mission updated"
				}
			}
			m.mode = modePassthrough
			return m, tea.Batch(loadItemsCmd(m.api), flashClearCmd())
		case "esc":
			m.mode = modePassthrough
			m.flash = ""
		default:
			var cmd tea.Cmd
			m.newMissionInput, cmd = m.newMissionInput.Update(msg)
			return m, cmd
		}

	case modeFeedbackType:
		switch key {
		case "left", "right":
			m.feedbackType = 1 - m.feedbackType
		case "enter":
			m.mode = modeFeedbackText
			m.feedbackInput.SetValue("")
			m.feedbackInput.Focus()
			kinds := []string{"bug", "feature"}
			m.flash = fmt.Sprintf("Describe the %s (Enter to submit, Esc to cancel)", kinds[m.feedbackType])
			return m, textinput.Blink
		case "esc":
			m.mode = m.feedbackPrevMode
			m.flash = ""
		}

	case modeFeedbackText:
		switch key {
		case "enter":
			summary := m.feedbackInput.Value()
			if summary != "" {
				kinds := []string{"bug", "feature"}
				go submitFeedback(kinds[m.feedbackType], summary, m.api, m.feedbackSnapshot)
				m.flash = fmt.Sprintf("✓ Submitted %s: %s", kinds[m.feedbackType], summary)
				m.mode = m.feedbackPrevMode
				return m, flashClearCmd()
			}
			m.mode = m.feedbackPrevMode
			return m, nil
		case "esc":
			m.mode = m.feedbackPrevMode
			m.flash = ""
		default:
			var cmd tea.Cmd
			m.feedbackInput, cmd = m.feedbackInput.Update(msg)
			return m, cmd
		}
	case modeContextPick:
		switch key {
		case "alt+a", "up":
			if m.ctxCursor > 0 {
				m.ctxCursor--
			}
		case "alt+z", "down":
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
			return m, loadItemsCmd(m.api)
		case "esc":
			m.mode = modePassthrough
			m.flash = ""
		}

	case modePlaneIssues:
		filtered := filteredPlaneIssues(m)
		r := handlePopupKeyShared(&m, msg, len(filtered), planeSortLabels)
		if r.action == "enter" {
			if m.popupCursor < len(filtered) {
				issue := filtered[m.popupCursor]
				m.openActionPicker(issue.Identifier+" "+issue.Title, planeIssuePrompt(issue), modePlaneIssues)
			}
		} else if r.action == "refresh" {
			m.planeIssues = nil
			m.planeReqID++
			return m, fetchPlaneIssues(m.planeReqID, m.api)
		}
		if r.handled {
			return m, r.cmd
		}
		// Plane-specific keys (not handled by shared handler)
		switch key {
		case "p": // set In Progress
			if m.popupCursor < len(filtered) {
				if m.planeStates == nil {
					m.flash = "Loading states..."
					return m, fetchPlaneStates(m.api)
				}
				issue := filtered[m.popupCursor]
				if stateID, ok := m.planeStates["started"]; ok {
					m.flash = "Setting In Progress..."
					return m, planeUpdateIssue(m.api, issue.ID, map[string]interface{}{"state": stateID})
				}
				m.flash = "No 'started' state found"
			}
		case "d": // set Done
			if m.popupCursor < len(filtered) {
				if m.planeStates == nil {
					m.flash = "Loading states..."
					return m, fetchPlaneStates(m.api)
				}
				issue := filtered[m.popupCursor]
				if stateID, ok := m.planeStates["completed"]; ok {
					m.flash = "Setting Done..."
					return m, planeUpdateIssue(m.api, issue.ID, map[string]interface{}{"state": stateID})
				}
				m.flash = "No 'completed' state found"
			}
		case "1":
			m.popupTriageMode = 1
			m.popupCursor = 0
		case "2":
			m.popupTriageMode = 2
			m.popupCursor = 0
		case "3":
			m.popupTriageMode = 3
			m.popupCursor = 0
		case "0":
			m.popupTriageMode = 0
			m.popupCursor = 0
		}

	case modeAuditLog:
		switch key {
		case "esc", "alt+l":
			m.mode = modePassthrough
		case "alt+a", "up", "k":
			if m.popupCursor > 0 {
				m.popupCursor--
				if len(m.auditEvents) > 0 {
					return m, fetchAuditScrollback(m.auditEvents[m.popupCursor].SessionID)
				}
			}
		case "alt+z", "down", "j":
			if m.popupCursor < len(m.auditEvents)-1 {
				m.popupCursor++
				if len(m.auditEvents) > 0 {
					return m, fetchAuditScrollback(m.auditEvents[m.popupCursor].SessionID)
				}
			}
		case "r":
			m.auditEvents = nil
			m.auditScrollback = ""
			m.popupCursor = 0
			m.popupErr = ""
			return m, fetchAuditEvents(m.api)
		}

	case modeIcingaAlerts:
		filtered := filteredIcingaProblems(m)
		r := handlePopupKeyShared(&m, msg, len(filtered), icingaSortLabels)
		if r.action == "enter" {
			if m.popupCursor < len(filtered) {
				problem := filtered[m.popupCursor]
				label := fmt.Sprintf("%s / %s", problem.Host, problem.Service)
				m.openActionPicker(label, icingaProblemPrompt(problem), modeIcingaAlerts)
			}
		} else if r.action == "refresh" {
			m.icingaProblems = nil
			m.icingaReqID++
			return m, fetchIcingaProblems(m.icingaReqID, m.api)
		}
		if r.handled {
			return m, r.cmd
		}
		// Icinga-specific keys
		switch key {
		case "a": // acknowledge
			if m.popupCursor < len(filtered) {
				problem := filtered[m.popupCursor]
				if problem.Acknowledged {
					m.flash = "Already acknowledged"
				} else {
					m.flash = "Acknowledging..."
					return m, icingaAcknowledge(m.api, problem.ObjectName, "Acknowledged from SwarmOps TUI")
				}
			}
		case "t": // schedule downtime (30m)
			if m.popupCursor < len(filtered) {
				problem := filtered[m.popupCursor]
				m.flash = "Scheduling 30m downtime..."
				return m, icingaScheduleDowntime(m.api, problem.ObjectName, 30*time.Minute, "Downtime from SwarmOps TUI (30m)")
			}
		case "T": // schedule downtime (2h)
			if m.popupCursor < len(filtered) {
				problem := filtered[m.popupCursor]
				m.flash = "Scheduling 2h downtime..."
				return m, icingaScheduleDowntime(m.api, problem.ObjectName, 2*time.Hour, "Downtime from SwarmOps TUI (2h)")
			}
		case "g": // toggle group by host
			m.icingaGroupByHost = !m.icingaGroupByHost
			m.popupCursor = 0
		}

	case modePopupAction:
		maxIdx := len(m.actionSessions) // last index is "new session"
		switch key {
		case "esc":
			m.mode = m.actionPrevMode
		case "alt+a", "up":
			if m.actionCursor > 0 {
				m.actionCursor--
			}
		case "alt+z", "down":
			if m.actionCursor < maxIdx {
				m.actionCursor++
			}
		case "enter":
			// Remember which session was chosen, then show context picker
			m.actionChosenIdx = m.actionCursor
			m.actionCtxCursor = 0
			m.mode = modeActionContext
			m.flash = "Add context? (↑↓ to pick, Enter to confirm, Esc to skip)"
			if m.contexts == nil {
				return m, fetchContexts()
			}
		}

	case modeActionContext:
		switch key {
		case "alt+a", "up":
			if m.actionCtxCursor > 0 {
				m.actionCtxCursor--
			}
		case "alt+z", "down":
			if m.actionCtxCursor < len(m.contexts) {
				m.actionCtxCursor++
			}
		case "esc":
			// Skip context — dispatch with raw prompt
			m.doDispatch(m.actionPrompt)
		case "enter":
			if m.actionCtxCursor == 0 {
				// "(none)" selected — dispatch without context
				m.doDispatch(m.actionPrompt)
			} else {
				// Fetch context content, then dispatch
				ctx := m.contexts[m.actionCtxCursor-1]
				m.flash = fmt.Sprintf("Loading %s context...", ctx.Name)
				return m, fetchContextContent(ctx.Name)
			}
		}
	}

	return m, nil
}

// sendKeyToSession translates a Bubbletea key string to tmux send-keys.
// resizeTmuxSessions resizes all tmux session windows to match the TUI content pane.
func (m *tuiModel) resizeTmuxSessions(width, height int) {
	for _, item := range m.items {
		if item.kind == itemSession && item.tmuxSession != "" {
			exec.Command("tmux", "resize-window", "-t", item.tmuxSession,
				"-x", fmt.Sprintf("%d", width), "-y", fmt.Sprintf("%d", height)).Run()
		}
	}
}

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
	var mission *string
	if v := m.newMissionInput.Value(); v != "" {
		mission = &v
	}
	s, err := m.spawner.Spawn(context.Background(), name, dir, contextID, contextName, mission, "")
	if err != nil {
		m.flash = "Spawn error: " + err.Error()
	} else {
		m.flash = fmt.Sprintf("Spawned %s", s.Name)
	}
}

// doDispatch executes the dispatch action: sends prompt to a session or spawns a new one.
// Called after the context picker step. Uses m.actionChosenIdx to determine target.
func (m *tuiModel) doDispatch(prompt string) {
	if m.actionChosenIdx < len(m.actionSessions) {
		// Inject into existing session
		sess := m.actionSessions[m.actionChosenIdx]
		if sess.status == "running" {
			injectToSession(sess.tmuxSession, prompt)
			m.flash = fmt.Sprintf("Sent to %s", sess.label)
		}
	} else {
		// Spawn new session
		name := sanitizeSessionName(m.actionTarget)
		s, err := m.spawner.Spawn(context.Background(), name, os.Getenv("HOME"), nil, nil, nil, "")
		if err != nil {
			m.flash = "Spawn error: " + err.Error()
		} else {
			go func() {
				time.Sleep(2 * time.Second)
				injectToSession(s.TmuxSession, prompt)
			}()
			m.flash = fmt.Sprintf("Spawned %s — injecting task", s.Name)
		}
	}
	m.mode = modePassthrough
}

// popupKeyResult holds the result of shared popup key handling.
type popupKeyResult struct {
	handled bool
	cmd     tea.Cmd
	action  string // "enter", "refresh", or ""
}

// handlePopupKeyShared processes keys common to all popup modes.
// Returns the action taken so the caller can perform popup-specific work.
func handlePopupKeyShared(m *tuiModel, msg tea.KeyMsg, filteredLen int, sortLabels []string) popupKeyResult {
	key := msg.String()

	if m.popupFilterActive {
		switch key {
		case "esc":
			m.popupFilterActive = false
			m.popupFilter.Blur()
			m.popupFilter.SetValue("")
			m.popupCursor = 0
			return popupKeyResult{handled: true}
		case "enter":
			m.popupFilterActive = false
			m.popupFilter.Blur()
			m.popupCursor = 0
			return popupKeyResult{handled: true}
		default:
			var cmd tea.Cmd
			m.popupFilter, cmd = m.popupFilter.Update(msg)
			m.popupCursor = 0
			return popupKeyResult{handled: true, cmd: cmd}
		}
	}

	switch key {
	case "q", "esc":
		m.mode = modePassthrough
		m.popupErr = ""
		m.popupFilter.SetValue("")
		m.popupSortMode = 0
		m.popupTriageMode = 0
		m.icingaGroupByHost = false
		return popupKeyResult{handled: true}
	case "alt+a", "up":
		if filteredLen > 0 && m.popupCursor > 0 {
			m.popupCursor--
		}
		return popupKeyResult{handled: true}
	case "alt+z", "down":
		if filteredLen > 0 && m.popupCursor < filteredLen-1 {
			m.popupCursor++
		}
		return popupKeyResult{handled: true}
	case "pgup":
		m.popupCursor -= 10
		if m.popupCursor < 0 {
			m.popupCursor = 0
		}
		return popupKeyResult{handled: true}
	case "pgdown":
		m.popupCursor += 10
		if m.popupCursor >= filteredLen {
			m.popupCursor = filteredLen - 1
		}
		if m.popupCursor < 0 {
			m.popupCursor = 0
		}
		return popupKeyResult{handled: true}
	case "home":
		m.popupCursor = 0
		return popupKeyResult{handled: true}
	case "end":
		if filteredLen > 0 {
			m.popupCursor = filteredLen - 1
		}
		return popupKeyResult{handled: true}
	case "/":
		m.popupFilterActive = true
		m.popupFilter.Focus()
		return popupKeyResult{handled: true, cmd: textinput.Blink}
	case "s":
		m.popupSortMode = (m.popupSortMode + 1) % len(sortLabels)
		m.popupCursor = 0
		return popupKeyResult{handled: true}
	case "enter":
		if filteredLen > 0 {
			return popupKeyResult{handled: true, action: "enter"}
		}
		return popupKeyResult{handled: true}
	case "r":
		m.popupErr = ""
		m.popupCursor = 0
		return popupKeyResult{handled: true, action: "refresh"}
	case "alt+f":
		// Capture current popup view before switching to feedback
		switch m.mode {
		case modePlaneIssues:
			m.feedbackSnapshot = renderPlanePopup(*m)
		case modeIcingaAlerts:
			m.feedbackSnapshot = renderIcingaPopup(*m)
		default:
			m.feedbackSnapshot = ""
		}
		m.feedbackPrevMode = m.mode
		m.mode = modeFeedbackType
		m.feedbackType = 0
		m.flash = "Feedback: ←/→ Bug or Feature, Enter to continue, Esc to cancel"
		return popupKeyResult{handled: true}
	}
	return popupKeyResult{}
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
	case modeAuditLog:
		return renderAuditPopup(m)
	case modePopupAction:
		return renderActionPicker(m)
	case modeActionContext:
		return renderDispatchContextPicker(m)
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
	case modeNewMission:
		statusLine = "Mission: " + m.newMissionInput.View()
	case modeEditMission:
		statusLine = "Mission: " + m.newMissionInput.View()
	case modeRename:
		statusLine = "Rename: " + m.renameInput.View()
	case modeFeedbackType:
		kinds := []string{"🐛 Bug", "✨ Feature"}
		var parts []string
		for i, k := range kinds {
			if i == m.feedbackType {
				parts = append(parts, selectedStyle.Render("> "+k))
			} else {
				parts = append(parts, dimStyle.Render("  "+k))
			}
		}
		statusLine = "Feedback: " + strings.Join(parts, "  ")
	case modeFeedbackText:
		statusLine = "Feedback: " + m.feedbackInput.View()
	case modeContextPick:
		statusLine = m.renderContextPicker()
	default:
		if m.flash != "" {
			statusLine = dimStyle.Render(m.flash)
		} else {
			statusLine = dimStyle.Render("Alt+A/Z nav │ Alt+N new │ Alt+S start/stop │ Alt+R rename │ Alt+M mission │ Alt+D delete") + "\n" +
				dimStyle.Render("Alt+P plane │ Alt+I icinga │ Alt+L audit │ Alt+W close issue │ Alt+O pool │ Alt+F feedback │ Alt+Q quit")
		}
	}

	return lipgloss.JoinVertical(lipgloss.Left, main, statusLine)
}

func (m tuiModel) renderTopBar() string {
	barWidth := m.w - 2
	innerWidth := barWidth - 2 // account for Padding(0, 1) left+right

	// Line 1: SwarmOps (left) + time (right)
	title := topBarTitleStyle.Render("SwarmOps")
	ts := dimStyle.Render(time.Now().Format("15:04:05"))
	gap1 := innerWidth - lipgloss.Width(title) - lipgloss.Width(ts)
	if gap1 < 1 {
		gap1 = 1
	}
	line1 := title + strings.Repeat(" ", gap1) + ts

	// Line 2: session/pool summary
	running := 0
	stopped := 0
	poolSlots := 0
	poolAlive := 0
	for _, item := range m.items {
		switch item.kind {
		case itemSession:
			if item.status == "running" {
				running++
			} else {
				stopped++
			}
		case itemPoolSlot:
			poolSlots++
			if item.alive {
				poolAlive++
			}
		}
	}
	var parts []string
	if running > 0 || stopped > 0 {
		parts = append(parts, fmt.Sprintf("%d sessions (%d running)", running+stopped, running))
	}
	if poolSlots > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d pool slots", poolAlive, poolSlots))
	} else {
		parts = append(parts, "pool off")
	}
	line2 := dimStyle.Render(strings.Join(parts, "  ·  "))

	content := line1 + "\n" + line2
	return topBarStyle.Width(barWidth).Render(content)
}

func (m tuiModel) renderSidebar() string {
	var lines []string

	// Top header: SwarmOps + time
	ts := time.Now().Format("15:04:05")
	titleLine := topBarTitleStyle.Render("SwarmOps")
	gap := 22 - lipgloss.Width(titleLine) - len(ts) // 22 = sidebar inner width
	if gap < 1 {
		gap = 1
	}
	lines = append(lines, titleLine+strings.Repeat(" ", gap)+dimStyle.Render(ts))

	// Summary line
	running := 0
	stopped := 0
	poolAlive := 0
	poolTotal := 0
	for _, item := range m.items {
		switch item.kind {
		case itemSession:
			if item.status == "running" {
				running++
			} else {
				stopped++
			}
		case itemPoolSlot:
			poolTotal++
			if item.alive {
				poolAlive++
			}
		}
	}
	var summary []string
	if running+stopped > 0 {
		summary = append(summary, fmt.Sprintf("%d sess", running+stopped))
	}
	if poolTotal > 0 {
		summary = append(summary, fmt.Sprintf("%d/%d pool", poolAlive, poolTotal))
	}
	lines = append(lines, dimStyle.Render(strings.Join(summary, " · ")))
	lines = append(lines, dimStyle.Render("────────────────────"))
	lines = append(lines, "")

	// Render session items
	for i, item := range m.items {
		if item.kind == itemPoolSlot {
			continue // pool rendered separately below
		}
		label := item.label
		if len(label) > 20 {
			label = label[:17] + "..."
		}
		ind := animatedIndicator(item.activity, m.animFrame)
		if i == m.cursor {
			line := fmt.Sprintf(" %s %s", ind, selectedLabelStyle.Render(label))
			lines = append(lines, selectedStyle.Render("▸")+line)
		} else {
			lines = append(lines, fmt.Sprintf("  %s %s", ind, label))
		}
	}

	// Pool section — collapsible (SWM-49)
	if poolTotal > 0 {
		lines = append(lines, "")
		busyCount := 0
		for _, item := range m.items {
			if item.kind == itemPoolSlot && item.state == "busy" {
				busyCount++
			}
		}
		busyStr := ""
		if busyCount > 0 {
			busyStr = fmt.Sprintf(" (%d)", busyCount)
		}
		toggleChar := "▶"
		if m.poolExpanded {
			toggleChar = "▼"
		}
		poolHeader := fmt.Sprintf("%s Pool %d/%d%s", toggleChar, poolAlive, poolTotal, busyStr)
		lines = append(lines, dimStyle.Render(poolHeader))

		if m.poolExpanded {
			for i, item := range m.items {
				if item.kind != itemPoolSlot {
					continue
				}
				label := item.label
				if len(label) > 20 {
					label = label[:17] + "..."
				}
				if i == m.cursor {
					line := fmt.Sprintf(" %s %s %s", item.indicator, selectedLabelStyle.Render(label), dimStyle.Render(item.state))
					lines = append(lines, selectedStyle.Render("▸")+line)
				} else {
					lines = append(lines, fmt.Sprintf("  %s %s %s", item.indicator, label, dimStyle.Render(item.state)))
				}
			}
		}
	}

	if len(m.items) == 0 {
		lines = append(lines, dimStyle.Render(" (no sessions)"))
	}

	for len(lines) < m.h-3 {
		lines = append(lines, "")
	}

	// Height accounts for: top bar (headerHeight), status line (2), sidebar vertical padding (2)
	sideHeight := m.h - headerHeight - 2 - 2
	if sideHeight < 3 {
		sideHeight = 3
	}
	return sidebarStyle.Height(sideHeight).Render(strings.Join(lines, "\n"))
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
	contentWidth := m.w - 26
	if contentWidth < 20 {
		contentWidth = 20
	}

	// Header: session/slot name + status on line 1, separator on line 2
	var headerLine string
	if m.cursor < len(m.items) {
		item := m.items[m.cursor]
		switch item.kind {
		case itemSession:
			status := dimStyle.Render("stopped")
			if item.status == "running" {
				status = lipgloss.NewStyle().Foreground(lipgloss.Color("#00ff00")).Render("running")
			}
			headerLine = topBarTitleStyle.Render(item.label) + "  " + status
			if item.mission != "" {
				headerLine += "\n" + dimStyle.Render(item.mission)
			}
		case itemPoolSlot:
			state := dimStyle.Render(item.state)
			if item.state == "idle" {
				state = lipgloss.NewStyle().Foreground(lipgloss.Color("#00ff00")).Render("idle")
			} else if item.state == "busy" {
				state = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffaa00")).Render("busy")
			}
			headerLine = topBarTitleStyle.Render(item.model) + "  " + state
		}
	}
	if headerLine == "" {
		headerLine = dimStyle.Render("No selection")
	}

	sep := dimStyle.Render(strings.Repeat("─", contentWidth))
	header := headerLine + "\n" + sep + "\n"

	return lipgloss.NewStyle().Width(contentWidth).Render(header + m.vp.View())
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
	// Build all options: 0=(none), i+1=contexts[i]
	total := len(m.contexts) + 1
	rawLabels := make([]string, total)
	rawLabels[0] = "(none)"
	for i, c := range m.contexts {
		rawLabels[i+1] = c.Name
	}

	// Sliding window of 4 around cursor
	const windowSize = 4
	start := m.ctxCursor - windowSize/2
	if start < 0 {
		start = 0
	}
	end := start + windowSize
	if end > total {
		end = total
		if start = end - windowSize; start < 0 {
			start = 0
		}
	}

	// Calculate max label length that fits within terminal width.
	// Budget: prefix + arrows (2) + separators (3 each between items) + 2 brackets on selected.
	prefix := fmt.Sprintf("Context: (%d/%d) ", m.ctxCursor, total-1)
	arrowBudget := 0
	if start > 0 {
		arrowBudget += 4 // "← │ "
	}
	if end < total {
		arrowBudget += 4 // " │ →"
	}
	nItems := end - start
	sepBudget := 3 * (nItems - 1) // " │ " between items
	bracketBudget := 2             // "[" + "]" around selected item
	available := m.w - len(prefix) - arrowBudget - sepBudget - bracketBudget
	maxLabelLen := available / nItems
	if maxLabelLen < 8 {
		maxLabelLen = 8 // minimum readable length
	}

	// Truncate labels to fit
	labels := make([]string, total)
	for i, lbl := range rawLabels {
		if len(lbl) > maxLabelLen {
			cut := maxLabelLen - 3
			if cut < 1 {
				cut = 1
			}
			labels[i] = lbl[:cut] + "..."
		} else {
			labels[i] = lbl
		}
	}

	var parts []string
	if start > 0 {
		parts = append(parts, dimStyle.Render("←"))
	}
	for i := start; i < end; i++ {
		lbl := labels[i]
		if i == m.ctxCursor {
			lbl = selectedStyle.Render("[" + lbl + "]")
		}
		parts = append(parts, lbl)
	}
	if end < total {
		parts = append(parts, dimStyle.Render("→"))
	}

	return prefix + strings.Join(parts, " │ ")
}

// runTUI starts the Bubbletea TUI. Database, config, and pool must be initialised by main().

// stripANSI removes ANSI escape sequences from a string.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// ─── Activity detector — prioritized classifier ─────────────────────────────
//
// States: "stopped" | "awaiting_input" | "working" | "idle"
// Priority order (highest wins):
//   1. Permission/menu/question prompts → awaiting_input
//   2. Prompt with user-typed text      → awaiting_input
//   3. Spinner / tool running / content changed → working
//   4. Bare prompt, stable              → idle
//
// Polled every 1s via activityTickCmd. Single-tick damper: if previous state
// was "working" and current tick is ambiguous, holds "working" for one more tick.

// activityState tracks per-session state for the activity detector.
type activityState struct {
	prevHash     uint64 // hash of previous capture for diff detection
	prevActivity string // previous classification for 1-tick hold
}

// Patterns for activity detection — compiled once.
var (
	// Spinner: Unicode spinner char + space + verb + ellipsis
	// Matches: ✶ Percolating…, ✽ Creating..., ◐ Thinking…, etc.
	spinnerRe = regexp.MustCompile(`^[✶✽✹◐◑◒◓⠋⠙⠹⠸⠼⠴⠦⠧●◆]\s+\S+[…\.]{1,3}`)

	// Tool running: "⎿  Running…" or "⎿  Running..."
	toolRunningRe = regexp.MustCompile(`^\s*⎿\s+Running[…\.]{1,3}`)

	// Permission/approval/menu prompts (blocking — highest priority)
	permissionRe = regexp.MustCompile(`(?i)(do you want to proceed|esc to cancel|allow|deny|yes/no|approve|pick a number|\(y/n\)|proceed\?)`)

	// Prompt line: ❯ at start (with or without trailing user text)
	promptRe = regexp.MustCompile(`^❯`)

	// Bare prompt: ❯ with no user-typed text after it
	barePromptRe = regexp.MustCompile(`^❯\s*$`)

	// Shell prompt (session fell through to shell) — all alternations start-anchored
	shellPromptRe = regexp.MustCompile(`^(>\s*|\$\s*|#\s*)$`)

	// Chrome lines to skip (status bar, separators, model info)
	chromeRe = regexp.MustCompile(`^──|^\[.*\]\s+\S+.*\|.*ctx|bypass permissions|^Claude\s+(MAX|Pro|Free)|^\[.*\]\s+nuc|⏵⏵`)
)

// classifyActivity analyses captured tmux lines and session state to determine activity.
// Exported for testing. Does not call tmux — operates on pre-captured text.
func classifyActivity(capture string, state *activityState) string {
	lines := strings.Split(strings.TrimRight(capture, "\n"), "\n")

	// --- Scan bottom-up, collect meaningful lines (up to 100) ---
	var meaningful []string
	for i := len(lines) - 1; i >= 0 && len(meaningful) < 100; i-- {
		line := strings.TrimSpace(ansiRe.ReplaceAllString(lines[i], ""))
		if line == "" {
			continue
		}
		if chromeRe.MatchString(line) {
			continue
		}
		meaningful = append(meaningful, line)
	}

	// --- Hash meaningful content (not raw capture) for diff detection ---
	// This avoids false "working" from ANSI/chrome noise changes.
	// Skip hash update on empty meaningful content to prevent false diffs after capture glitches.
	isFirstSeen := state.prevHash == 0 // true on the very first call (no prior content seen)
	contentChanged := false
	if len(meaningful) > 0 {
		hash := fnvHash(strings.Join(meaningful, "\n"))
		contentChanged = !isFirstSeen && hash != state.prevHash
		state.prevHash = hash
	}

	// --- Priority 1: Permission/menu prompts → awaiting_input ---
	for _, line := range meaningful {
		if permissionRe.MatchString(line) {
			state.prevActivity = "awaiting_input"
			return "awaiting_input"
		}
	}

	// --- Priority 2: Working — content changed ---
	// Checked BEFORE question detection: animated spinner (contentChanged=true each tick)
	// never triggers awaiting_input. Fixes SWM-52/51.
	if contentChanged {
		state.prevActivity = "working"
		return "working"
	}

	// --- Priority 2b: First-seen spinner/tool → working ---
	// On the very first call prevHash was 0, so contentChanged=false even if there is
	// an active spinner in the buffer. Scan the top of the pane to detect it. Fixes
	// the case where SwarmOps starts and a session is already mid-tool-call.
	if isFirstSeen && len(meaningful) > 0 {
		limit := 15
		if len(meaningful) < limit {
			limit = len(meaningful)
		}
		for _, line := range meaningful[:limit] {
			if spinnerRe.MatchString(line) || toolRunningRe.MatchString(line) {
				state.prevActivity = "working"
				return "working"
			}
		}
	}

	// --- Priority 3: User-typed prompt (stable) → awaiting_input ---
	// ❯ followed by text means the user is composing input and hasn't submitted yet.
	// Only fires when content is stable (contentChanged=false) so transient streaming
	// output that happens to contain ❯ doesn't trigger this. Fixes SWM-46.
	for _, line := range meaningful {
		if promptRe.MatchString(line) && !barePromptRe.MatchString(line) {
			state.prevActivity = "awaiting_input"
			return "awaiting_input"
		}
	}

	// --- Priority 4 (was 3): Question from Claude → awaiting_input ---
	// Only fires when content is STABLE (contentChanged=false) AND a bare prompt is
	// visible — meaning Claude has stopped and is waiting. Prevents false positives
	// from assistant lines that happen to end with "?". Fixes SWM-51.
	hasBarePrompt := false
	for _, line := range meaningful {
		if barePromptRe.MatchString(line) {
			hasBarePrompt = true
			break
		}
	}
	if hasBarePrompt {
		for _, line := range meaningful {
			if barePromptRe.MatchString(line) {
				continue
			}
			if promptRe.MatchString(line) || shellPromptRe.MatchString(line) {
				continue
			}
			// Spinner/tool alongside bare prompt → still working
			if spinnerRe.MatchString(line) || toolRunningRe.MatchString(line) {
				break
			}
			// First non-prompt, non-spinner assistant line: check for trailing question
			if strings.HasSuffix(strings.TrimSpace(line), "?") {
				state.prevActivity = "awaiting_input"
				return "awaiting_input"
			}
			break
		}
	}

	// --- Priority 5: 1-tick hold ---
	// Prevents flicker when Claude pauses briefly between tool calls.
	// Also handles static spinner in buffer (SWM-46): spinner present but
	// contentChanged=false means nothing actually moved — fall through to hold/idle.
	if state.prevActivity == "working" {
		state.prevActivity = "idle"
		return "working"
	}

	// --- Priority 6: Idle ---
	state.prevActivity = "idle"
	return "idle"
}


// fnvHash computes a quick FNV-1a hash for change detection.
func fnvHash(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 16777619
	}
	return h
}

var (
	spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧"}
)

// animatedIndicator returns the indicator string for a session based on its activity and the current animation frame.
func animatedIndicator(activity string, frame int) string {
	switch activity {
	case "idle":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#00ff00")).Render("●")
	case "working":
		f := spinnerFrames[frame%len(spinnerFrames)]
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#00ff00")).Render(f)
	case "awaiting_input":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#ffaa00")).Render("?")
	default:
		return statusStopped
	}
}
func runTUI(api swarmClient) error {
	p := tea.NewProgram(initialModel(api), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

// resumeClaudeCmd returns the claude command args for restarting a session.
// If a claude session ID is available, uses --resume; otherwise starts fresh.
func resumeClaudeCmd(claudeID string) []string {
	if claudeID == "" || !isValidUUID(claudeID) {
		return []string{"claude", "--dangerously-skip-permissions"}
	}
	return []string{"claude", "--resume", claudeID, "--dangerously-skip-permissions"}
}
