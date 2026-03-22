package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── Main model ───────────────────────────────────────────────────────────────

type tuiModel struct {
	// Data
	sessions []tuiSession
	states   map[string]tuiState

	// Sidebar
	items  []tuiSidebarItem
	cursor int

	// Right pane
	vp       viewport.Model
	vpReady  bool
	vpLines          map[string][]string // sid → rendered log lines
	vpPinned         bool                // true = auto-scroll to bottom; false = user scrolled up
	vpLastContentKey string              // last content key; vpPinned resets to true when this changes

	// Input
	chatInput textarea.Model

	// Modal
	modal *tuiModal

	// Focus
	focus tuiFocus

	// Animation
	frame int

	// Terminal size
	w, h int

	// Status bar (persistent — stays until replaced)
	flash    string
	flashErr bool

	// Fleet status bar (always-visible bottom line)
	statusBar tuiStatusBar

	// Terminal snapshot (agentID → last captured content)
	termContent  map[string]string
	termFetching bool

	// Escalation view
	escView      bool
	escCursor    int
	escInputting bool
	escActive    *tuiEscalation
	escInput     textinput.Model

	// Goals view
	goalView   bool
	goalCursor int

	// Work queue view
	workQueueView      bool
	workQueueItems     []WorkQueueItem
	workQueueSID       string
	workQueueCursor    int
	workQueuePromoting bool // true while create-goal POST is in-flight

	// Notes view (N key)
	notesView    bool
	notesAgentID string
	notesSID     string
	notesCursor  int
	notesItems   []agentNote

	// Generic two-press confirmation (replaces pendingDespawn + pendingDeleteSession)
	pendingConfirm *pendingConfirmAction

	// Collapsed sessions (sid → true means agents/tasks hidden)
	collapsedSessions map[string]bool

	// Help overlay (hold ?)
	helpVisible bool
	helpVersion int

	// Icinga monitor view (I key)
	icingaView     bool
	icingaServices []IcingaService // sorted by state
	icingaTopCur   int             // cursor in top pane (services)
	icingaBotCur   int             // cursor in bottom pane (recent alerts)
	icingaFocus    int             // 0=top, 1=bottom

	// Global triage view (T key)
	triageView   bool
	triageCursor int

	// Event log view (L key)
	evtLogView      bool
	evtCursor       int
	evtAgentFilter  string
	evtDetailView   *tuiEvent
	vpRawEvents     map[string][]tuiEvent

	// Version check
	updateAvailable bool
	updateRemote    string

	// Feedback (alt-F)
	feedbackCapture    *tuiFeedbackCapture
	feedbackSubmitting bool

	// Ops Console (o key)
	opsView   bool
	opsCursor int

	// Git status cache (agentID → last fetched status)
	gitStatus    map[string]tuiGitStatus
	gitFetching  bool

	// Clients
	client TUIClient
	ws     *tuiWSManager
}

func newTUIModel() tuiModel {
	c := newSwarmClient()
	ws := newTUIWSManager()
	ta := textarea.New()
	ta.Placeholder = "Message to orchestrator… /goal <desc> to set a goal  (Enter sends, Esc unfocuses)"
	ta.ShowLineNumbers = false
	ta.CharLimit = 2000
	ei := textinput.New()
	ei.Placeholder = "Type response to escalation…"
	ei.CharLimit = 500
	return tuiModel{
		states:            make(map[string]tuiState),
		vpLines:           make(map[string][]string),
		vpRawEvents:       make(map[string][]tuiEvent),
		termContent:       make(map[string]string),
		gitStatus:         make(map[string]tuiGitStatus),
		collapsedSessions: make(map[string]bool),
		chatInput:         ta,
		escInput:          ei,
		client:            c,
		ws:                ws,
		vpPinned:          true, // start pinned to bottom; scrolling up unpins
	}
}

// ─── Event feed ───────────────────────────────────────────────────────────────

