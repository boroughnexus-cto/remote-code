package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── Sidebar items ────────────────────────────────────────────────────────────

type tuiItemKind int

const (
	tuiItemSession tuiItemKind = iota
	tuiItemAgent
	tuiItemTask
	tuiItemControlTower // pinned fleet supervisor — cannot be deleted
)

type tuiSidebarItem struct {
	kind tuiItemKind
	sid  string // session ID
	eid  string // entity ID (agent or task)
}

// ─── Sidebar helpers ──────────────────────────────────────────────────────────

func (m *tuiModel) rebuildItems() {
	// Save selection identity before rebuild so cursor follows the item, not the index
	var selKind tuiItemKind
	var selSID, selEID string
	if m.cursor < len(m.items) {
		it := m.items[m.cursor]
		selKind, selSID, selEID = it.kind, it.sid, it.eid
	}

	m.items = nil
	// Control Tower is always pinned at the top.
	m.items = append(m.items, tuiSidebarItem{kind: tuiItemControlTower})
	for _, sess := range m.sessions {
		m.items = append(m.items, tuiSidebarItem{kind: tuiItemSession, sid: sess.ID})
		if m.collapsedSessions[sess.ID] {
			continue // skip children when collapsed
		}
		st, ok := m.states[sess.ID]
		if !ok {
			continue
		}
		for _, a := range st.Agents {
			m.items = append(m.items, tuiSidebarItem{kind: tuiItemAgent, sid: sess.ID, eid: a.ID})
		}
		for _, t := range st.Tasks {
			m.items = append(m.items, tuiSidebarItem{kind: tuiItemTask, sid: sess.ID, eid: t.ID})
		}
	}

	// Prune termContent for agents no longer in any session (prevents unbounded growth)
	liveAgents := make(map[string]bool)
	for _, sess := range m.sessions {
		for _, a := range m.states[sess.ID].Agents {
			liveAgents[a.ID] = true
		}
	}
	for aid := range m.termContent {
		if !liveAgents[aid] {
			delete(m.termContent, aid)
		}
	}

	// Restore cursor by identity; fall back to clamping if not found
	for i, it := range m.items {
		if it.kind == selKind && it.sid == selSID && it.eid == selEID {
			m.cursor = i
			return
		}
	}
	if m.cursor >= len(m.items) {
		m.cursor = max(0, len(m.items)-1)
	}
}

func (m tuiModel) selItem() *tuiSidebarItem {
	if m.cursor < len(m.items) {
		it := m.items[m.cursor]
		return &it
	}
	return nil
}

func (m tuiModel) selSessionID() string {
	it := m.selItem()
	if it != nil && it.kind != tuiItemControlTower {
		return it.sid
	}
	// Control Tower selected (or no item): fall back to first session.
	if len(m.sessions) > 0 {
		return m.sessions[0].ID
	}
	return ""
}

func (m tuiModel) lookupAgent(sid, aid string) *tuiAgent {
	st := m.states[sid]
	for i := range st.Agents {
		if st.Agents[i].ID == aid {
			return &st.Agents[i]
		}
	}
	return nil
}

func (m tuiModel) lookupTask(sid, tid string) *tuiTask {
	st := m.states[sid]
	for i := range st.Tasks {
		if st.Tasks[i].ID == tid {
			return &st.Tasks[i]
		}
	}
	return nil
}

func (m tuiModel) lookupSession(sid string) *tuiSession {
	for i := range m.sessions {
		if m.sessions[i].ID == sid {
			return &m.sessions[i]
		}
	}
	return nil
}

// navigateTo sets the sidebar cursor to the given (kind, sid, eid) triple.
// It ensures the session is not collapsed before rebuilding, then scans for
// the item. Used by the triage view and any feature that needs to jump to a
// specific item programmatically.
func (m *tuiModel) navigateTo(kind tuiItemKind, sid, eid string) {
	// Ensure the target session is visible
	delete(m.collapsedSessions, sid)
	m.rebuildItems()
	for i, it := range m.items {
		if it.kind == kind && it.sid == sid && it.eid == eid {
			m.cursor = i
			return
		}
	}
}

