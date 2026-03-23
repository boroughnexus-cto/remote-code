package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func (m tuiModel) updateEventLog(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	sid := m.selSessionID()
	evts := m.vpRawEvents[sid]
	n := len(evts)
	state := m.states[sid]

	agentNames := make(map[string]string)
	for _, a := range state.Agents {
		agentNames[a.ID] = a.Name
	}

	switch msg.String() {
	case "q", "esc":
		m.evtLogView = false
		m.evtDetailView = nil
	case "j", "down":
		if m.evtCursor < n-1 {
			m.evtCursor++
		}
	case "k", "up":
		if m.evtCursor > 0 {
			m.evtCursor--
		}
	case "g":
		m.evtCursor = 0
	case "G":
		m.evtCursor = max(0, n-1)
	case "f":
		// Toggle filter by agent of event under cursor
		if m.evtCursor >= 0 && m.evtCursor < n {
			ev := evts[m.evtCursor]
			name := agentNames[ev.AgentID]
			if name != "" {
				if m.evtAgentFilter == name {
					m.evtAgentFilter = ""
				} else {
					m.evtAgentFilter = name
				}
				// Refilter
				for _, sess := range m.sessions {
					if st, ok := m.states[sess.ID]; ok {
						m.appendEvents(sess.ID, st)
					}
				}
				m.updateVP()
				// Reposition cursor at bottom of filtered results
				evts = m.vpRawEvents[sid]
				m.evtCursor = max(0, len(evts)-1)
			}
		}
	case "F":
		m.evtAgentFilter = ""
		for _, sess := range m.sessions {
			if st, ok := m.states[sess.ID]; ok {
				m.appendEvents(sess.ID, st)
			}
		}
		m.updateVP()
		evts = m.vpRawEvents[sid]
		m.evtCursor = max(0, len(evts)-1)
	case "enter":
		if m.evtCursor >= 0 && m.evtCursor < n {
			ev := evts[m.evtCursor]
			m.evtDetailView = &ev
		}
	}
	return m, nil
}

func (m tuiModel) updateNotesView(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	var cmds []tea.Cmd
	n := len(m.notesItems)
	switch msg.String() {
	case "q", "esc":
		m.notesView = false
		m.notesItems = nil
		m.notesCursor = 0
	case "j", "down":
		if m.notesCursor < n-1 {
			m.notesCursor++
		}
	case "k", "up":
		if m.notesCursor > 0 {
			m.notesCursor--
		}
	case "g":
		m.notesCursor = 0
	case "G":
		m.notesCursor = max(0, n-1)
	case "a":
		mo := newTUIModal(tuiModalAddNote, m.notesSID)
		mo.eid = m.notesAgentID
		m.modal = mo
		m.focus = tuiFocusModal
	}
	return m, cmds
}

func (m tuiModel) viewNotesScreen() string {
	var sb strings.Builder
	sb.WriteString(m.viewHUD() + "\n")

	// Find agent name for the header
	agentName := m.notesAgentID
	if len(agentName) > 8 {
		agentName = agentName[:8]
	}
	if agent := m.lookupAgent(m.notesSID, m.notesAgentID); agent != nil {
		agentName = agent.Name
	}

	title := lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("Notes  —  " + agentName)
	sb.WriteString(title + "\n\n")

	if m.notesItems == nil {
		sb.WriteString(dimStyle.Render("  Loading…") + "\n")
	} else if len(m.notesItems) == 0 {
		sb.WriteString(dimStyle.Render("  No notes yet — press 'a' to add one.") + "\n")
	} else {
		n := len(m.notesItems)
		visible := m.h - 8
		if visible < 5 {
			visible = 5
		}
		start := m.notesCursor - visible/2
		if start < 0 {
			start = 0
		}
		end := start + visible
		if end > n {
			end = n
		}
		if end-start < visible && start > 0 {
			start = end - visible
			if start < 0 {
				start = 0
			}
		}

		byColor := map[string]lipgloss.Color{
			"user":         colorTeal,
			"agent":        colorYellow,
			"orchestrator": colorGreen,
		}

		for i := start; i < end; i++ {
			note := m.notesItems[i]
			ts := time.Unix(note.CreatedAt, 0).Format("15:04:05")
			byC := byColor[note.CreatedBy]
			if byC == "" {
				byC = colorDim
			}
			byStr := lipgloss.NewStyle().Foreground(byC).Render(fmt.Sprintf("%-13s", note.CreatedBy))

			if i == m.notesCursor {
				// Full-width highlight, content unwrapped
				line := fmt.Sprintf("  %s  %s  %s", ts, byStr, note.Content)
				sb.WriteString(lipgloss.NewStyle().
					Background(colorSubtle).Foreground(colorText).
					Width(m.w-2).Render(line) + "\n")
			} else {
				content := truncStr(note.Content, m.w-40)
				line := fmt.Sprintf("  %s  %s  %s", ts, byStr, dimStyle.Render(content))
				sb.WriteString(line + "\n")
			}
		}
		if n > visible {
			sb.WriteString(dimStyle.Render(fmt.Sprintf("\n  %d/%d", m.notesCursor+1, n)))
		}
	}

	sb.WriteString("\n" + dimStyle.Render("  j/k navigate  ·  a add note  ·  g/G first/last  ·  q close"))
	return sb.String()
}