func (m *tuiModel) appendEvents(sid string, state tuiState) {
	// Build agent name lookup
	agentNames := make(map[string]string, len(state.Agents))
	for _, a := range state.Agents {
		agentNames[a.ID] = a.Name
	}

	// Apply agent filter
	var evts []tuiEvent
	for _, ev := range state.Events {
		if m.evtAgentFilter != "" {
			name := agentNames[ev.AgentID]
			if !strings.Contains(strings.ToLower(name), strings.ToLower(m.evtAgentFilter)) {
				continue
			}
		}
		evts = append(evts, ev)
	}

	// Cap at 500 raw events
	if len(evts) > 500 {
		evts = evts[len(evts)-500:]
	}
	m.vpRawEvents[sid] = evts

	// Render lines
	lines := make([]string, 0, len(evts))
	for _, ev := range evts {
		ts := time.Unix(ev.Ts, 0).Format("15:04:05")
		agentName := truncStr(agentNames[ev.AgentID], 12)
		payload := truncStr(ev.Payload, 80)
		typeStr := lipgloss.NewStyle().Foreground(tuiEventColor(ev.Type)).Render(truncStr(ev.Type, 26))
		line := fmt.Sprintf("%s  %s  %-12s  %s", ts, typeStr, agentName, payload)
		lines = append(lines, line)
	}
	m.vpLines[sid] = lines
}

func (m *tuiModel) updateVP() {
	if !m.vpReady {
		return
	}
	// Agent with active tmux → show live terminal capture
	it := m.selItem()
	if it != nil && it.kind == tuiItemAgent {
		agent := m.lookupAgent(it.sid, it.eid)
		if agent != nil && agent.TmuxSession != nil {
			key := "term:" + it.sid + ":" + it.eid
			if key != m.vpLastContentKey {
				m.vpLastContentKey = key
				m.vpPinned = true
			}
			if content, ok := m.termContent[it.eid]; ok && content != "" {
				m.vp.SetContent(content)
				if m.vpPinned {
					m.vp.GotoBottom()
				}
				return
			}
			m.vp.SetContent(dimStyle.Render("  Fetching terminal…"))
			return
		}
	}
	sid := m.selSessionID()
	key := "events:" + sid
	if key != m.vpLastContentKey {
		m.vpLastContentKey = key
		m.vpPinned = true
	}
	lines := m.vpLines[sid]
	if len(lines) == 0 {
		m.vp.SetContent(dimStyle.Render("  No events yet"))
		return
	}
	m.vp.SetContent(strings.Join(lines, "\n"))
	if m.vpPinned {
		m.vp.GotoBottom()
	}
}

func tuiEventColor(evType string) lipgloss.Color {
	switch {
	case strings.Contains(evType, "stuck"):
		return colorRed
	case strings.Contains(evType, "spawn"), strings.Contains(evType, "start"):
		return colorGreen
	case strings.Contains(evType, "message"), strings.Contains(evType, "inject"):
		return colorTeal
	case strings.Contains(evType, "task"):
		return colorBlue
	default:
		return colorDim
	}
}

// ─── Flash ────────────────────────────────────────────────────────────────────

func (m *tuiModel) setFlash(text string, isErr bool) {
	m.flash = text
	m.flashErr = isErr
}

// ─── Init ─────────────────────────────────────────────────────────────────────

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(
		tea.EnterAltScreen,
		tuiAnimTick(),
		m.client.fetchAll(),
		waitForWS(m.ws.ch),
		checkVersionCmd(m.client),
	)
}