func (m tuiModel) updateSidebar(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	var cmds []tea.Cmd
	// Cancel pending confirmation on any key except the confirm key
	if m.pendingConfirm != nil && msg.String() != m.pendingConfirm.confirmKey {
		m.pendingConfirm = nil
		m.flash = ""
	}
	switch msg.String() {
	case "q", "ctrl+c":
		m.ws.closeAll()
		cmds = append(cmds, tea.Quit)

	case "esc":
		// Open settings overlay when no other overlay is active
		if m.modal == nil && m.settings == nil && !m.opsView && m.cmdPalette == nil {
			st := newTUISettings(m.client)
			m.settings = st
			// Kick off data load for the first active section
			if st.activeSection() != nil {
				cmds = append(cmds, st.activeSection().Init())
			}
		}

	case "up", "k", "w":
		if m.cursor > 0 {
			m.cursor--
			m.updateVP()
		}
	case "down", "j", "s":
		if m.cursor < len(m.items)-1 {
			m.cursor++
			m.updateVP()
		}

	case "tab", "/":
		m.focus = tuiFocusInput
		cmds = append(cmds, m.chatInput.Focus())

	case "enter":
		it := m.selItem()
		if it != nil {
			switch it.kind {
			case tuiItemControlTower:
				m.ctView = true
				return m, nil

			case tuiItemSession:
				// Toggle collapse
				if m.collapsedSessions[it.sid] {
					delete(m.collapsedSessions, it.sid)
				} else {
					m.collapsedSessions[it.sid] = true
				}
				m.rebuildItems()
				m.updateVP()
			case tuiItemAgent:
				agent := m.lookupAgent(it.sid, it.eid)
				if agent != nil && agent.TmuxSession != nil {
					// Zoom view: keeps HUD visible, shows live terminal capture.
					// Press Enter again to switch into the tmux session; Esc to return.
					m.termZoomed = true
					m.zoomAgentID = agent.ID
				} else {
					m.setFlash("No tmux session — press s to spawn", false)
				}
			}
		}

	case "alt+s":
		it := m.selItem()
		if it != nil && it.kind == tuiItemAgent {
			agent := m.lookupAgent(it.sid, it.eid)
			if agent != nil && agent.TmuxSession == nil {
				path := "/api/swarm/sessions/" + it.sid + "/agents/" + it.eid + "/spawn"
				cmds = append(cmds, m.client.post("spawn", path, nil))
			} else {
				m.setFlash("Agent already running", false)
			}
		}

	case "d":
		it := m.selItem()
		if it != nil && it.kind == tuiItemAgent {
			agent := m.lookupAgent(it.sid, it.eid)
			if agent != nil && agent.TmuxSession != nil {
				if m.pendingConfirm != nil && m.pendingConfirm.confirmKey == "d" && m.pendingConfirm.targetItem.eid == it.eid {
					// Second press — confirmed, execute
					cmds = append(cmds, m.pendingConfirm.onConfirm)
					m.pendingConfirm = nil
				} else {
					// First press — require confirmation
					path := "/api/swarm/sessions/" + it.sid + "/agents/" + it.eid + "/despawn"
					itemCopy := *it
					m.pendingConfirm = &pendingConfirmAction{
						label:      "Despawn " + agent.Name + "? Press d again to confirm, Esc to cancel",
						confirmKey: "d",
						targetItem: &itemCopy,
						onConfirm:  m.client.post("despawn", path, nil),
					}
					m.setFlash(m.pendingConfirm.label, true)
				}
			} else {
				m.setFlash("Agent not running", false)
			}
		}

	case "i":
		// Inject a direct message into a live agent's Claude Code session
		it := m.selItem()
		if it != nil && it.kind == tuiItemAgent {
			agent := m.lookupAgent(it.sid, it.eid)
			if agent != nil && agent.TmuxSession != nil {
				mo := newTUIModal(tuiModalInjectAgent, it.sid)
				mo.eid = it.eid
				m.modal = mo
				m.focus = tuiFocusModal
			} else {
				m.setFlash("Agent not running — cannot inject", false)
			}
		}

	case "D":
		// Delete agent or task record (agent must be offline; tasks always deletable)
		it := m.selItem()
		if it == nil {
			break
		}
		switch it.kind {
		case tuiItemAgent:
			agent := m.lookupAgent(it.sid, it.eid)
			if agent == nil {
				break
			}
			if agent.TmuxSession != nil {
				m.setFlash("Stop agent first (d) before deleting", true)
				break
			}
			if m.pendingConfirm != nil && m.pendingConfirm.confirmKey == "D" && m.pendingConfirm.targetItem.eid == it.eid {
				// Second press — execute
				cmds = append(cmds, m.pendingConfirm.onConfirm)
				m.pendingConfirm = nil
			} else {
				itemCopy := *it
				m.pendingConfirm = &pendingConfirmAction{
					label:      "Delete agent " + agent.Name + "? Press D again to confirm",
					confirmKey: "D",
					targetItem: &itemCopy,
					onConfirm:  m.client.deleteItem("delete-agent", "/api/swarm/sessions/"+it.sid+"/agents/"+it.eid),
				}
				m.setFlash(m.pendingConfirm.label, true)
			}
		case tuiItemTask:
			task := m.lookupTask(it.sid, it.eid)
			if task == nil {
				break
			}
			if m.pendingConfirm != nil && m.pendingConfirm.confirmKey == "D" && m.pendingConfirm.targetItem.eid == it.eid {
				cmds = append(cmds, m.pendingConfirm.onConfirm)
				m.pendingConfirm = nil
			} else {
				itemCopy := *it
				m.pendingConfirm = &pendingConfirmAction{
					label:      "Delete task \"" + truncStr(task.Title, 30) + "\"? Press D again to confirm",
					confirmKey: "D",
					targetItem: &itemCopy,
					onConfirm:  m.client.deleteItem("delete-task", "/api/swarm/sessions/"+it.sid+"/tasks/"+it.eid),
				}
				m.setFlash(m.pendingConfirm.label, true)
			}
		}

	case "N":
		// View and add agent notes
		it := m.selItem()
		if it != nil && it.kind == tuiItemAgent {
			m.notesView = true
			m.notesSID = it.sid
			m.notesAgentID = it.eid
			m.notesItems = nil
			cmds = append(cmds, m.client.fetchNotes(it.sid, it.eid))
		}

	case "S":
		// Manually set a task's stage
		it := m.selItem()
		if it != nil && it.kind == tuiItemTask {
			task := m.lookupTask(it.sid, it.eid)
			if task != nil {
				mo := newTUIEditModal(tuiModalEditTaskStage, it.sid, it.eid, []string{task.Stage})
				m.modal = mo
				m.focus = tuiFocusModal
			}
		}

	case "X":
		it := m.selItem()
		if it != nil && it.kind == tuiItemControlTower {
			m.setFlash("Control Tower is permanent — it cannot be deleted", false)
			break
		}
		if it != nil && it.kind == tuiItemSession {
			sess := m.lookupSession(it.sid)
			if sess != nil {
				// Open typed-confirm modal — user must type session name to confirm
				itemCopy := *it
				sessionName := sess.Name
				m.pendingConfirm = &pendingConfirmAction{
					label:      "delete-session",
					confirmKey: "X",
					targetItem: &itemCopy,
					onConfirm:  m.client.deleteItem("delete-session", "/api/swarm/sessions/"+it.sid),
				}
				m.modal = newTUIConfirmModal(
					"Delete session \""+sessionName+"\"?",
					func(s string) bool { return s == sessionName },
				)
				m.focus = tuiFocusModal
				// Move cursor up if at end
				if m.cursor > 0 {
					m.cursor--
				}
			}
		}

	case "r":
		it := m.selItem()
		if it != nil {
			path := "/api/swarm/sessions/" + it.sid + "/resume"
			cmds = append(cmds, m.client.post("resume", path, nil))
		}

	case "+":
		sid := m.selSessionID()
		if sid != "" {
			m.modal = newTUIModal(tuiModalQuickAgent, sid)
			m.focus = tuiFocusModal
		}

	case "n":
		sid := m.selSessionID()
		if sid != "" {
			m.rolePicker = newRolePicker(sid, false)
			cmds = append(cmds, fetchRolesForPicker(m.client))
		}

	case "t":
		sid := m.selSessionID()
		if sid != "" {
			m.modal = newTUIModal(tuiModalNewTask, sid)
			m.focus = tuiFocusModal
		}

	case "c":
		m.modal = newTUIModal(tuiModalNewSession, "")
		m.focus = tuiFocusModal

	case "e":
		sid := m.selSessionID()
		if sid != "" {
			m.escView = true
			m.escCursor = 0
			m.escActive = nil
			m.escInputting = false
		}

	case "E":
		// Edit selected item in-place
		it := m.selItem()
		if it != nil {
			switch it.kind {
			case tuiItemSession:
				sess := m.lookupSession(it.sid)
				if sess != nil {
					m.modal = newTUIEditModal(tuiModalEditSession, it.sid, "", []string{sess.Name})
					m.focus = tuiFocusModal
				}
			case tuiItemAgent:
				agent := m.lookupAgent(it.sid, it.eid)
				if agent != nil {
					mission := ""
					if agent.Mission != nil {
						mission = *agent.Mission
					}
					project := ""
					if agent.Project != nil {
						project = *agent.Project
					}
					repoPath := ""
					if agent.RepoPath != nil {
						repoPath = *agent.RepoPath
					}
					m.modal = newTUIEditModal(tuiModalEditAgent, it.sid, it.eid, []string{agent.Name, mission, project, repoPath})
					m.focus = tuiFocusModal
				}
			case tuiItemTask:
				task := m.lookupTask(it.sid, it.eid)
				if task != nil {
					desc := ""
					if task.Description != nil {
						desc = *task.Description
					}
					proj := ""
					if task.Project != nil {
						proj = *task.Project
					}
					m.modal = newTUIEditModal(tuiModalEditTask, it.sid, it.eid, []string{task.Title, desc, proj})
					m.focus = tuiFocusModal
				}
			}
		}

	case "g":
		sid := m.selSessionID()
		if sid != "" {
			m.goalView = true
			m.goalCursor = 0
		}

	case "A":
		sid := m.selSessionID()
		if sid != "" {
			st := m.states[sid]
			newVal := !st.Session.AutopilotEnabled
			body := map[string]interface{}{"enabled": newVal}
			if st.Session.AutopilotPlaneProject != nil {
				body["plane_project_id"] = *st.Session.AutopilotPlaneProject
			}
			path := "/api/swarm/sessions/" + sid + "/autopilot"
			cmds = append(cmds, m.client.patch("autopilot", path, body))
			if newVal {
				m.setFlash("Autopilot ON — syncing Plane issues", false)
			} else {
				m.setFlash("Autopilot OFF", false)
			}
		}

	case "W":
		sid := m.selSessionID()
		if sid != "" {
			m.workQueueView = true
			m.workQueueSID = sid
			m.workQueueItems = nil
			path := "/api/swarm/sessions/" + sid + "/plane/issues?state_group=backlog,unstarted"
			cmds = append(cmds, m.client.get("workqueue", path))
		}

	case "M":
		// Toggle swarm mode for the selected agent.
		// Swarm mode passes --swarm to claude, enabling Claude's built-in sub-agent spawning.
		// Takes effect on next spawn/relaunch.
		it := m.selItem()
		if it == nil || it.kind != tuiItemAgent {
			m.setFlash("Select an agent to toggle swarm mode", false)
			break
		}
		agent := m.lookupAgent(it.sid, it.eid)
		if agent == nil {
			break
		}
		newMode := !agent.SwarmMode
		modeStr := "off"
		if newMode {
			modeStr = "on"
		}
		path := "/api/swarm/sessions/" + it.sid + "/agents/" + agent.ID
		cmds = append(cmds, m.client.patch("swarm-mode-"+modeStr, path, map[string]interface{}{"swarm_mode": newMode}))
		m.setFlash(fmt.Sprintf("Swarm mode %s for %s (takes effect on next spawn)", modeStr, agent.Name), false)

	case "P":
		// Open Settings → Agent Personas, pre-selecting the current agent's role.
		role := "worker"
		if it := m.selItem(); it != nil && it.kind == tuiItemAgent {
			if ag := m.lookupAgent(it.sid, it.eid); ag != nil {
				role = ag.Role
			}
		}
		if m.settings == nil {
			m.settings = newTUISettings(m.client)
		}
		m.settings.active = 0 // Personas tab is always index 0
		for _, sec := range m.settings.sections {
			if ps, ok := sec.(*personasSection); ok {
				ps.pendingRole = role
				cmds = append(cmds, ps.Init())
			}
		}

	case "C":
		// Open context picker for the selected session
		it := m.selItem()
		sid := ""
		if it != nil && it.kind == tuiItemSession {
			sid = it.sid
		} else if it != nil && it.kind == tuiItemControlTower {
			m.setFlash("Select a session to assign a context", false)
			break
		} else {
			sid = m.selSessionID()
		}
		if sid != "" {
			m.ctxPicker = newCtxPicker(sid)
			cmds = append(cmds, fetchContextsForPicker(m.client))
		}

	case "o", "O":
		m.opsView = true
		m.opsCursor = 0

	case "I":
		m.icingaView = true
		m.icingaServices = nil // show loading state
		m.icingaTopCur = 0
		m.icingaBotCur = 0
		m.icingaFocus = 0
		cmds = append(cmds, m.client.get("icinga", "/api/icinga/services"))

	case "L":
		sid := m.selSessionID()
		if sid != "" {
			evts := m.vpRawEvents[sid]
			m.evtLogView = true
			m.evtDetailView = nil
			m.evtCursor = max(0, len(evts)-1)
		}

	case "T":
		m.triageView = true
		m.triageCursor = 0

	case "R":
		cmds = append(cmds, m.client.fetchAll())

	case "alt+f":
		// Capture current TUI state and open feedback modal
		fc := captureFeedbackState(&m)
		m.feedbackCapture = &fc
		m.feedbackSubmitting = false
		m.modal = newTUIModal(tuiModalFeedback, m.selSessionID())
		m.focus = tuiFocusModal
	}
	return m, cmds
}