func (m tuiModel) updateEscalation(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	var cmds []tea.Cmd
	sid := m.selSessionID()
	st := m.states[sid]
	escs := st.Escalations

	if m.escInputting {
		switch msg.String() {
		case "esc":
			m.escInputting = false
			m.escInput.Blur()
		case "enter":
			text := strings.TrimSpace(m.escInput.Value())
			if text != "" && m.escActive != nil {
				path := "/api/swarm/sessions/" + sid + "/escalations/" + m.escActive.ID + "/respond"
				cmds = append(cmds, m.client.post("esc-respond", path, map[string]string{"text": text}))
				m.escInput.Reset()
				m.escInput.Blur()
				m.escInputting = false
				m.escActive = nil
			}
		default:
			var cmd tea.Cmd
			m.escInput, cmd = m.escInput.Update(msg)
			cmds = append(cmds, cmd)
		}
		return m, cmds
	}

	switch msg.String() {
	case "e", "esc", "q":
		m.escView = false
	case "up", "k":
		if m.escCursor > 0 {
			m.escCursor--
		}
	case "down", "j":
		if m.escCursor < len(escs)-1 {
			m.escCursor++
		}
	case "enter":
		if m.escCursor < len(escs) {
			esc := escs[m.escCursor]
			m.escActive = &esc
			m.escInputting = true
			cmds = append(cmds, m.escInput.Focus())
		}
	}
	return m, cmds
}

// ─── Icinga monitor view ──────────────────────────────────────────────────────

func (m tuiModel) updateIcingaView(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	// Non-OK services (top pane data) and recent alerts (bottom pane — same set sorted by time)
	nonOK := icingaNonOK(m.icingaServices)
	n := len(m.icingaServices)
	nb := len(nonOK)

	switch msg.String() {
	case "q", "esc":
		m.icingaView = false
	case "tab":
		m.icingaFocus = 1 - m.icingaFocus
	case "r", "R":
		return m, []tea.Cmd{m.client.get("icinga", "/api/icinga/services")}
	case "j", "down":
		if m.icingaFocus == 0 {
			if m.icingaTopCur < n-1 {
				m.icingaTopCur++
			}
		} else {
			if m.icingaBotCur < nb-1 {
				m.icingaBotCur++
			}
		}
	case "k", "up":
		if m.icingaFocus == 0 {
			if m.icingaTopCur > 0 {
				m.icingaTopCur--
			}
		} else {
			if m.icingaBotCur > 0 {
				m.icingaBotCur--
			}
		}
	case "g":
		if m.icingaFocus == 0 {
			m.icingaTopCur = 0
		} else {
			m.icingaBotCur = 0
		}
	case "G":
		if m.icingaFocus == 0 {
			m.icingaTopCur = max(0, n-1)
		} else {
			m.icingaBotCur = max(0, nb-1)
		}
	case "pgup", "ctrl+b":
		bodyH := m.h - 6
		if bodyH < 8 {
			bodyH = 8
		}
		topH := bodyH * 60 / 100
		botH := bodyH - topH
		if m.icingaFocus == 0 {
			page := max(1, topH-1)
			m.icingaTopCur = max(0, m.icingaTopCur-page)
		} else {
			page := max(1, botH-1)
			m.icingaBotCur = max(0, m.icingaBotCur-page)
		}
	case "pgdown", "ctrl+f":
		bodyH := m.h - 6
		if bodyH < 8 {
			bodyH = 8
		}
		topH := bodyH * 60 / 100
		botH := bodyH - topH
		if m.icingaFocus == 0 {
			page := max(1, topH-1)
			m.icingaTopCur = min(max(0, n-1), m.icingaTopCur+page)
		} else {
			page := max(1, botH-1)
			m.icingaBotCur = min(max(0, nb-1), m.icingaBotCur+page)
		}
	}
	return m, nil
}