// checkVersionCmd fires 15 s after startup — enough for the server's own
// background goroutine to have run its first git check.
func checkVersionCmd(c TUIClient) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(15 * time.Second)
		data, err := c.getSync("/api/swarm/version")
		if err != nil {
			return nil
		}
		var resp struct {
			UpdateAvail bool   `json:"update_available"`
			Remote      string `json:"remote"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil
		}
		return tuiVersionMsg{updateAvail: resp.UpdateAvail, remote: resp.Remote}
	}
}

// ─── Update ──────────────────────────────────────────────────────────────────

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		bodyH := m.h - 3 - 1 - tuiInputH - 2 - 1 - 1 // hud(content+border+join-newline) + help + input rows + input borders + status bar + fleet bar
		if bodyH < 5 {
			bodyH = 5
		}
		vpH := bodyH - tuiDetailH - 1
		if vpH < 3 {
			vpH = 3
		}
		vpW := m.w - tuiSidebarW - 1
		if vpW < 10 {
			vpW = 10
		}
		if !m.vpReady {
			m.vp = viewport.New(vpW, vpH)
			m.vpReady = true
		} else {
			m.vp.Width = vpW
			m.vp.Height = vpH
		}
		m.chatInput.SetWidth(m.w - 4)
		m.chatInput.SetHeight(tuiInputH)
		m.updateVP()

	case tuiAnimTickMsg:
		m.frame = (m.frame + 1) % 8
		cmds = append(cmds, tuiAnimTick())
		if m.frame%3 == 0 {
			it := m.selItem()
			if it != nil && it.kind == tuiItemAgent {
				agent := m.lookupAgent(it.sid, it.eid)
				if agent != nil {
					if agent.TmuxSession != nil && !m.termFetching {
						m.termFetching = true
						cmds = append(cmds, m.client.fetchTerminal(it.sid, it.eid))
					}
					// Fetch git status every ~12 animation ticks (~1.8s) when on an agent
					if !m.gitFetching && m.frame%8 == 0 {
						m.gitFetching = true
						cmds = append(cmds, m.client.fetchGitStatus(it.sid, it.eid))
					}
				}
			}
		}

	case tuiTermMsg:
		m.termFetching = false
		if msg.content != "" {
			m.termContent[msg.agentID] = msg.content
			m.updateVP()
		}

	case tuiGitStatusMsg:
		m.gitFetching = false
		if msg.status.Branch != "" || msg.status.Subject != "" {
			m.gitStatus[msg.agentID] = msg.status
		}

	case tuiWSUpdateMsg:
		m.states[msg.sid] = msg.state
		m.appendEvents(msg.sid, msg.state)
		m.rebuildItems()
		m.updateVP()
		m.statusBar = buildStatusBar(&m)
		cmds = append(cmds, waitForWS(m.ws.ch))

	case tuiFleetModeMsg:
		m.statusBar.fleetMode = msg.mode

	case tuiDataMsg:
		m.sessions = msg.sessions
		m.states = msg.states
		for _, sess := range msg.sessions {
			m.ws.connect(sess.ID)
			if st, ok := msg.states[sess.ID]; ok {
				m.appendEvents(sess.ID, st)
			}
		}
		m.rebuildItems()
		m.updateVP()

	case tuiErrMsg:
		m.setFlash(msg.text, true)
		// If promotion failed, stay in work queue so user can retry
		if msg.op == "create-goal" && m.workQueueView {
			m.workQueuePromoting = false
		}

	case tuiDoneMsg:
		// Close work queue on successful goal promotion
		if msg.op == "create-goal" && m.workQueueView {
			m.workQueueView = false
			m.workQueueItems = nil
			m.workQueueCursor = 0
			m.workQueuePromoting = false
		}
		// Re-fetch notes after add-note so the notes view is refreshed.
		if msg.op == "add-note" && m.notesView {
			cmds = append(cmds, m.client.fetchNotes(m.notesSID, m.notesAgentID))
		}
		labels := map[string]string{
			"spawn": "Agent spawned", "despawn": "Agent stopped",
			"msg": "Message sent", "create-session": "Session created",
			"create-agent": "Agent created", "create-task": "Task created",
			"resume": "Session resumed", "create-goal": "Goal created",
			"esc-respond": "Escalation resolved",
			"inject-agent": "Message injected", "edit-task-stage": "Stage updated",
			"delete-agent": "Agent deleted", "delete-task": "Task deleted",
			"cancel-goal": "Goal cancelled", "reactivate-goal": "Goal reactivated",
			"add-note": "Note added",
		}
		label := labels[msg.op]
		if label == "" {
			label = msg.op + " OK"
		}
		m.setFlash(label, false)
		cmds = append(cmds, m.client.fetchAll())

	case tuiHelpHideMsg:
		if msg.version == m.helpVersion {
			m.helpVisible = false
		}

	case tuiIcingaMsg:
		m.icingaServices = msg.services
		m.icingaTopCur = 0
		// Scroll bottom pane to show most recent alert at top.
		m.icingaBotCur = 0

	case tuiNotesMsg:
		m.notesItems = msg.items
		m.notesCursor = 0

	case tuiWorkQueueMsg:
		m.workQueueItems = msg.items
		m.workQueueCursor = 0
		m.workQueuePromoting = false
		m.updateVP()

	case tuiAttachMsg:
		m.setFlash("Returned from tmux session", false)
		cmds = append(cmds, m.client.fetchAll())

	case tuiRolePromptEditMsg:
		// Phase 2: open $EDITOR (suspends TUI), then PUT result on close.
		role := msg.role
		tmpPath := msg.tmpPath
		client := m.client
		cmd := exec.Command(msg.editor, tmpPath)
		cmds = append(cmds, tea.ExecProcess(cmd, func(err error) tea.Msg {
			defer os.Remove(tmpPath) //nolint:errcheck
			if err != nil {
				return tuiErrMsg{op: "role-prompts", text: err.Error()}
			}
			newPrompt, err := os.ReadFile(tmpPath)
			if err != nil || len(newPrompt) == 0 {
				return tuiErrMsg{op: "role-prompts", text: "empty prompt — not saved"}
			}
			body, _ := json.Marshal(map[string]string{"prompt": string(newPrompt)})
			if putErr := client.putSync("/api/swarm/role-prompts/"+role, body); putErr != nil {
				return tuiErrMsg{op: "role-prompts", text: putErr.Error()}
			}
			return tuiRolePromptSavedMsg{role: role}
		}))

	case tuiRolePromptSavedMsg:
		m.setFlash("Role prompt updated: "+msg.role, false)

	case tuiVersionMsg:
		m.updateAvailable = msg.updateAvail
		m.updateRemote = msg.remote
		if msg.updateAvail {
			m.setFlash(fmt.Sprintf("⬆ SwarmOps update available (remote: %s) — git pull && make backend", msg.remote), false)
		}

	case tuiFeedbackResultMsg:
		m.feedbackSubmitting = false
		m.feedbackCapture = nil
		if msg.err != nil {
			// Keep modal open so user can retry
			if m.modal != nil && m.modal.kind == tuiModalFeedback {
				m.modal.err = msg.err.Error()
			} else {
				m.setFlash("Feedback failed: "+msg.err.Error(), true)
			}
		} else {
			m.modal = nil
			m.focus = tuiFocusSidebar
			m.setFlash(fmt.Sprintf("Feedback submitted — SWM-%d", msg.seqID), false)
		}

	case opsFleetActionMsg:
		if msg.err != nil {
			m.setFlash("Fleet action failed: "+msg.err.Error(), true)
		} else {
			m.statusBar.fleetMode = msg.mode
		}

	case tea.KeyMsg:
		// ctrl+x global halt — works from anywhere, no menu navigation required
		if msg.String() == "ctrl+x" {
			m.opsView = false
			cmds = append(cmds, postFleetHalt("user"))
			m.setFlash("⚠ HALT — fleet entering CONTAIN mode", true)
			break
		}
		// ? shows hold-to-view help from anywhere; each press resets the hide timer.
		if msg.String() == "?" {
			m.helpVisible = true
			m.helpVersion++
			cmds = append(cmds, hideHelpAfter(m.helpVersion))
			break
		}
		// alt+F opens the feedback modal from any view (except when feedback modal already open)
		if msg.String() == "alt+f" {
			if m.modal == nil || m.modal.kind != tuiModalFeedback {
				fc := captureFeedbackState(&m)
				m.feedbackCapture = &fc
				m.feedbackSubmitting = false
				m.modal = newTUIModal(tuiModalFeedback, m.selSessionID())
				m.focus = tuiFocusModal
				break
			}
		}
		// Any other key hides the help overlay immediately.
		if m.helpVisible {
			m.helpVisible = false
		}
		if m.modal != nil || m.focus == tuiFocusModal {
			return m.updateModal(msg)
		} else if m.opsView {
			m, cmds = m.updateOpsView(msg)
		} else if m.notesView {
			m, cmds = m.updateNotesView(msg)
		} else if m.triageView {
			m, cmds = m.updateTriageView(msg)
		} else if m.evtDetailView != nil {
			if msg.String() == "q" || msg.String() == "esc" {
				m.evtDetailView = nil
			}
		} else if m.icingaView {
			m, cmds = m.updateIcingaView(msg)
		} else if m.evtLogView {
			m, cmds = m.updateEventLog(msg)
		} else if m.workQueueView {
			m, cmds = m.updateWorkQueueView(msg)
		} else if m.goalView {
			m, cmds = m.updateGoalView(msg)
		} else if m.escView {
			m, cmds = m.updateEscalation(msg)
		} else {
			switch m.focus {
			case tuiFocusInput:
				m, cmds = m.updateInput(msg)
			case tuiFocusSidebar:
				m, cmds = m.updateSidebar(msg)
			}
		}
	}

	// PageUp / PageDown and mouse wheel always scroll the Claude Code viewport,
	// regardless of which pane has keyboard focus.
	if m.vpReady {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "pgup", "pgdown":
				prevOffset := m.vp.YOffset
				var cmd tea.Cmd
				m.vp, cmd = m.vp.Update(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
				if m.vp.YOffset < prevOffset {
					m.vpPinned = false // scrolled up — unpin
				} else if m.vp.AtBottom() {
					m.vpPinned = true // scrolled back to bottom — re-pin
				}
			}
		case tea.MouseMsg:
			if msg.Action == tea.MouseActionPress &&
				(msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown) {
				prevOffset := m.vp.YOffset
				var cmd tea.Cmd
				m.vp, cmd = m.vp.Update(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
				if m.vp.YOffset < prevOffset {
					m.vpPinned = false // scrolled up — unpin
				} else if m.vp.AtBottom() {
					m.vpPinned = true // scrolled back to bottom — re-pin
				}
			}
		}
	}

	return m, tea.Batch(cmds...)
}

func (m tuiModel) updateInput(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	var cmds []tea.Cmd
	switch msg.String() {
	case "esc":
		m.chatInput.Blur()
		m.focus = tuiFocusSidebar
	case "ctrl+c":
		m.ws.closeAll()
		cmds = append(cmds, tea.Quit)
	case "enter":
		text := strings.TrimSpace(m.chatInput.Value())
		if text != "" {
			sid := m.selSessionID()
			if sid != "" {
				m.chatInput.Reset()
				m.focus = tuiFocusSidebar
				if strings.HasPrefix(text, "/goal ") {
					desc := strings.TrimSpace(text[6:])
					if desc != "" {
						path := "/api/swarm/sessions/" + sid + "/goals"
						cmds = append(cmds, m.client.post("create-goal", path, map[string]string{"description": desc}))
					}
				} else {
					path := "/api/swarm/sessions/" + sid + "/orchestrator/message"
					cmds = append(cmds, m.client.post("msg", path, map[string]string{"text": text}))
					// Optimistic local echo
					ts := time.Now().Format("15:04:05")
					line := lipgloss.NewStyle().Foreground(colorTeal).Render(
						fmt.Sprintf("%s  %-22s  %s", ts, "→ you", truncStr(text, 44)))
					m.vpLines[sid] = append(m.vpLines[sid], line)
					m.updateVP()
				}
			}
		}
	default:
		var cmd tea.Cmd
		m.chatInput, cmd = m.chatInput.Update(msg)
		cmds = append(cmds, cmd)
	}
	return m, cmds
}

// ─── Entry Point ─────────────────────────────────────────────────────────────

func RunSwarmTUI() {
	p := tea.NewProgram(newTUIModel(), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "swarm TUI error: %v\n", err)
		os.Exit(1)
	}
}