// sessionExceptions counts actionable problems in a session state.
// blocked = tasks in {blocked, needs_human, failed, timed_out}
// escalations = open escalations
// stuck = agents with status "stuck"
func sessionExceptions(st tuiState) (blocked, escalations, stuck int) {
	for _, t := range st.Tasks {
		switch t.Stage {
		case "blocked", "needs_human", "failed", "timed_out":
			blocked++
		}
	}
	escalations = len(st.Escalations)
	for _, a := range st.Agents {
		if a.Status == "stuck" {
			stuck++
		}
	}
	return
}

func (m tuiModel) viewSidebar(h int) string {
	var lines []string
	for i, it := range m.items {
		if len(lines) >= h {
			break
		}
		sel := i == m.cursor && m.focus == tuiFocusSidebar
		switch it.kind {
		case tuiItemControlTower:
			mode := strings.ToUpper(m.statusBar.fleetMode)
			if mode == "" {
				mode = "NORMAL"
			}
			var modeC lipgloss.Color
			switch m.statusBar.fleetMode {
			case "contain":
				modeC = colorRed
			case "stabilize":
				modeC = colorOrange
			default:
				modeC = colorGreen
			}
			modeSuffix := " " + lipgloss.NewStyle().Foreground(modeC).Render(mode)
			base := lipgloss.NewStyle().Foreground(colorTeal).Bold(true)
			if sel {
				base = base.Background(colorSubtle)
				modeSuffix = lipgloss.NewStyle().Foreground(modeC).Background(colorSubtle).Render(mode)
				modeSuffix = " " + modeSuffix
			}
			lines = append(lines, base.Width(tuiSidebarW-len([]rune(mode))-1).Render("⊕ Control Tower")+modeSuffix)
			// Thin separator below CT
			if len(lines) < h {
				lines = append(lines, dimStyle.Width(tuiSidebarW).Render(strings.Repeat("─", tuiSidebarW)))
			}

		case tuiItemSession:
			sess := m.lookupSession(it.sid)
			if sess == nil {
				continue
			}
			st := m.states[it.sid]
			live := 0
			for _, a := range st.Agents {
				if a.TmuxSession != nil {
					live++
				}
			}
			blocked, escalations, stuck := sessionExceptions(st)
			pendingDel := m.pendingConfirm != nil && m.pendingConfirm.confirmKey == "X" && m.pendingConfirm.targetItem != nil && m.pendingConfirm.targetItem.sid == it.sid
			collapsed := m.collapsedSessions[it.sid]
			collapsePrefix := "▼ "
			if collapsed {
				collapsePrefix = "▶ "
			}
			ctxBadge := ""
			if sess.ContextName != nil {
				ctxBadge = " " + lipgloss.NewStyle().Foreground(colorPurple).Render("@"+truncStr(*sess.ContextName, 10))
			}
			name := truncStr(sess.Name, tuiSidebarW-9)
			suffix := ""
			if pendingDel {
				suffix = lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render(" ✗?")
			} else {
				// Exception badge: [2b 1e 1s]
				var badges []string
				if blocked > 0 {
					badges = append(badges, fmt.Sprintf("%db", blocked))
				}
				if escalations > 0 {
					badges = append(badges, fmt.Sprintf("%de", escalations))
				}
				if stuck > 0 {
					badges = append(badges, fmt.Sprintf("%ds", stuck))
				}
				if len(badges) > 0 {
					badgeStr := strings.Join(badges, " ")
					suffix = " " + lipgloss.NewStyle().Foreground(colorRed).Render("["+badgeStr+"]")
				} else if live > 0 {
					suffix = lipgloss.NewStyle().Foreground(colorTeal).Render(fmt.Sprintf(" ·%d", live))
				}
				// Context badge shown alongside (appended after exceptions)
				suffix += ctxBadge
			}
			base := lipgloss.NewStyle().Foreground(colorText).Bold(true)
			if pendingDel {
				base = base.Foreground(colorRed)
			}
			if sel {
				base = base.Background(colorSubtle)
			}
			lines = append(lines, base.Width(tuiSidebarW).Render(collapsePrefix+name)+suffix)

		case tuiItemAgent:
			agent := m.lookupAgent(it.sid, it.eid)
			if agent == nil {
				continue
			}
			_, roleColor := tuiRoleConfig(agent.Role)
			spinFrame, statusLabel, statusColor := tuiStatusConfig(agent.Status, m.frame)
			bg := SpritePanelBG
			if sel {
				bg = colorSubtle
			}
			// 4-line sprite portrait (6 chars wide)
			sprite := GetAgentSprite(agent.Role, agent.Status, m.frame)
			spriteStyled := lipgloss.NewStyle().Background(bg).Render(sprite)

			// Info column (tuiSidebarW - 6 sprite cols - 1 gap)
			infoW := tuiSidebarW - 7
			faint := agent.TmuxSession == nil
			nameStyle := lipgloss.NewStyle().Foreground(roleColor).Bold(true)
			if faint {
				nameStyle = nameStyle.Faint(true)
			}
			statusStyle := lipgloss.NewStyle().Foreground(statusColor)
			dimInfo := lipgloss.NewStyle().Foreground(colorDim)
			if sel {
				nameStyle = nameStyle.Background(colorSubtle)
				statusStyle = statusStyle.Background(colorSubtle)
				dimInfo = dimInfo.Background(colorSubtle)
			}
			infoLine0 := nameStyle.Render(truncStr(agent.Name, infoW))
			statusText := spinFrame + " " + truncStr(statusLabel, infoW-2)
			if ag := ageStr(agent.StatusChangedAt); ag != "" {
				switch agent.Status {
				case "waiting", "stuck":
					statusText += " " + lipgloss.NewStyle().Foreground(ageColor(agent.StatusChangedAt, agent.Status)).Render(ag)
				}
			}
			infoLine1 := statusStyle.Render(statusText)
			infoLine2 := ""
			if agent.Mission != nil {
				infoLine2 = dimInfo.Render(truncStr(*agent.Mission, infoW))
			}
			// Line 3: role, plus git branch badge if available
			infoLine3 := dimInfo.Render(truncStr(agent.Role, infoW))
			if gs, ok := m.gitStatus[agent.ID]; ok && gs.Branch != "" {
				branchBadge := lipgloss.NewStyle().Foreground(colorPurple).Render("⎇ " + truncStr(gs.Branch, infoW-len(agent.Role)-2))
				if gs.Dirty {
					branchBadge += lipgloss.NewStyle().Foreground(colorOrange).Render("✎")
				}
				roleStr := truncStr(agent.Role, infoW/2)
				infoLine3 = dimInfo.Render(roleStr) + "  " + branchBadge
			}
			if agent.SwarmMode {
				infoLine3 += " " + lipgloss.NewStyle().Foreground(colorYellow).Render("⟲")
			}

			// Join sprite + info side by side, 4 rows each
			spriteLines := strings.Split(spriteStyled, "\n")
			infoLines := []string{infoLine0, infoLine1, infoLine2, infoLine3}
			for i := 0; i < 4; i++ {
				sl := ""
				if i < len(spriteLines) {
					sl = spriteLines[i]
				}
				il := ""
				if i < len(infoLines) {
					il = infoLines[i]
				}
				rowBg := lipgloss.NewStyle()
				if sel {
					rowBg = rowBg.Background(colorSubtle)
				}
				lines = append(lines, rowBg.Width(tuiSidebarW).Render(sl+" "+il))
			}

		case tuiItemTask:
			task := m.lookupTask(it.sid, it.eid)
			if task == nil {
				continue
			}
			stageC := tuiStageColor(task.Stage)
			stageTag := shortStage(task.Stage)
			taskAgeSuffix := ""
			switch task.Stage {
			case "blocked", "needs_human":
				if a := ageStr(task.StageChangedAt); a != "" {
					taskAgeSuffix = lipgloss.NewStyle().Foreground(ageColor(task.StageChangedAt, task.Stage)).Render(a)
				}
			}
			if taskAgeSuffix != "" {
				stageTag += " " + taskAgeSuffix
			}
			stageStr := lipgloss.NewStyle().Foreground(stageC).Render(fmt.Sprintf("[%-6s]", stageTag))
			dot, dotStyle := ciDot(task)
			dotStr := dotStyle.Render(dot)
			title := truncStr(task.Title, tuiSidebarW-12)
			row := fmt.Sprintf("  %s%-*s %s", dotStr, tuiSidebarW-12, title, stageStr)
			base := lipgloss.NewStyle().Foreground(colorDim)
			if sel {
				base = base.Background(colorSubtle).Foreground(colorText)
			}
			lines = append(lines, base.Width(tuiSidebarW).Render(row))
		}
	}
	// Pad to height
	for len(lines) < h {
		lines = append(lines, lipgloss.NewStyle().Width(tuiSidebarW).Render(""))
	}
	return strings.Join(lines[:h], "\n")
}