// icingaNonOK returns non-OK services sorted by last_change descending (most recent alert first).
func icingaNonOK(svcs []IcingaService) []IcingaService {
	var out []IcingaService
	for _, s := range svcs {
		if s.State != 0 {
			out = append(out, s)
		}
	}
	// Sort by last_change desc (most recently fired first).
	for i := 0; i < len(out)-1; i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].LastChange > out[i].LastChange {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func icingaStateLabel(state, stateType int, acked, downtime bool) string {
	label := map[int]string{0: "OK", 1: "WARN", 2: "CRIT", 3: "UNKN"}[state]
	if stateType == 0 {
		label += "(SOFT)"
	}
	color := map[int]lipgloss.Color{
		0: colorGreen, 1: colorOrange, 2: colorRed, 3: colorDim,
	}[state]
	s := lipgloss.NewStyle().Foreground(color).Bold(state == 2).Render(label)
	if acked {
		s += dimStyle.Render("✓")
	}
	if downtime {
		s += dimStyle.Render("⏸")
	}
	return s
}

func (m tuiModel) viewIcingaScreen() string {
	svcs := m.icingaServices
	alerts := icingaNonOK(svcs)

	// Counts for header.
	var nCrit, nWarn, nUnkn, nOK int
	for _, s := range svcs {
		switch s.State {
		case 2:
			nCrit++
		case 1:
			nWarn++
		case 3:
			nUnkn++
		default:
			nOK++
		}
	}

	var sb strings.Builder
	sb.WriteString(m.viewHUD() + "\n")

	// ── Header ──
	hdr := lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("Icinga Services")
	if svcs == nil {
		hdr += dimStyle.Render("  Loading…")
	} else {
		hdr += "  " +
			lipgloss.NewStyle().Foreground(colorRed).Render(fmt.Sprintf("%d CRIT", nCrit)) + "  " +
			lipgloss.NewStyle().Foreground(colorOrange).Render(fmt.Sprintf("%d WARN", nWarn)) + "  " +
			lipgloss.NewStyle().Foreground(colorDim).Render(fmt.Sprintf("%d UNKN", nUnkn)) + "  " +
			lipgloss.NewStyle().Foreground(colorGreen).Render(fmt.Sprintf("%d OK", nOK)) +
			dimStyle.Render(fmt.Sprintf("  (%d total)", len(svcs)))
	}
	sb.WriteString(hdr + "\n\n")

	// Split the available height: subtract HUD (2) + header (2) + divider (1) + help (1).
	bodyH := m.h - 6
	if bodyH < 8 {
		bodyH = 8
	}
	topH := bodyH * 60 / 100
	botH := bodyH - topH

	topFocused := m.icingaFocus == 0
	botFocused := m.icingaFocus == 1

	focusBorder := func(focused bool) lipgloss.Color {
		if focused {
			return colorTeal
		}
		return colorDim
	}

	// ── Top pane: all services, sorted CRIT→WARN→UNKN→OK ──
	topTitle := lipgloss.NewStyle().Foreground(focusBorder(topFocused)).Render("All Services")
	sb.WriteString(topTitle + "\n")

	if svcs == nil {
		sb.WriteString(dimStyle.Render("  Fetching…") + "\n")
	} else if len(svcs) == 0 {
		sb.WriteString(dimStyle.Render("  No services found") + "\n")
	} else {
		visible := topH - 1
		if visible < 1 {
			visible = 1
		}
		start := m.icingaTopCur - visible/2
		if start < 0 {
			start = 0
		}
		end := start + visible
		if end > len(svcs) {
			end = len(svcs)
		}
		if end-start < visible && start > 0 {
			start = end - visible
			if start < 0 {
				start = 0
			}
		}
		nameW := max(20, m.w/5)
		hostW := max(14, m.w/8)
		outW := m.w - nameW - hostW - 18
		if outW < 10 {
			outW = 10
		}

		for i := start; i < end; i++ {
			s := svcs[i]
			hostStr := truncStr(s.Host, hostW)
			svcStr := truncStr(s.Service, nameW)
			outStr := truncStr(s.Output, outW)
			rawLine := fmt.Sprintf("%-*s  %-*s  %s", hostW, hostStr, nameW, svcStr, outStr)
			if i == m.icingaTopCur && topFocused {
				rawLine = lipgloss.NewStyle().Background(lipgloss.Color("#1a3050")).Render(rawLine)
			}
			sb.WriteString("  " + icingaStateLabel(s.State, s.StateType, s.Acked, s.Downtime) + "  " + rawLine + "\n")
		}
		if len(svcs) > visible {
			sb.WriteString(dimStyle.Render(fmt.Sprintf("  %d/%d", m.icingaTopCur+1, len(svcs))) + "\n")
		}
	}

	// ── Divider ──
	divColor := colorDim
	sb.WriteString(lipgloss.NewStyle().Foreground(divColor).Render(strings.Repeat("─", m.w)) + "\n")

	// ── Bottom pane: recent non-OK alerts ──
	botTitle := lipgloss.NewStyle().Foreground(focusBorder(botFocused)).Render(
		fmt.Sprintf("Recent Alerts (%d non-OK)", len(alerts)),
	)
	sb.WriteString(botTitle + "\n")

	if len(alerts) == 0 && svcs != nil {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorGreen).Render("  ✓ All services OK") + "\n")
	} else {
		visible := botH - 2
		if visible < 1 {
			visible = 1
		}
		start := m.icingaBotCur - visible/2
		if start < 0 {
			start = 0
		}
		end := start + visible
		if end > len(alerts) {
			end = len(alerts)
		}
		if end-start < visible && start > 0 {
			start = end - visible
			if start < 0 {
				start = 0
			}
		}
		nameW := max(20, m.w/5)
		hostW := max(14, m.w/8)
		outW := m.w - nameW - hostW - 28
		if outW < 10 {
			outW = 10
		}

		for i := start; i < end; i++ {
			s := alerts[i]
			ts := time.Unix(s.LastChange, 0).Format("01-02 15:04")
			hostStr := truncStr(s.Host, hostW)
			svcStr := truncStr(s.Service, nameW)
			outStr := truncStr(s.Output, outW)
			rawLine := fmt.Sprintf("%s  %-*s  %-*s  %s", ts, hostW, hostStr, nameW, svcStr, outStr)
			if i == m.icingaBotCur && botFocused {
				rawLine = lipgloss.NewStyle().Background(lipgloss.Color("#1a3050")).Render(rawLine)
			}
			sb.WriteString("  " + icingaStateLabel(s.State, s.StateType, s.Acked, s.Downtime) + "  " + rawLine + "\n")
		}
		if len(alerts) > visible {
			sb.WriteString(dimStyle.Render(fmt.Sprintf("  %d/%d", m.icingaBotCur+1, len(alerts))) + "\n")
		}
	}

	sb.WriteString("\n" + dimStyle.Render("  Tab switch-pane  ·  j/k navigate  ·  PgUp/PgDn page  ·  g/G first/last  ·  r refresh  ·  q close"))
	return sb.String()
}

