package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── Control Tower ────────────────────────────────────────────────────────────
//
// The Control Tower is a pinned meta-entry at the top of the sidebar. It cannot
// be deleted. Pressing Enter opens the fleet dashboard view; Esc/q closes it.

// updateControlTower handles keys when the fleet dashboard is open.
func (m tuiModel) updateControlTower(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		m.ctView = false
	}
	return m, nil
}

// viewControlTower renders the full-screen fleet operations dashboard.
func (m tuiModel) viewControlTower() string {
	w := m.w
	contentW := min(76, w-4)

	var sb strings.Builder
	sb.WriteString(m.viewHUD() + "\n")

	// ── Title ──────────────────────────────────────────────────────────────────
	sb.WriteString("\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("⊕ Control Tower") +
		"  " + dimStyle.Render("Fleet Operations Dashboard") + "\n\n")

	// ── Fleet mode ─────────────────────────────────────────────────────────────
	mode := strings.ToUpper(m.statusBar.fleetMode)
	if mode == "" {
		mode = "NORMAL"
	}
	var modeBadge string
	switch m.statusBar.fleetMode {
	case "contain":
		modeBadge = lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render("● " + mode)
	case "stabilize":
		modeBadge = lipgloss.NewStyle().Foreground(colorOrange).Bold(true).Render("● " + mode)
	default:
		modeBadge = lipgloss.NewStyle().Foreground(colorGreen).Render("● " + mode)
	}
	sb.WriteString("  Fleet mode: " + modeBadge + "\n\n")

	// ── Aggregate stats ────────────────────────────────────────────────────────
	var (
		totalAgents, liveAgents, stuckAgents, waitingAgents int
		totalTasks, blockedTasks, completedTasks            int
		totalTokens                                         int64
		totalCostUSD                                        float64
	)
	for _, sess := range m.sessions {
		st, ok := m.states[sess.ID]
		if !ok {
			continue
		}
		for _, a := range st.Agents {
			totalAgents++
			if a.TmuxSession != nil {
				liveAgents++
			}
			switch a.Status {
			case "stuck":
				stuckAgents++
			case "waiting":
				waitingAgents++
			}
			totalTokens += a.TokensUsed
			totalCostUSD += estimateCostUSD(a.ModelName, a.TokensUsed)
		}
		for _, t := range st.Tasks {
			totalTasks++
			switch t.Stage {
			case "blocked", "needs_human", "failed", "timed_out":
				blockedTasks++
			case "done", "complete":
				completedTasks++
			}
		}
	}

	kv := func(label, value string, c lipgloss.Color) string {
		return "  " + dimStyle.Render(fmt.Sprintf("%-10s", label+":")) + "  " +
			lipgloss.NewStyle().Foreground(c).Render(value)
	}
	sb.WriteString(kv("Sessions", fmt.Sprintf("%d", len(m.sessions)), colorText) + "\n")
	sb.WriteString(kv("Agents", fmt.Sprintf("%d total  %d live  %d idle",
		totalAgents, liveAgents, totalAgents-liveAgents), colorGreen) + "\n")
	if stuckAgents > 0 {
		sb.WriteString(kv("Stuck", fmt.Sprintf("%d", stuckAgents), colorRed) + "\n")
	}
	if waitingAgents > 0 {
		sb.WriteString(kv("Waiting", fmt.Sprintf("%d", waitingAgents), colorOrange) + "\n")
	}
	sb.WriteString(kv("Tasks", fmt.Sprintf("%d total  %d done", totalTasks, completedTasks), colorText) + "\n")
	if blockedTasks > 0 {
		sb.WriteString(kv("Blocked", fmt.Sprintf("%d", blockedTasks), colorRed) + "\n")
	}
	sb.WriteString(kv("Spend", fmt.Sprintf("%s  (%s tokens)",
		formatCostUSD(totalCostUSD), formatTokensK(totalTokens)), colorDim) + "\n")

	// ── Per-session table ──────────────────────────────────────────────────────
	if len(m.sessions) > 0 {
		sb.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("Sessions") + "\n")
		sb.WriteString("  " + dimStyle.Render(strings.Repeat("─", contentW)) + "\n")
		for _, sess := range m.sessions {
			st, ok := m.states[sess.ID]
			if !ok {
				continue
			}
			live := 0
			for _, a := range st.Agents {
				if a.TmuxSession != nil {
					live++
				}
			}
			blocked, escalations, stuck := sessionExceptions(st)
			name := truncStr(sess.Name, 28)
			row := fmt.Sprintf("  %-28s  agents %d/%d  tasks %d",
				name, live, len(st.Agents), len(st.Tasks))
			var badges []string
			if stuck > 0 {
				badges = append(badges, lipgloss.NewStyle().Foreground(colorRed).Render(fmt.Sprintf("%d stuck", stuck)))
			}
			if blocked > 0 {
				badges = append(badges, lipgloss.NewStyle().Foreground(colorRed).Render(fmt.Sprintf("%d blocked", blocked)))
			}
			if escalations > 0 {
				badges = append(badges, lipgloss.NewStyle().Foreground(colorOrange).Render(fmt.Sprintf("%d esc", escalations)))
			}
			if sess.AutopilotEnabled {
				badges = append(badges, lipgloss.NewStyle().Foreground(colorTeal).Render("auto"))
			}
			line := dimStyle.Render(row)
			if len(badges) > 0 {
				line += "  " + strings.Join(badges, " ")
			}
			sb.WriteString(line + "\n")
		}
	}

	// ── Fleet controls quick-ref ───────────────────────────────────────────────
	sb.WriteString("\n  " + dimStyle.Render("Fleet controls:") + "\n")
	sb.WriteString("  " + dimStyle.Render("  o  Ops Console    ctrl+x  Emergency HALT    :  Command Palette") + "\n")
	sb.WriteString("\n  " + dimStyle.Render("Esc · q  return"))

	return sb.String()
}
