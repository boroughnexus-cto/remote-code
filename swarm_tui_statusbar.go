package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// tuiStatusBar holds the data for the always-visible bottom fleet status line.
// It is updated from WS state updates and async health checks.
type tuiStatusBar struct {
	// Fleet health
	activeAgents  int
	waitingAgents int // agents with unresolved escalations
	stuckAgents   int // agents in "stuck" status

	// Spend
	sessionSpend float64
	costLimit    float64 // 0 = no limit configured

	// Integrations (simple OK/fail booleans)
	planeOK    bool
	planeReady bool // false = never checked yet
	icingaOK   bool
	icingaReady bool

	// Fleet mode (from globalFleetState or tuiFleetModeMsg)
	fleetMode string // "normal", "contain", "stabilize"
}

// buildStatusBar derives a tuiStatusBar from the model's current state.
func buildStatusBar(m *tuiModel) tuiStatusBar {
	sb := m.statusBar // carry over integration readiness/mode from previous state

	// Default fleet mode if unset
	if sb.fleetMode == "" {
		sb.fleetMode = "normal"
	}

	// Reset counters — they'll be recomputed from WS data
	sb.activeAgents = 0
	sb.waitingAgents = 0
	sb.stuckAgents = 0
	sb.sessionSpend = 0

	for _, state := range m.states {
		for _, a := range state.Agents {
			switch a.Status {
			case "working", "active", "coding", "testing", "thinking":
				sb.activeAgents++
			case "stuck":
				sb.stuckAgents++
			case "waiting":
				sb.waitingAgents++
			}
			// Accumulate spend using the existing estimateCostUSD helper
			sb.sessionSpend += estimateCostUSD(a.ModelName, a.TokensUsed)
		}
		// Count unresolved escalations as waiting agents (additive)
		sb.waitingAgents += len(state.Escalations)
	}

	return sb
}

// viewFleetStatusBar renders the always-visible one-line fleet status bar.
//
// Format (full width):
//
//	 3 active · 1 waiting  │  $1.23 spent  │  Plane ✓  Icinga ✓  │  NORMAL
func viewFleetStatusBar(sb tuiStatusBar, width int) string {
	if width < 40 {
		// Minimal fallback
		mode := strings.ToUpper(sb.fleetMode)
		if mode == "" {
			mode = "NORMAL"
		}
		return lipgloss.NewStyle().
			Background(colorSubtle).
			Foreground(colorDim).
			Width(width).
			Render(" " + mode)
	}

	barBg := lipgloss.Color("#060d1a")
	sepStyle := lipgloss.NewStyle().Foreground(colorDim).Background(barBg)
	sep := sepStyle.Render("  │  ")

	// ── Fleet health segment ──────────────────────────────────────────────────
	var fleetParts []string

	activeStr := fmt.Sprintf("%d active", sb.activeAgents)
	fleetParts = append(fleetParts,
		lipgloss.NewStyle().Foreground(colorGreen).Background(barBg).Render(activeStr))

	if sb.waitingAgents > 0 {
		waitStr := fmt.Sprintf("· %d waiting", sb.waitingAgents)
		fleetParts = append(fleetParts,
			lipgloss.NewStyle().Foreground(colorOrange).Background(barBg).Render(waitStr))
	}

	if sb.stuckAgents > 0 {
		stuckStr := fmt.Sprintf("· %d stuck", sb.stuckAgents)
		fleetParts = append(fleetParts,
			lipgloss.NewStyle().Foreground(colorRed).Bold(true).Background(barBg).Render(stuckStr))
	}

	fleetSeg := strings.Join(fleetParts, " ")

	// ── Budget segment ────────────────────────────────────────────────────────
	var budgetSeg string
	if sb.costLimit <= 0 {
		costStr := formatSpendUSD(sb.sessionSpend)
		budgetSeg = lipgloss.NewStyle().Foreground(colorDim).Background(barBg).
			Render(costStr + " spent")
	} else {
		pct := sb.sessionSpend / sb.costLimit
		if pct > 1.0 {
			pct = 1.0
		}
		bar := miniProgressBar(pct, 4)
		pctStr := fmt.Sprintf("%.0f%%", pct*100)
		limitStr := fmt.Sprintf("%s / %s %s %s",
			formatSpendUSD(sb.sessionSpend),
			formatSpendUSD(sb.costLimit),
			bar,
			pctStr,
		)
		c := colorGreen
		if pct >= 0.9 {
			c = colorRed
		} else if pct >= 0.7 {
			c = colorYellow
		}
		budgetSeg = lipgloss.NewStyle().Foreground(c).Background(barBg).Render(limitStr)
	}

	// ── Integration segment ───────────────────────────────────────────────────
	planeIndicator := integrationIndicator("Plane", sb.planeOK, sb.planeReady)
	icingaIndicator := integrationIndicator("Icinga", sb.icingaOK, sb.icingaReady)
	integSeg := planeIndicator + "  " + icingaIndicator

	// ── Fleet mode segment ────────────────────────────────────────────────────
	mode := strings.ToUpper(sb.fleetMode)
	if mode == "" {
		mode = "NORMAL"
	}
	var modeSeg string
	switch sb.fleetMode {
	case "contain":
		modeSeg = lipgloss.NewStyle().
			Foreground(colorRed).Bold(true).Background(barBg).Render(mode)
	case "stabilize":
		modeSeg = lipgloss.NewStyle().
			Foreground(colorOrange).Bold(true).Background(barBg).Render(mode)
	default:
		modeSeg = lipgloss.NewStyle().
			Foreground(colorDim).Background(barBg).Render(mode)
	}

	// ── Assemble full bar ─────────────────────────────────────────────────────
	content := " " + fleetSeg + sep + budgetSeg + sep + integSeg + sep + modeSeg + " "

	return lipgloss.NewStyle().
		Background(barBg).
		Foreground(colorDim).
		Width(width).
		Render(content)
}

// integrationIndicator returns a coloured name + check/cross/question symbol.
func integrationIndicator(name string, ok, ready bool) string {
	barBg := lipgloss.Color("#060d1a")
	if !ready {
		return lipgloss.NewStyle().Foreground(colorDim).Background(barBg).
			Render(name + " ?")
	}
	if ok {
		return lipgloss.NewStyle().Foreground(colorGreen).Background(barBg).
			Render(name + " ✓")
	}
	return lipgloss.NewStyle().Foreground(colorRed).Background(barBg).
		Render(name + " ✗")
}

// miniProgressBar renders a 4-char wide ASCII mini progress bar.
// filled = '█', empty = '░'.
func miniProgressBar(pct float64, width int) string {
	if width <= 0 {
		width = 4
	}
	filled := int(pct * float64(width))
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// formatSpendUSD formats a spend value without the "~" prefix used for estimates.
func formatSpendUSD(c float64) string {
	if c < 0.005 {
		return "$0.00"
	}
	if c < 1.0 {
		return fmt.Sprintf("$%.2f", c)
	}
	return fmt.Sprintf("$%.2f", c)
}