func (m tuiModel) viewWorkQueueScreen() string {
	var sb strings.Builder
	sb.WriteString(m.viewHUD() + "\n")

	title := lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("Work Queue — Plane Backlog")
	filterBadge := ""
	if m.workQueueFilter != "" {
		filterBadge = "  " + lipgloss.NewStyle().Foreground(colorOrange).Render("["+m.workQueueFilter+"]") +
			dimStyle.Render(" (f cycles)")
	}
	promotingBadge := ""
	if m.workQueuePromoting {
		promotingBadge = "  " + lipgloss.NewStyle().Foreground(colorYellow).Render("Promoting…")
	}
	sb.WriteString(title + filterBadge + promotingBadge + "\n\n")

	filtered := m.filteredWorkQueueItems()
	if m.workQueueItems == nil {
		sb.WriteString(dimStyle.Render("  Loading…") + "\n")
	} else if len(filtered) == 0 {
		if m.workQueueFilter != "" {
			sb.WriteString(dimStyle.Render(fmt.Sprintf("  No %s priority items in queue.", m.workQueueFilter)) + "\n")
		} else {
			sb.WriteString(dimStyle.Render("  No items in backlog or unstarted state.") + "\n")
		}
	} else {
		priIcon := map[string]string{"urgent": "🔴", "high": "🟠", "medium": "🟡"}

		// Sliding window around cursor
		n := len(filtered)
		visible := m.h - 8
		if visible < 5 {
			visible = 5
		}
		start := m.workQueueCursor - visible/2
		if start < 0 {
			start = 0
		}
		end := start + visible
		if end > n {
			end = n
		}
		if end-start < visible && start > 0 {
			start = end - visible
			if start < 0 {
				start = 0
			}
		}

		for i := start; i < end; i++ {
			item := filtered[i]
			icon := priIcon[item.Priority]
			if icon == "" {
				icon = "⚪"
			}
			seqStr := fmt.Sprintf("#%-4d", item.SequenceID)
			if i == m.workQueueCursor {
				line := fmt.Sprintf("  %s  %s  [%-9s]  %s", icon, seqStr, item.StateGroup, item.Title)
				sb.WriteString(lipgloss.NewStyle().
					Background(colorSubtle).Foreground(colorText).
					Width(m.w-2).Render(line) + "\n")
			} else {
				line := fmt.Sprintf("  %s  %s  [%-9s]  %s", icon, dimStyle.Render(seqStr), item.StateGroup, item.Title)
				sb.WriteString(line + "\n")
			}
		}
		if n > visible {
			sb.WriteString(dimStyle.Render(fmt.Sprintf("\n  %d/%d", m.workQueueCursor+1, n)))
		}
	}

	sb.WriteString("\n" + dimStyle.Render("  j/k navigate  ·  Enter/p promote to goal  ·  f filter priority  ·  g/G first/last  ·  q close"))
	return sb.String()
}

