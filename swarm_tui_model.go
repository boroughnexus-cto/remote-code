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

	// Terminal zoom view — full-screen agent terminal with HUD preserved
	termZoomed  bool
	zoomAgentID string // locked on entry; does not follow sidebar selection

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
	workQueueFilter    string // priority filter: "" | "urgent" | "high" | "medium" | "low"
	workQueuePromoting bool   // true while create-goal POST is in-flight

	// Notes view (N key)
	notesView    bool
	notesAgentID string
	notesSID     string
	notesCursor  int
	notesItems   []agentNote

	// Generic two-press confirmation (replaces pendingDespawn + pendingDeleteSession)
	pendingConfirm *pendingConfirmAction

	// ctrl+x requires a second press within one tick to fire the fleet HALT
	pendingHalt bool

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

	// Control Tower (pinned fleet dashboard)
	ctView bool

	// Context picker overlay (C on a session)
	ctxPicker *tuiCtxPickerModel

	// Role picker overlay ('n' new agent; auto after session creation)
	rolePicker *tuiRolePickerModel
	// postSessionSID: when set, open role picker after ctx picker closes
	postSessionSID string

	// Command Palette (: key)
	cmdPalette *cmdPaletteModel

	// Settings overlay (Esc from sidebar)
	settings *tuiSettingsModel

	// Git status cache (agentID → last fetched status)
	gitStatus    map[string]tuiGitStatus
	gitFetching  bool

	// Operator's own Claude Code session stats (from ~/.claude/.swarmops-statusline.json)
	ccUsage tuiCCUsage

	// API-sourced usage stats (Claude quota + Copilot) from /api/swarm/usage
	apiUsage tuiAPIUsageStats

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
		tuiSlowTick(),
		m.client.fetchAll(),
		m.client.get("icinga", "/api/icinga/services"),
		fetchCCUsage(),
		fetchAPIUsage(m.client),
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
		// Must match the bodyH formula in View() exactly so the viewport is sized
		// to fit the available space without pushing the HUD off-screen.
		bodyH := m.h - 3 - m.helpLineCount() - tuiInputH - 2 - 1 - 1 // hud(content+border+join-newline) + help + input rows + input borders + status bar + fleet bar
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

	case tuiCCUsageMsg:
		if msg.err == nil {
			m.ccUsage = msg.usage
		}

	case tuiAPIUsageMsg:
		if msg.err == nil {
			m.apiUsage = msg.stats
		}

	case tuiSlowTickMsg:
		cmds = append(cmds, tuiSlowTick())
		// Read operator's own CC session stats from statusline cache.
		cmds = append(cmds, fetchCCUsage())
		// Refresh API usage stats every slow tick (~30s), server caches at 5min.
		cmds = append(cmds, fetchAPIUsage(m.client))
		// Always refresh Icinga in the background so data is ready when the view opens.
		cmds = append(cmds, m.client.get("icinga", "/api/icinga/services"))
		// Background Plane fetch: use the work queue session if open, otherwise the selected session.
		planeSID := m.workQueueSID
		if planeSID == "" {
			planeSID = m.selSessionID()
		}
		if planeSID != "" {
			path := "/api/swarm/sessions/" + planeSID + "/plane/issues?state_group=backlog,unstarted"
			cmds = append(cmds, m.client.get("workqueue", path))
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
		// Update integration health flags on fetch failure.
		if msg.op == "icinga" {
			m.statusBar.icingaOK = false
			m.statusBar.icingaReady = true
		}
		if msg.op == "workqueue" {
			m.statusBar.planeOK = false
			m.statusBar.planeReady = true
		}
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
		if (msg.op == "ctx-content" || msg.op == "ctx-dynamic" || msg.op == "delete-context") && m.settings != nil {
			// Reload the Session Contexts settings tab after content/dynamic edit or delete.
			for _, sec := range m.settings.sections {
				if sc, ok := sec.(*sessionContextsSection); ok {
					sc.loading = true
					cmds = append(cmds, sc.Init())
					break
				}
			}
		}
		// After session creation, auto-open context picker so user can optionally assign one.
		if msg.op == "create-session" {
			// Find the newly created session (first in list after reload)
			cmds = append(cmds, func() tea.Msg {
				return tuiAutoOpenCtxPickerMsg{}
			})
		}
		labels := map[string]string{
			"spawn": "Agent spawned", "despawn": "Agent stopped",
			"msg": "Message sent", "create-session": "Session created",
			"create-agent": "Agent created", "create-task": "Task created",
			"resume": "Session resumed", "create-goal": "Goal created",
			"esc-respond": "Escalation resolved",
			"inject-agent": "Message injected", "edit-task-stage": "Stage updated",
			"delete-agent": "Agent deleted", "delete-task": "Task deleted",
			"delete-session": "Session deleted",
			"cancel-goal": "Goal cancelled", "reactivate-goal": "Goal reactivated",
			"add-note": "Note added", "set-context": "Context assigned",
			"ctx-content": "Context content updated", "ctx-dynamic": "Dynamic context updated",
			"delete-context": "Context deleted",
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
		m.statusBar.icingaOK = true
		m.statusBar.icingaReady = true

	case tuiNotesMsg:
		m.notesItems = msg.items
		m.notesCursor = 0

	case tuiCtxPickerMsg:
		if m.ctxPicker != nil {
			m.ctxPicker.items = msg.items
			m.ctxPicker.ready = true
			m.ctxPicker.cursor = 0
		}

	case tuiRolePickerMsg:
		if m.rolePicker != nil {
			m.rolePicker.items = msg.items
			m.rolePicker.ready = true
			m.rolePicker.cursor = 0
		}

	case tuiWorkQueueMsg:
		m.workQueueItems = msg.items
		m.workQueueCursor = 0
		m.workQueuePromoting = false
		m.statusBar.planeOK = true
		m.statusBar.planeReady = true
		m.updateVP()

	case tuiAutoOpenCtxPickerMsg:
		// Open the context picker on the most recently created session (first in list).
		// Fires after fetchAll completes on session create, so m.sessions is populated.
		if len(m.sessions) > 0 {
			newest := m.sessions[0]
			if newest.ContextID == nil {
				// Context not yet assigned — open ctx picker, then role picker after
				m.ctxPicker = newCtxPicker(newest.ID)
				cmds = append(cmds, fetchContextsForPicker(m.client))
				m.postSessionSID = newest.ID
			} else {
				// Already has a context — skip ctx picker, go straight to role picker
				m.rolePicker = newRolePicker(newest.ID, true)
				cmds = append(cmds, fetchRolesForPicker(m.client))
			}
		}

	case tuiAttachMsg:
		m.termZoomed = false
		if msg.err != nil {
			m.setFlash("tmux switch failed: "+msg.err.Error(), true)
		} else {
			m.setFlash("Returned from tmux session", false)
		}
		cmds = append(cmds, m.client.fetchAll())

	case personaSavedMsg:
		if msg.err != nil {
			m.setFlash("Error saving persona: "+msg.err.Error(), true)
		} else {
			m.setFlash("Persona saved: "+msg.role, false)
			// Reload the personas list in settings.
			if m.settings != nil {
				for _, sec := range m.settings.sections {
					if ps, ok := sec.(*personasSection); ok {
						cmds = append(cmds, ps.Init())
					}
				}
			}
		}

	case personaDeletedMsg:
		if msg.err != nil {
			m.setFlash("Error deleting persona: "+msg.err.Error(), true)
		} else {
			m.setFlash("Persona deleted: "+msg.role, false)
			if m.settings != nil {
				for _, sec := range m.settings.sections {
					if ps, ok := sec.(*personasSection); ok {
						cmds = append(cmds, ps.Init())
					}
				}
			}
		}

	case tuiVersionMsg:
		m.updateAvailable = msg.updateAvail
		m.updateRemote = msg.remote
		if msg.updateAvail {
			m.setFlash(fmt.Sprintf("⬆ SwarmOps update available (remote: %s) — git pull && make backend", msg.remote), false)
		}

	case tuiDispatchSuggestMsg:
		if msg.err != nil {
			// Fallback was used server-side — still open modal with whatever came back.
			if msg.sessionID != "" {
				m.modal = newIcingaAgentModal(msg.sessionID, msg.svc)
				overrideIcingaModalFields(m.modal, msg.role, msg.mission)
				m.focus = tuiFocusModal
				m.setFlash("LLM unavailable — used keyword fallback", false)
			} else {
				m.setFlash("Dispatch suggest failed: "+msg.err.Error(), true)
			}
		} else {
			m.modal = newIcingaAgentModal(msg.sessionID, msg.svc)
			overrideIcingaModalFields(m.modal, msg.role, msg.mission)
			m.focus = tuiFocusModal
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

	case swarmConfigLoadedMsg:
		applySwarmConfigLoaded(&m, msg)

	case swarmConfigSavedMsg:
		applySwarmConfigSaved(&m, msg)
		if msg.err == nil {
			m.setFlash("Config saved: "+msg.key, false)
			// Trigger reload in section
			if m.settings != nil {
				for _, sec := range m.settings.sections {
					if sc, ok := sec.(*swarmConfigSection); ok && sc.loading {
						cmds = append(cmds, sc.Init())
					}
				}
			}
		} else {
			m.setFlash("Config save failed: "+msg.err.Error(), true)
		}

	case personasLoadedMsg:
		applyPersonasLoaded(&m, msg)

	case sessionContextsLoadedMsg:
		applySessionContextsLoaded(&m, msg)

	case sessionContextSavedMsg:
		applySessionContextSaved(&m, msg)
		if msg.err == nil {
			m.setFlash("Session context saved", false)
			// Trigger reload in section
			if m.settings != nil {
				for _, sec := range m.settings.sections {
					if sc, ok := sec.(*sessionContextsSection); ok && sc.loading {
						cmds = append(cmds, sc.Init())
					}
				}
			}
		} else {
			m.setFlash("Save failed: "+msg.err.Error(), true)
		}

	case opsFleetActionMsg:
		if msg.err != nil {
			m.setFlash("Fleet action failed: "+msg.err.Error(), true)
		} else {
			m.statusBar.fleetMode = msg.mode
		}

	case tea.KeyMsg:
		// Any key other than ctrl+x cancels the pending-halt confirmation.
		if msg.String() != "ctrl+x" {
			m.pendingHalt = false
		}
		// : opens command palette from anywhere (except when typing in a text field)
		if msg.String() == ":" && m.focus != tuiFocusInput && m.cmdPalette == nil &&
			m.modal == nil && !m.opsView {
			m.cmdPalette = newCmdPaletteModel()
			break
		}
		// esc closes command palette
		if msg.String() == "esc" && m.cmdPalette != nil {
			m.cmdPalette = nil
			break
		}
		// ctrl+x global halt — requires double-press; ignored when typing in input
		if msg.String() == "ctrl+x" && m.focus != tuiFocusInput {
			if m.pendingHalt {
				m.pendingHalt = false
				m.opsView = false
				cmds = append(cmds, postFleetHalt("user"))
				m.setFlash("⚠ HALT — fleet entering CONTAIN mode", true)
			} else {
				m.pendingHalt = true
				m.setFlash("⚠ Press ctrl+x again to HALT the fleet", true)
			}
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
		if m.ctxPicker != nil {
			m, cmds = m.updateCtxPicker(msg)
			// After ctx picker closes, offer role picker if in post-session flow
			if m.ctxPicker == nil && m.postSessionSID != "" {
				sid := m.postSessionSID
				m.postSessionSID = ""
				m.rolePicker = newRolePicker(sid, true)
				cmds = append(cmds, fetchRolesForPicker(m.client))
			}
		} else if m.rolePicker != nil {
			m, cmds = m.updateRolePicker(msg)
		} else if m.cmdPalette != nil {
			m, cmds = m.updateCmdPalette(msg)
		} else if m.settings != nil {
			m, cmds = m.updateSettings(msg)
		} else if m.modal != nil || m.focus == tuiFocusModal {
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
		} else if m.termZoomed {
			m, cmds = m.updateTermZoom(msg)
		} else if m.ctView {
			m, cmds = m.updateControlTower(msg)
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

func (m tuiModel) updateTermZoom(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.ws.closeAll()
		return m, []tea.Cmd{tea.Quit}
	case "esc", "q":
		m.termZoomed = false
	case "enter":
		// Switch fully into the agent's tmux session for interactive control.
		if os.Getenv("TMUX") != "" {
			// Find the agent by locked zoom ID rather than current selection.
			var sessionName string
			for _, st := range m.states {
				for _, a := range st.Agents {
					if a.ID == m.zoomAgentID && a.TmuxSession != nil {
						sessionName = *a.TmuxSession
					}
				}
			}
			if sessionName != "" {
				name := sessionName
				return m, []tea.Cmd{func() tea.Msg {
					cmd := exec.Command("tmux", "switch-client", "-t", name)
					cmd.Stdin = os.Stdin
					return tuiAttachMsg{err: cmd.Run()}
				}}
			}
		} else {
			m.setFlash("Must be inside tmux to switch — use ctrl+b then select manually", false)
		}
	}
	return m, nil
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
