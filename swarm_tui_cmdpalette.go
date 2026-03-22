package main

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── Command Palette ─────────────────────────────────────────────────────────
//
// The command palette opens with ':' from the sidebar and gives fast fuzzy
// access to all TUI actions and navigation targets.
//
// Key bindings:
//   type      – filter commands
//   ↑/↓       – navigate results
//   Enter     – execute selected command
//   esc       – close without executing

// cmdEntry describes a single palette command.
type cmdEntry struct {
	label    string               // displayed in palette
	keywords []string             // extra match terms (lowercased)
	action   func(m tuiModel) (tuiModel, []tea.Cmd)
}

// globalCommands is the static command registry.
// Commands that require context (e.g. selected session) grab it from the model
// inside the action func.
var globalCommands = []cmdEntry{
	// ── Navigation ────────────────────────────────────────────────────────────
	{label: "Ops Console", keywords: []string{"ops", "fleet", "halt", "contain"}, action: func(m tuiModel) (tuiModel, []tea.Cmd) {
		m.opsView = true
		m.cmdPalette = nil
		return m, nil
	}},
	{label: "Icinga Monitoring", keywords: []string{"icinga", "monitor", "alerts", "problems"}, action: func(m tuiModel) (tuiModel, []tea.Cmd) {
		m.icingaView = true
		m.icingaTopCur = 0
		m.icingaBotCur = 0
		m.icingaFocus = 0
		m.cmdPalette = nil
		return m, []tea.Cmd{m.client.get("icinga", "/api/icinga/services")}
	}},
	{label: "Event Log", keywords: []string{"events", "log", "history"}, action: func(m tuiModel) (tuiModel, []tea.Cmd) {
		sid := m.selSessionID()
		if sid != "" {
			evts := m.vpRawEvents[sid]
			m.evtLogView = true
			m.evtDetailView = nil
			m.evtCursor = max(0, len(evts)-1)
		}
		m.cmdPalette = nil
		return m, nil
	}},
	{label: "Goals View", keywords: []string{"goals", "objectives"}, action: func(m tuiModel) (tuiModel, []tea.Cmd) {
		m.goalView = true
		m.goalCursor = 0
		m.cmdPalette = nil
		return m, nil
	}},
	{label: "Triage View", keywords: []string{"triage", "issues", "stale", "prs"}, action: func(m tuiModel) (tuiModel, []tea.Cmd) {
		m.triageView = true
		m.triageCursor = 0
		m.cmdPalette = nil
		return m, nil
	}},
	{label: "Work Queue", keywords: []string{"work", "queue", "plane", "backlog"}, action: func(m tuiModel) (tuiModel, []tea.Cmd) {
		sid := m.selSessionID()
		if sid != "" {
			m.workQueueView = true
			m.workQueueSID = sid
			m.workQueueCursor = 0
			m.cmdPalette = nil
			return m, []tea.Cmd{m.client.get("workqueue", "/api/swarm/sessions/"+sid+"/plane/issues?state_group=backlog,unstarted")}
		}
		m.cmdPalette = nil
		return m, nil
	}},

	// ── Fleet actions ─────────────────────────────────────────────────────────
	{label: "Fleet: CONTAIN (halt spawning + writes)", keywords: []string{"contain", "halt", "stop"}, action: func(m tuiModel) (tuiModel, []tea.Cmd) {
		m.cmdPalette = nil
		m.setFlash("⚠ CONTAIN mode — spawning suspended, writes blocked", false)
		return m, []tea.Cmd{postFleetMode("contain", "user")}
	}},
	{label: "Fleet: STABILIZE (halt spawning, drain safely)", keywords: []string{"stabilize", "drain", "pause"}, action: func(m tuiModel) (tuiModel, []tea.Cmd) {
		m.cmdPalette = nil
		m.setFlash("STABILIZE mode — spawning suspended, existing work draining", false)
		return m, []tea.Cmd{postFleetMode("stabilize", "user")}
	}},
	{label: "Fleet: RESUME (restore normal operation)", keywords: []string{"resume", "normal", "start"}, action: func(m tuiModel) (tuiModel, []tea.Cmd) {
		m.cmdPalette = nil
		m.setFlash("NORMAL mode — fleet resumed", false)
		return m, []tea.Cmd{postFleetMode("normal", "user")}
	}},

	// ── Session actions ───────────────────────────────────────────────────────
	{label: "New Session…", keywords: []string{"create", "session", "new"}, action: func(m tuiModel) (tuiModel, []tea.Cmd) {
		m.cmdPalette = nil
		m.modal = newTUIModal(tuiModalNewSession, "")
		m.focus = tuiFocusModal
		return m, nil
	}},
	{label: "New Agent…", keywords: []string{"create", "agent", "spawn", "new"}, action: func(m tuiModel) (tuiModel, []tea.Cmd) {
		sid := m.selSessionID()
		m.cmdPalette = nil
		m.modal = newTUIModal(tuiModalNewAgent, sid)
		m.focus = tuiFocusModal
		return m, nil
	}},
	{label: "New Task…", keywords: []string{"create", "task", "new"}, action: func(m tuiModel) (tuiModel, []tea.Cmd) {
		sid := m.selSessionID()
		m.cmdPalette = nil
		m.modal = newTUIModal(tuiModalNewTask, sid)
		m.focus = tuiFocusModal
		return m, nil
	}},
	{label: "Quick Spawn Worker…", keywords: []string{"quick", "worker", "spawn"}, action: func(m tuiModel) (tuiModel, []tea.Cmd) {
		sid := m.selSessionID()
		m.cmdPalette = nil
		m.modal = newTUIModal(tuiModalQuickAgent, sid)
		m.focus = tuiFocusModal
		return m, nil
	}},

	// ── Misc ──────────────────────────────────────────────────────────────────
	{label: "Refresh Data", keywords: []string{"refresh", "reload", "sync"}, action: func(m tuiModel) (tuiModel, []tea.Cmd) {
		m.cmdPalette = nil
		return m, []tea.Cmd{m.client.fetchAll()}
	}},
	{label: "Submit Feedback…", keywords: []string{"feedback", "bug", "report"}, action: func(m tuiModel) (tuiModel, []tea.Cmd) {
		fc := captureFeedbackState(&m)
		m.feedbackCapture = &fc
		m.feedbackSubmitting = false
		m.cmdPalette = nil
		m.modal = newTUIModal(tuiModalFeedback, m.selSessionID())
		m.focus = tuiFocusModal
		return m, nil
	}},
}

