package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	activity    string // "stopped", "working", "awaiting"
	// Pool slot fields
	slotID   string
	model    string
	state    string // idle, busy, starting, dead
	requests int64
	costUSD  float64
	alive    bool
}

// ─── Messages ────────────────────────────────────────────────────────────────

type tickMsg time.Time     // fast animation tick (150ms)
type dataTickMsg time.Time // slow data refresh tick (2s)
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
	modeRename
	modeFeedbackType
	modeFeedbackText
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
	planeReqID     uint64 // incremented on each fetch; stale responses ignored
	icingaReqID    uint64

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

	// Scroll state
	userScrolled bool

	// Animation frame (cycles on tick)
	animFrame int

	// Rename session
	renameInput textinput.Model

	// Feedback submission
	feedbackInput    textinput.Model
	feedbackType     int // 0=bug, 1=feature
	feedbackSnapshot string // TUI state captured at Alt+F press

	// Dependency injection for testing
	spawner Spawner

	// HTTP client for backend API (client mode)
	api *apiClient
}

func initialModel(api *apiClient) tuiModel {
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
		mode:         modePassthrough,
		newNameInput: ni,
		newDirInput:  di,
		popupFilter:  fi,
		renameInput:   ri,
		feedbackInput: fi2,
		spawner:      spawner,
		api:          api,
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(tickCmd(), dataTickCmd(), loadItemsCmd(m.api))
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

// loadItemsCmd returns a tea.Cmd that builds the unified sidebar list.
// In client mode it fetches via HTTP; in server mode it calls in-process.
func loadItemsCmd(api *apiClient) tea.Cmd {
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

		for _, s := range sessions {
			activity := "stopped"
			indicator := statusStopped
			if s.Status == "running" {
				indicator = statusRunning
				activity = detectActivity(s.TmuxSession)
			}
			items = append(items, sidebarItem{
				kind:        itemSession,
				label:       s.Name,
				indicator:   indicator,
				sessionID:   s.ID,
				tmuxSession: s.TmuxSession,
				status:      s.Status,
				activity:    activity,
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

		return itemsMsg(items)
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
		// Match sidebar: top bar (headerHeight) + status line (1) + sidebar padding (2)
		contentHeight := m.h - headerHeight - 1 - 2
		if contentHeight < 5 {
			contentHeight = 5
		}
		m.vp = viewport.New(contentWidth, contentHeight)
		// Resize all tmux sessions to match the content pane
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

	case dataTickMsg:
		// Slow tick (2s): HTTP data refresh (sessions, pool status, activity detection)
		return m, tea.Batch(dataTickCmd(), loadItemsCmd(m.api))

	case itemsMsg:
		m.items = msg
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
			contentHeight := m.h - headerHeight - 1 - 2
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

	case popupErrMsg:
		if (msg.source == "plane" && m.mode == modePlaneIssues && msg.reqID == m.planeReqID) ||
			(msg.source == "icinga" && m.mode == modeIcingaAlerts && msg.reqID == m.icingaReqID) {
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
				m.flash = fmt.Sprintf("Deleted %s", item.label)
				return m, loadItemsCmd(m.api)
			}
			return m, nil
		case "alt+s":
			if m.cursor < len(m.items) && m.items[m.cursor].kind == itemSession {
				item := m.items[m.cursor]
				if item.tmuxSession != "" {
					// Send Ctrl+C to interrupt the running process
					exec.Command("tmux", "send-keys", "-t", item.tmuxSession, "C-c").Run()
					m.flash = fmt.Sprintf("Sent interrupt to %s (Alt+S again to kill & restart)", item.label)
				}
				return m, nil
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
					exec.Command("tmux", "send-keys", "-t", item.tmuxSession, "claude --dangerously-skip-permissions", "Enter").Run()
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
		case "alt+f":
			// Capture TUI state before switching to feedback mode
			m.feedbackSnapshot = m.View()
			m.mode = modeFeedbackType
			m.feedbackType = 0
			m.flash = "Feedback: ←/→ Bug or Feature, Enter to continue, Esc to cancel"
			return m, nil
		case "alt+p":
			m.mode = modePlaneIssues
			m.planeIssues = nil
			m.popupErr = ""
			m.popupCursor = 0
			m.flash = ""
			m.planeReqID++
			return m, fetchPlaneIssues(m.planeReqID, m.api)
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
			m.flash = "Fetching contexts..."
			return m, fetchContexts()
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
			m.mode = modePassthrough
			m.flash = ""
		}

	case modeFeedbackText:
		switch key {
		case "enter":
			summary := m.feedbackInput.Value()
			if summary != "" {
				kinds := []string{"bug", "feature"}
				go submitFeedback(kinds[m.feedbackType], summary, m.api, m.feedbackSnapshot)
				m.flash = fmt.Sprintf("Submitted %s: %s", kinds[m.feedbackType], summary)
			}
			m.mode = modePassthrough
			return m, nil
		case "esc":
			m.mode = modePassthrough
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
				m.openActionPicker(issue.Title, planeIssuePrompt(issue), modePlaneIssues)
			}
		} else if r.action == "refresh" {
			m.planeIssues = nil
			m.planeReqID++
			return m, fetchPlaneIssues(m.planeReqID, m.api)
		}
		if r.handled {
			return m, r.cmd
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
				return m, loadItemsCmd(m.api)
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
	s, err := m.spawner.Spawn(context.Background(), name, dir, contextID, contextName)
	if err != nil {
		m.flash = "Spawn error: " + err.Error()
	} else {
		m.flash = fmt.Sprintf("Spawned %s", s.Name)
	}
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
			statusLine = dimStyle.Render("Alt+A/Z nav │ Alt+N new │ Alt+S stop │ Alt+R rename │ Alt+D delete │ Alt+P plane │ Alt+I icinga │ Alt+F feedback │ Alt+Q quit")
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

		ind := item.indicator
		if item.kind == itemSession {
			ind = animatedIndicator(item.activity, m.animFrame)
		}

		if i == m.cursor {
			// Selected: cursor arrow + underlined label
			if item.kind == itemPoolSlot {
				line := fmt.Sprintf(" %s %s %s", ind, selectedLabelStyle.Render(label), dimStyle.Render(item.state))
				lines = append(lines, selectedStyle.Render("▸") + line)
				continue
			}
			line := fmt.Sprintf(" %s %s", ind, selectedLabelStyle.Render(label))
			lines = append(lines, selectedStyle.Render("▸") + line)
			continue
		}

		var line string
		if item.kind == itemPoolSlot {
			line = fmt.Sprintf("  %s %s %s", ind, label, dimStyle.Render(item.state))
		} else {
			line = fmt.Sprintf("  %s %s", ind, label)
		}
		lines = append(lines, line)
	}

	if len(m.items) == 0 {
		lines = append(lines, dimStyle.Render(" (no sessions)"))
	}

	for len(lines) < m.h-2 {
		lines = append(lines, "")
	}

	// Height accounts for: top bar (headerHeight), status line (1), sidebar vertical padding (2)
	sideHeight := m.h - headerHeight - 1 - 2
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
	return lipgloss.NewStyle().Width(contentWidth).Render(m.vp.View())
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

// runTUI starts the Bubbletea TUI. Database, config, and pool must be initialised by main().

// stripANSI removes ANSI escape sequences from a string.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// detectActivity scans the last 15 lines of a tmux session to classify activity.
// Returns "idle" (empty prompt, nothing happening), "working" (anything active),
// or "stopped" (session not running).
func detectActivity(tmuxSession string) string {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-S", "-15", "-t", tmuxSession).Output()
	if err != nil {
		return "stopped"
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")

	// Collect cleaned non-chrome lines from the bottom
	var meaningful []string
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-15; i-- {
		line := strings.TrimSpace(ansiRe.ReplaceAllString(lines[i], ""))
		if line == "" {
			continue
		}
		// Skip Claude Code chrome (status bar, separator lines, effort indicator)
		if strings.HasPrefix(line, "──") ||
			strings.HasPrefix(line, "[") ||
			strings.HasPrefix(line, "Claude") ||
			strings.Contains(line, "bypass permissions") ||
			strings.HasPrefix(line, "◐") || strings.HasPrefix(line, "◑") ||
			strings.HasPrefix(line, "◒") || strings.HasPrefix(line, "◓") {
			continue
		}
		meaningful = append(meaningful, line)
		if len(meaningful) >= 5 {
			break
		}
	}

	if len(meaningful) == 0 {
		return "working"
	}

	// "idle" = the ONLY meaningful content is a bare ❯ prompt or shell prompt
	// with no tool calls, no output, no permission dialogs above it
	first := meaningful[0] // closest to bottom

	// Bare prompt with nothing or just whitespace/cursor after ❯
	isBareprompt := (strings.HasPrefix(first, "❯") && len(strings.TrimSpace(strings.TrimPrefix(first, "❯"))) <= 1) ||
		strings.HasSuffix(first, "$ ") ||
		strings.HasSuffix(first, "# ")

	if isBareprompt {
		// Check if anything above the prompt suggests active work
		for _, line := range meaningful[1:] {
			if strings.Contains(line, "Running") ||
				strings.HasPrefix(line, "●") ||
				strings.Contains(line, "Esc to cancel") ||
				strings.Contains(line, "Do you want to proceed") {
				return "working" // permission dialog or tool running above prompt
			}
		}
		return "idle"
	}

	// Everything else is "working" — tool calls, output, permission dialogs, etc.
	return "working"
}

var (
	spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧"}
)

// animatedIndicator returns the indicator string for a session based on its activity and the current animation frame.
func animatedIndicator(activity string, frame int) string {
	switch activity {
	case "idle":
		// Truly idle — static green dot
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#00ff00")).Render("●")
	case "working":
		// Active (processing, tool calls, permission dialogs) — spinner
		f := spinnerFrames[frame%len(spinnerFrames)]
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#00ff00")).Render(f)
	default:
		return statusStopped
	}
}
func runTUI(api *apiClient) error {
	p := tea.NewProgram(initialModel(api), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}