func (m tuiModel) viewEventLogScreen() string {
	sid := m.selSessionID()
	evts := m.vpRawEvents[sid]
	state := m.states[sid]

	agentNames := make(map[string]string)
	for _, a := range state.Agents {
		agentNames[a.ID] = a.Name
	}
	taskTitles := make(map[string]string)
	for _, t := range state.Tasks {
		taskTitles[t.ID] = t.Title
	}

	var sb strings.Builder
	sb.WriteString(m.viewHUD() + "\n")

	title := lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("Event Log")
	filterBadge := ""
	if m.evtAgentFilter != "" {
		filterBadge = " " + lipgloss.NewStyle().Foreground(colorOrange).Render("[agent: "+m.evtAgentFilter+"]") +
			dimStyle.Render(" (F clears)")
	}
	countStr := dimStyle.Render(fmt.Sprintf("  %d events", len(evts)))
	sb.WriteString(title + filterBadge + countStr + "\n\n")

	if len(evts) == 0 {
		sb.WriteString(dimStyle.Render("  No events") + "\n")
	} else {
		// Show a window of events around the cursor
		visible := m.h - 9
		if visible < 5 {
			visible = 5
		}
		start := m.evtCursor - visible/2
		if start < 0 {
			start = 0
		}
		end := start + visible
		if end > len(evts) {
			end = len(evts)
		}
		if end-start < visible && start > 0 {
			start = end - visible
			if start < 0 {
				start = 0
			}
		}

		for i := start; i < end; i++ {
			ev := evts[i]
			ts := time.Unix(ev.Ts, 0).Format("15:04:05")
			agentName := truncStr(agentNames[ev.AgentID], 12)
			typeColored := lipgloss.NewStyle().Foreground(tuiEventColor(ev.Type)).Render(fmt.Sprintf("%-26s", truncStr(ev.Type, 26)))
			payload := truncStr(ev.Payload, 60)
			line := fmt.Sprintf("%s  %s  %-12s  %s", ts, typeColored, agentName, payload)
			if i == m.evtCursor {
				line = lipgloss.NewStyle().Background(lipgloss.Color("#1a3050")).Foreground(lipgloss.Color("#e0e8f0")).Render(
					fmt.Sprintf("%s  %-26s  %-12s  %s", ts, truncStr(ev.Type, 26), agentName, payload),
				)
			}
			sb.WriteString("  " + line + "\n")
		}
		sb.WriteString(fmt.Sprintf("\n"+dimStyle.Render("  %d/%d"), m.evtCursor+1, len(evts)))
	}

	sb.WriteString("\n" + dimStyle.Render("  j/k navigate  ·  Enter detail  ·  f filter-agent  ·  F clear-filter  ·  g/G first/last  ·  q close"))
	return sb.String()
}

func (m tuiModel) viewEventDetailScreen() string {
	ev := m.evtDetailView
	if ev == nil {
		return ""
	}
	sid := m.selSessionID()
	state := m.states[sid]
	agentName := ""
	for _, a := range state.Agents {
		if a.ID == ev.AgentID {
			agentName = a.Name
			break
		}
	}
	taskTitle := ""
	for _, t := range state.Tasks {
		if t.ID == ev.TaskID {
			taskTitle = t.Title
			break
		}
	}

	var sb strings.Builder
	sb.WriteString(m.viewHUD() + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("Event Detail") + "\n\n")

	meta := func(label, val string) string {
		if val == "" {
			return ""
		}
		return lipgloss.NewStyle().Foreground(colorText).Render(label+": ") +
			lipgloss.NewStyle().Foreground(colorTeal).Render(val) + "\n"
	}
	sb.WriteString(meta("Type   ", ev.Type))
	sb.WriteString(meta("Time   ", time.Unix(ev.Ts, 0).Format("2006-01-02 15:04:05")))
	if agentName != "" {
		sb.WriteString(meta("Agent  ", agentName))
	}
	if taskTitle != "" {
		sb.WriteString(meta("Task   ", taskTitle))
	}
	sb.WriteString("\n")

	if ev.Payload == "" {
		sb.WriteString(dimStyle.Render("  (no payload)") + "\n")
	} else {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorText).Render("Payload:") + "\n")
		// Word-wrap payload to terminal width
		wrapW := m.w - 4
		if wrapW < 40 {
			wrapW = 40
		}
		wrapped := wordWrap(ev.Payload, wrapW)
		for _, line := range strings.Split(wrapped, "\n") {
			sb.WriteString("  " + line + "\n")
		}
	}

	sb.WriteString("\n" + dimStyle.Render("  Esc / q to return to event log"))
	return sb.String()
}