// ─── Model ────────────────────────────────────────────────────────────────────

type cmdPaletteModel struct {
	input    textinput.Model
	filtered []int  // indices into globalCommands
	cursor   int
}

func newCmdPaletteModel() *cmdPaletteModel {
	ti := textinput.New()
	ti.Placeholder = "type to filter commands…"
	ti.CharLimit = 80
	ti.Focus()
	cp := &cmdPaletteModel{input: ti}
	cp.refilter()
	return cp
}

// refilter updates filtered based on current input.
func (cp *cmdPaletteModel) refilter() {
	q := strings.ToLower(strings.TrimSpace(cp.input.Value()))
	cp.filtered = cp.filtered[:0]
	for i, c := range globalCommands {
		if q == "" || matchesCmd(c, q) {
			cp.filtered = append(cp.filtered, i)
		}
	}
	if cp.cursor >= len(cp.filtered) {
		cp.cursor = max(0, len(cp.filtered)-1)
	}
}

func matchesCmd(c cmdEntry, q string) bool {
	if strings.Contains(strings.ToLower(c.label), q) {
		return true
	}
	for _, kw := range c.keywords {
		if strings.Contains(kw, q) {
			return true
		}
	}
	return false
}

// ─── Update ───────────────────────────────────────────────────────────────────

func (m tuiModel) updateCmdPalette(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	cp := m.cmdPalette
	if cp == nil {
		return m, nil
	}
	var cmds []tea.Cmd

	switch msg.String() {
	case "esc", "ctrl+c":
		m.cmdPalette = nil
		return m, nil

	case "enter":
		if len(cp.filtered) > 0 {
			idx := cp.filtered[cp.cursor]
			newM, actionCmds := globalCommands[idx].action(m)
			return newM, actionCmds
		}
		m.cmdPalette = nil
		return m, nil

	case "up", "k":
		if cp.cursor > 0 {
			cp.cursor--
		}

	case "down", "j":
		if cp.cursor < len(cp.filtered)-1 {
			cp.cursor++
		}

	default:
		var cmd tea.Cmd
		cp.input, cmd = cp.input.Update(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		cp.refilter()
	}

	return m, cmds
}

// ─── View ─────────────────────────────────────────────────────────────────────

func (m tuiModel) viewCmdPalette() string {
	cp := m.cmdPalette
	if cp == nil {
		return ""
	}
	w := m.w
	boxW := min(60, w-4)

	var sb strings.Builder
	sb.WriteString(lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render(": Command Palette") + "\n\n")
	sb.WriteString(cp.input.View() + "\n\n")

	if len(cp.filtered) == 0 {
		sb.WriteString(dimStyle.Render("  No matching commands") + "\n")
	} else {
		maxShow := min(12, len(cp.filtered))
		start := 0
		if cp.cursor >= maxShow {
			start = cp.cursor - maxShow + 1
		}
		end := start + maxShow
		if end > len(cp.filtered) {
			end = len(cp.filtered)
		}
		for i := start; i < end; i++ {
			idx := cp.filtered[i]
			cmd := globalCommands[idx]
			cursor := "  "
			labelStyle := lipgloss.NewStyle().Foreground(colorText)
			if i == cp.cursor {
				cursor = lipgloss.NewStyle().Foreground(colorTeal).Render("▶ ")
				labelStyle = lipgloss.NewStyle().Foreground(colorTeal)
			}
			sb.WriteString(cursor + labelStyle.Render(cmd.label) + "\n")
		}
		if len(cp.filtered) > maxShow {
			sb.WriteString(dimStyle.Render(
				"\n  "+string(rune('0'+len(cp.filtered)-maxShow))+" more — type to narrow") + "\n")
		}
	}

	sb.WriteString("\n" + dimStyle.Render("↑/↓ navigate  Enter execute  Esc close"))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorTeal).
		Padding(1, 2).
		Width(boxW).
		Render(sb.String())

	padTop := (m.h - lipgloss.Height(box)) / 3 // bias toward top third
	padLeft := (m.w - lipgloss.Width(box)) / 2
	if padTop < 0 {
		padTop = 0
	}
	if padLeft < 0 {
		padLeft = 0
	}
	top := strings.Repeat("\n", padTop)
	indent := strings.Repeat(" ", padLeft)
	rows := strings.Split(box, "\n")
	for i, r := range rows {
		rows[i] = indent + r
	}
	return top + strings.Join(rows, "\n")
}
