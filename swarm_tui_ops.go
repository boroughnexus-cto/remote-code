package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── Ops Console ─────────────────────────────────────────────────────────────
//
// The Ops Console is a full-screen overlay opened with 'o' from the sidebar.
// It shows a fleet health matrix, per-agent detail, and quick-action buttons
// for fleet mode changes (CONTAIN / STABILIZE / RESUME / NORMAL).
//
// Key bindings while opsView == true:
//   c       – CONTAIN  (suspend spawning + block external writes)
//   s       – STABILIZE (suspend spawning, allow existing work to drain)
//   r       – RESUME   (NORMAL, re-enable spawning)
//   ↑/↓     – scroll agent table
//   esc/q   – close
//
// ctrl+x is wired globally (in updateSidebar) and also here.

// opsFleetActionMsg is returned by the fleet-mode POST command.
type opsFleetActionMsg struct {
	mode string
	err  error
}

// opsAgentRow holds a computed row for the agent health table.
type opsAgentRow struct {
	agentID    string
	name       string
	role       string
	status     string
	contextPct float64
	tokensUsed int64
	model      string
	lastEvent  time.Time
	spend      float64
	sid        string // parent session ID
}

// ─── HTTP commands ────────────────────────────────────────────────────────────

func postFleetMode(mode, setBy string) tea.Cmd {
	return func() tea.Msg {
		body, _ := json.Marshal(map[string]string{"mode": mode, "set_by": setBy})
		resp, err := http.Post(
			"http://localhost:8080/api/swarm/fleet/mode",
			"application/json",
			strings.NewReader(string(body)),
		)
		if err != nil {
			return opsFleetActionMsg{err: err}
		}
		resp.Body.Close()
		return opsFleetActionMsg{mode: mode}
	}
}

func postFleetHalt(setBy string) tea.Cmd {
	return func() tea.Msg {
		body, _ := json.Marshal(map[string]string{"set_by": setBy})
		resp, err := http.Post(
			"http://localhost:8080/api/swarm/fleet/halt",
			"application/json",
			strings.NewReader(string(body)),
		)
		if err != nil {
			return opsFleetActionMsg{err: err}
		}
		resp.Body.Close()
		return opsFleetActionMsg{mode: "contain"}
	}
}

// ─── Update handler ───────────────────────────────────────────────────────────

func (m tuiModel) updateOpsView(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	var cmds []tea.Cmd
	switch msg.String() {
	case "esc", "q", "o":
		m.opsView = false

	case "c":
		cmds = append(cmds, postFleetMode("contain", "user"))
		m.setFlash("⚠ CONTAIN mode — spawning suspended, writes blocked", false)

	case "s":
		cmds = append(cmds, postFleetMode("stabilize", "user"))
		m.setFlash("STABILIZE mode — spawning suspended, existing work draining", false)

	case "r", "n":
		cmds = append(cmds, postFleetMode("normal", "user"))
		m.setFlash("NORMAL mode — fleet resumed", false)

	case "ctrl+x":
		cmds = append(cmds, postFleetHalt("user"))
		m.opsView = false
		m.setFlash("⚠ HALT — fleet in CONTAIN mode", true)

	case "up", "k":
		if m.opsCursor > 0 {
			m.opsCursor--
		}

	case "down", "j":
		rows := m.buildOpsRows()
		if m.opsCursor < len(rows)-1 {
			m.opsCursor++
		}
	}
	return m, cmds
}

// ─── View ─────────────────────────────────────────────────────────────────────