func (m tuiModel) viewEscalationScreen() string {
	sid := m.selSessionID()
	var escs []tuiEscalation
	if st, ok := m.states[sid]; ok {
		escs = st.Escalations
	}

	var sb strings.Builder
	sb.WriteString(m.viewHUD() + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(colorOrange).Bold(true).
		Render("  ⚠ ESCALATIONS — pending human responses") + "\n\n")

	if len(escs) == 0 {
		sb.WriteString(dimStyle.Render("  No pending escalations") + "\n")
	} else {
		for i, esc := range escs {
			sel := i == m.escCursor
			prefix := "  "
			style := dimStyle
			if sel {
				prefix = lipgloss.NewStyle().Foreground(colorOrange).Render("▶ ")
				style = lipgloss.NewStyle().Foreground(colorText)
			}
			ts := time.Unix(esc.Ts, 0).Format("15:04")
			maxReason := m.w - 30
			if maxReason < 20 {
				maxReason = 20
			}
			line := fmt.Sprintf("[%s] task:%-8s  %s", ts, truncStr(esc.TaskID, 8), truncStr(esc.Reason, maxReason))
			sb.WriteString(prefix + style.Render(line) + "\n")
		}
	}

	sb.WriteString("\n")
	if m.escActive != nil {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorOrange).Render("  Responding to: "+truncStr(m.escActive.Reason, m.w-20)) + "\n")
		sb.WriteString("  " + m.escInput.View() + "\n")
		sb.WriteString(dimStyle.Render("  Enter to send  ·  Esc to cancel") + "\n")
	} else if len(escs) > 0 {
		sb.WriteString(dimStyle.Render("  Enter to respond  ·  ↑↓/jk navigate") + "\n")
	}
	sb.WriteString("\n" + dimStyle.Render("  e/Esc/q — close escalation view"))
	return sb.String()
}

// ─── Goals view ──────────────────────────────────────────────────────────────

func (m tuiModel) updateWorkQueueView(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	var cmds []tea.Cmd
	filtered := m.filteredWorkQueueItems()
	n := len(filtered)
	switch msg.String() {
	case "q", "esc":
		m.workQueueView = false
		m.workQueueItems = nil
		m.workQueueCursor = 0
		m.workQueueFilter = ""
		m.workQueuePromoting = false
	case "f":
		// Cycle priority filter: "" → "urgent" → "high" → "medium" → "low" → ""
		priorities := []string{"", "urgent", "high", "medium", "low"}
		cur := 0
		for i, p := range priorities {
			if p == m.workQueueFilter {
				cur = i
				break
			}
		}
		m.workQueueFilter = priorities[(cur+1)%len(priorities)]
		m.workQueueCursor = 0
	case "j", "down":
		if m.workQueueCursor < n-1 {
			m.workQueueCursor++
		}
	case "k", "up":
		if m.workQueueCursor > 0 {
			m.workQueueCursor--
		}
	case "g":
		m.workQueueCursor = 0
	case "G":
		m.workQueueCursor = max(0, n-1)
	case "enter", "p":
		// Promote selected item to a session goal.
		// Keep the view open and mark in-flight — close only on tuiDoneMsg.
		if !m.workQueuePromoting && m.workQueueCursor < n {
			item := filtered[m.workQueueCursor]
			path := "/api/swarm/sessions/" + m.workQueueSID + "/goals"
			cmds = append(cmds, m.client.post("create-goal", path,
				map[string]string{"description": item.Title}))
			m.workQueuePromoting = true
		}
	}
	return m, cmds
}

// filteredWorkQueueItems returns work queue items matching the current priority
// filter. Returns all items when workQueueFilter is empty.
func (m tuiModel) filteredWorkQueueItems() []WorkQueueItem {
	if m.workQueueFilter == "" {
		return m.workQueueItems
	}
	var out []WorkQueueItem
	for _, item := range m.workQueueItems {
		if item.Priority == m.workQueueFilter {
			out = append(out, item)
		}
	}
	return out
}

func (m tuiModel) updateGoalView(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	sid := m.selSessionID()
	goals := m.states[sid].Goals
	var cmds []tea.Cmd
	switch msg.String() {
	case "esc", "q":
		m.goalView = false
	case "g":
		m.goalCursor = 0
	case "G":
		if len(goals) > 0 {
			m.goalCursor = len(goals) - 1
		}
	case "up", "k":
		if m.goalCursor > 0 {
			m.goalCursor--
		}
	case "down", "j":
		if m.goalCursor < len(goals)-1 {
			m.goalCursor++
		}
	case "x":
		// Cancel selected active goal
		if m.goalCursor < len(goals) {
			g := goals[m.goalCursor]
			if g.Status == "active" {
				path := "/api/swarm/sessions/" + sid + "/goals/" + g.ID
				cmds = append(cmds, m.client.patch("cancel-goal", path,
					map[string]string{"status": "cancelled"}))
			} else {
				m.setFlash("Goal is not active — cannot cancel", true)
			}
		}
	case "u":
		// Reactivate selected cancelled goal
		if m.goalCursor < len(goals) {
			g := goals[m.goalCursor]
			if g.Status == "cancelled" {
				path := "/api/swarm/sessions/" + sid + "/goals/" + g.ID
				cmds = append(cmds, m.client.patch("reactivate-goal", path,
					map[string]string{"status": "active"}))
			} else {
				m.setFlash("Goal is not cancelled — cannot reactivate", true)
			}
		}
	}
	return m, cmds
}

// goalTaskStats returns (total, done) task counts for a goal.
func (m tuiModel) goalTaskStats(sid, goalID string) (total, done int) {
	st := m.states[sid]
	for _, t := range st.Tasks {
		if t.GoalID != nil && *t.GoalID == goalID {
			total++
			if t.Stage == "complete" || t.Stage == "failed" || t.Stage == "cancelled" || t.Stage == "timed_out" {
				done++
			}
		}
	}
	return
}

func (m tuiModel) viewGoalsScreen() string {
	sid := m.selSessionID()
	if sid == "" {
		return dimStyle.Width(m.w).Render("No session selected")
	}
	goals := m.states[sid].Goals
	var sb strings.Builder
	title := lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Width(m.w).Render("  Goals  (↑↓/jk navigate · x cancel · u reactivate · g/esc close)")
	sb.WriteString(title + "\n")
	sb.WriteString(dimStyle.Render(strings.Repeat("─", m.w)) + "\n")

	if len(goals) == 0 {
		sb.WriteString(dimStyle.Width(m.w).Render("  No goals yet. Use /goal <description> in the chat input.") + "\n")
	}
	for i, g := range goals {
		sel := i == m.goalCursor
		var statusC lipgloss.Color
		var statusIcon string
		switch g.Status {
		case "complete":
			statusC, statusIcon = colorGreen, "✓"
		case "failed":
			statusC, statusIcon = colorRed, "✗"
		case "cancelled":
			statusC, statusIcon = colorDim, "○"
		default:
			statusC, statusIcon = colorTeal, "▶"
		}
		total, done := m.goalTaskStats(sid, g.ID)
		progress := fmt.Sprintf("%d/%d tasks", done, total)

		// Complexity badge
		complexityBadge := ""
		switch g.Complexity {
		case "trivial":
			complexityBadge = dimStyle.Render(" (trv)")
		case "complex":
			complexityBadge = lipgloss.NewStyle().Foreground(colorOrange).Render(" (cpx)")
		}

		// Budget bar (only shown when budget is set)
		budgetBar := ""
		if g.TokenBudget > 0 {
			pct := float64(g.TokensUsed) / float64(g.TokenBudget)
			filled := int(pct * 8)
			if filled > 8 {
				filled = 8
			}
			barColor := colorGreen
			if pct >= 1.0 {
				barColor = colorRed
			} else if pct >= 0.8 {
				barColor = colorOrange
			}
			bar := strings.Repeat("█", filled) + strings.Repeat("░", 8-filled)
			budgetBar = " " + lipgloss.NewStyle().Foreground(barColor).Render(fmt.Sprintf("[%s %3.0f%%]", bar, pct*100))
		}

		descWidth := m.w - 28
		if descWidth < 10 {
			descWidth = 10
		}
		prefix := fmt.Sprintf("  %s  %-*s%s  %s%s",
			lipgloss.NewStyle().Foreground(statusC).Render(statusIcon),
			descWidth,
			truncStr(g.Description, descWidth),
			complexityBadge,
			dimStyle.Render(progress),
			budgetBar,
		)
		row := lipgloss.NewStyle()
		if sel {
			row = row.Background(colorSubtle).Foreground(colorText)
		}
		sb.WriteString(row.Width(m.w).Render(prefix) + "\n")

		// If selected, expand phase tasks below
		if sel && total > 0 {
			st := m.states[sid]
			for _, t := range st.Tasks {
				if t.GoalID == nil || *t.GoalID != g.ID {
					continue
				}
				stageC := tuiStageColor(t.Stage)
				phaseLabel := ""
				if t.Phase != nil {
					phaseLabel = fmt.Sprintf("%-12s", *t.Phase)
				}
				taskRow := fmt.Sprintf("      %s  %s  %s",
					lipgloss.NewStyle().Foreground(stageC).Render(shortStage(t.Stage)),
					dimStyle.Render(phaseLabel),
					truncStr(t.Title, m.w-30),
				)
				sb.WriteString(dimStyle.Width(m.w).Render(taskRow) + "\n")
			}
		}
	}

	sb.WriteString(dimStyle.Render(strings.Repeat("─", m.w)) + "\n")
	return sb.String()
}

// ─── Triage view ──────────────────────────────────────────────────────────────

type triageItem struct {
	kind        tuiItemKind
	sid, eid    string
	sessionName string
	label       string // agent name or task title
	detail      string // status or stage
	age         string // ageStr result
	severity    int    // 3=escalation/stuck, 2=blocked/needs_human, 1=needs_review
}