func (m tuiModel) viewOpsConsole() string {
	rows := m.buildOpsRows()
	w := m.w
	if w < 60 {
		w = 60
	}

	var sb strings.Builder

	// Title
	title := lipgloss.NewStyle().
		Foreground(colorTeal).Bold(true).
		Render("⚡ Ops Console")
	mode := strings.ToUpper(m.statusBar.fleetMode)
	if mode == "" {
		mode = "NORMAL"
	}
	modeStyle := lipgloss.NewStyle().Bold(true)
	switch m.statusBar.fleetMode {
	case "contain":
		modeStyle = modeStyle.Foreground(colorRed)
	case "stabilize":
		modeStyle = modeStyle.Foreground(colorOrange)
	default:
		modeStyle = modeStyle.Foreground(colorDim)
	}
	modeLabel := modeStyle.Render("Fleet: " + mode)
	sb.WriteString(title + "   " + modeLabel + "\n\n")

	// ── Fleet mode actions ─────────────────────────────────────────────────────
	sb.WriteString(dimStyle.Render("Fleet Mode:") + "\n")
	actions := []struct {
		key   string
		label string
		mode  string
		color lipgloss.Color
	}{
		{"c", "CONTAIN (halt spawning + block writes)", "contain", colorRed},
		{"s", "STABILIZE (halt spawning, drain safely)", "stabilize", colorOrange},
		{"r", "RESUME / NORMAL", "normal", colorGreen},
	}
	for _, a := range actions {
		keyStr := lipgloss.NewStyle().Foreground(colorTeal).Render("[" + a.key + "]")
		active := m.statusBar.fleetMode == a.mode
		labelStyle := lipgloss.NewStyle().Foreground(a.color)
		if active {
			labelStyle = labelStyle.Bold(true)
		} else {
			labelStyle = lipgloss.NewStyle().Foreground(colorText)
		}
		bullet := "  "
		if active {
			bullet = lipgloss.NewStyle().Foreground(colorTeal).Render("▶ ")
		}
		sb.WriteString(bullet + keyStr + " " + labelStyle.Render(a.label) + "\n")
	}
	haltLine := lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render("[ctrl+x]") +
		"  " + lipgloss.NewStyle().Foreground(colorRed).Render("HALT immediately (CONTAIN from anywhere)")
	sb.WriteString("  " + haltLine + "\n")

	// ── Summary stats ──────────────────────────────────────────────────────────
	sb.WriteString("\n")
	activeN, waitingN, stuckN := 0, 0, 0
	totalSpend := 0.0
	for _, r := range rows {
		switch r.status {
		case "working", "active", "coding", "testing", "thinking":
			activeN++
		case "stuck":
			stuckN++
		case "waiting":
			waitingN++
		}
		totalSpend += r.spend
	}
	statsLine := fmt.Sprintf("%d agents   %d active   %d waiting   %d stuck   %s total spend",
		len(rows), activeN, waitingN, stuckN, formatSpendUSD(totalSpend))
	sb.WriteString(dimStyle.Render(statsLine) + "\n\n")

	// ── Agent health table ─────────────────────────────────────────────────────
	if len(rows) == 0 {
		sb.WriteString(dimStyle.Render("  No agents running.") + "\n")
	} else {
		hdr := fmt.Sprintf("  %-20s  %-12s  %-10s  %6s  %7s  %-8s",
			"Agent", "Role", "Status", "Ctx%", "Spend", "Last")
		sb.WriteString(dimStyle.Render(hdr) + "\n")
		sb.WriteString(dimStyle.Render("  " + strings.Repeat("─", min(w-4, 72))) + "\n")

		maxRows := m.h - 24 // leave room for header, actions, input hint
		if maxRows < 3 {
			maxRows = 3
		}
		start := 0
		if m.opsCursor >= maxRows {
			start = m.opsCursor - maxRows + 1
		}
		end := start + maxRows
		if end > len(rows) {
			end = len(rows)
		}

		for i := start; i < end; i++ {
			r := rows[i]
			cursor := "  "
			if i == m.opsCursor {
				cursor = lipgloss.NewStyle().Foreground(colorTeal).Render("▶ ")
			}

			statusStyle := lipgloss.NewStyle().Foreground(colorText)
			switch r.status {
			case "working", "active", "coding", "testing", "thinking":
				statusStyle = lipgloss.NewStyle().Foreground(colorGreen)
			case "stuck":
				statusStyle = lipgloss.NewStyle().Foreground(colorRed).Bold(true)
			case "waiting":
				statusStyle = lipgloss.NewStyle().Foreground(colorOrange)
			case "idle":
				statusStyle = lipgloss.NewStyle().Foreground(colorDim)
			}

			ctxColor := colorGreen
			if r.contextPct >= 85 {
				ctxColor = colorRed
			} else if r.contextPct >= 70 {
				ctxColor = colorOrange
			}

			var lastStr string
			if r.lastEvent.IsZero() {
				lastStr = "—"
			} else {
				age := time.Since(r.lastEvent)
				switch {
				case age < 2*time.Minute:
					lastStr = fmt.Sprintf("%ds", int(age.Seconds()))
				case age < time.Hour:
					lastStr = fmt.Sprintf("%dm", int(age.Minutes()))
				default:
					lastStr = fmt.Sprintf("%dh", int(age.Hours()))
				}
			}

			line := fmt.Sprintf("%-20s  %-12s  %s  %s  %s  %s",
				truncStr(r.name, 20),
				truncStr(r.role, 12),
				statusStyle.Render(fmt.Sprintf("%-10s", truncStr(r.status, 10))),
				lipgloss.NewStyle().Foreground(ctxColor).Render(fmt.Sprintf("%5.0f%%", r.contextPct)),
				lipgloss.NewStyle().Foreground(colorDim).Render(fmt.Sprintf("%7s", formatSpendUSD(r.spend))),
				dimStyle.Render(fmt.Sprintf("%-8s", lastStr)),
			)
			sb.WriteString(cursor + line + "\n")
		}

		if len(rows) > maxRows {
			sb.WriteString(dimStyle.Render(fmt.Sprintf("\n  %d agents total — ↑/↓ to scroll", len(rows))) + "\n")
		}
	}

	// ── Footer ─────────────────────────────────────────────────────────────────
	sb.WriteString("\n" + dimStyle.Render("c contain  s stabilize  r resume  ctrl+x halt  esc close"))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorTeal).
		Padding(1, 2).
		Width(min(w-2, 90)).
		Render(sb.String())
}

// buildOpsRows collects all agents across all sessions, sorted by status severity.
func (m tuiModel) buildOpsRows() []opsAgentRow {
	var rows []opsAgentRow
	for sid, state := range m.states {
		for _, a := range state.Agents {
			var lastEvent time.Time
			// Find last event for this agent
			for _, e := range state.Events {
				if e.AgentID == a.ID {
					t := time.Unix(e.Ts, 0)
					if t.After(lastEvent) {
						lastEvent = t
					}
				}
			}
			rows = append(rows, opsAgentRow{
				agentID:    a.ID,
				name:       a.Name,
				role:       a.Role,
				status:     a.Status,
				contextPct: a.ContextPct,
				tokensUsed: a.TokensUsed,
				model:      a.ModelName,
				lastEvent:  lastEvent,
				spend:      estimateCostUSD(a.ModelName, a.TokensUsed),
				sid:        sid,
			})
		}
	}
	// Sort: stuck first, then working, then waiting, then idle, then alpha
	statusPriority := map[string]int{
		"stuck": 0, "working": 1, "active": 1, "coding": 1, "testing": 1,
		"thinking": 1, "waiting": 2,
	}
	sort.Slice(rows, func(i, j int) bool {
		pi := statusPriority[rows[i].status]
		pj := statusPriority[rows[j].status]
		if pi != pj {
			return pi < pj
		}
		return rows[i].name < rows[j].name
	})
	return rows
}