// buildTriageItems collects all actionable items across all sessions, sorted
// by severity DESC then by age (oldest first within same severity).
func buildTriageItems(m tuiModel) []triageItem {
	var items []triageItem
	for _, sess := range m.sessions {
		st := m.states[sess.ID]
		// Escalations (severity 3)
		for _, esc := range st.Escalations {
			agent := ""
			for _, a := range st.Agents {
				if a.ID == esc.AgentID {
					agent = a.Name
					break
				}
			}
			label := agent
			if label == "" {
				label = esc.AgentID[:8]
			}
			items = append(items, triageItem{
				kind:        tuiItemAgent,
				sid:         sess.ID,
				eid:         esc.AgentID,
				sessionName: sess.Name,
				label:       label,
				detail:      "escalation: " + truncStr(esc.Reason, 40),
				age:         ageStr(esc.Ts),
				severity:    3,
			})
		}
		// Stuck agents (severity 3)
		for _, a := range st.Agents {
			if a.Status == "stuck" {
				items = append(items, triageItem{
					kind:        tuiItemAgent,
					sid:         sess.ID,
					eid:         a.ID,
					sessionName: sess.Name,
					label:       a.Name,
					detail:      "stuck",
					age:         ageStr(a.StatusChangedAt),
					severity:    3,
				})
			}
		}
		// Tasks by stage
		for _, t := range st.Tasks {
			var sev int
			switch t.Stage {
			case "blocked", "needs_human":
				sev = 2
			case "needs_review":
				sev = 1
			case "failed", "timed_out":
				sev = 2
			default:
				continue
			}
			detail := t.Stage
			if t.BlockedReason != nil && *t.BlockedReason != "" {
				detail += ": " + truncStr(*t.BlockedReason, 40)
			}
			items = append(items, triageItem{
				kind:        tuiItemTask,
				sid:         sess.ID,
				eid:         t.ID,
				sessionName: sess.Name,
				label:       t.Title,
				detail:      detail,
				age:         ageStr(t.StageChangedAt),
				severity:    sev,
			})
		}
	}
	// Sort: severity DESC, then oldest age first (empty age sorts last)
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			a, b := items[j-1], items[j]
			if a.severity < b.severity {
				items[j-1], items[j] = b, a
			} else if a.severity == b.severity && a.age == "" && b.age != "" {
				items[j-1], items[j] = b, a
			}
		}
	}
	return items
}

func (m tuiModel) updateTriageView(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	items := buildTriageItems(m)
	n := len(items)
	switch msg.String() {
	case "q", "esc", "T":
		m.triageView = false
	case "j", "down":
		if m.triageCursor < n-1 {
			m.triageCursor++
		}
	case "k", "up":
		if m.triageCursor > 0 {
			m.triageCursor--
		}
	case "enter":
		if m.triageCursor < n {
			it := items[m.triageCursor]
			m.triageView = false
			m.navigateTo(it.kind, it.sid, it.eid)
		}
	}
	return m, nil
}

func (m tuiModel) viewTriageScreen() string {
	var sb strings.Builder
	items := buildTriageItems(m)

	// Header
	hdr := fmt.Sprintf("  Triage — %d item(s) need attention   j/k:move  Enter:jump  T/Esc:close", len(items))
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorText).Width(m.w).Render(hdr) + "\n")
	sb.WriteString(dimStyle.Render(strings.Repeat("─", m.w)) + "\n")

	if len(items) == 0 {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorGreen).Render("  ✓ Nothing needs attention") + "\n")
		return sb.String()
	}

	severityLabel := map[int]string{3: "!!!", 2: "! ", 1: "  "}
	severityColor := map[int]lipgloss.Color{3: colorRed, 2: colorOrange, 1: colorYellow}

	sessW := 16
	ageW := 7
	sevW := 3
	labelW := m.w - sessW - ageW - sevW - 24
	if labelW < 10 {
		labelW = 10
	}

	for i, it := range items {
		sel := i == m.triageCursor
		sev := severityLabel[it.severity]
		sevC := severityColor[it.severity]
		ageStr := it.age
		if ageStr == "" {
			ageStr = "—"
		}
		row := fmt.Sprintf("  %s  %-*s  %-*s  %-*s  %s",
			lipgloss.NewStyle().Foreground(sevC).Bold(true).Render(sev),
			sessW, truncStr(it.sessionName, sessW),
			ageW, ageStr,
			labelW, truncStr(it.label, labelW),
			dimStyle.Render(truncStr(it.detail, m.w-sessW-ageW-labelW-20)),
		)
		base := lipgloss.NewStyle()
		if sel {
			base = base.Background(colorSubtle).Foreground(colorText)
		}
		sb.WriteString(base.Width(m.w).Render(row) + "\n")
	}
	sb.WriteString(dimStyle.Render(strings.Repeat("─", m.w)) + "\n")
	return sb.String()
}
