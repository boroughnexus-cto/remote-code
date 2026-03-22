package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ─── Styles ───────────────────────────────────────────────────────────────────

var (
	colorTeal    = lipgloss.Color("#14b8a6")
	colorMagenta = lipgloss.Color("#ec4899")
	colorPurple  = lipgloss.Color("#a855f7")
	colorOrange  = lipgloss.Color("#f97316")
	colorBlue    = lipgloss.Color("#3b82f6")
	colorYellow  = lipgloss.Color("#eab308")
	colorGreen   = lipgloss.Color("#4ade80")
	colorRed     = lipgloss.Color("#ef4444")
	colorDim     = lipgloss.Color("#334155")
	colorSubtle  = lipgloss.Color("#1e293b")
	colorText    = lipgloss.Color("#e2e8f0")

	hudStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#060d1a")).
			Foreground(colorText).
			BorderBottom(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(colorDim).
			PaddingLeft(2).PaddingRight(2)

	titleStyle = lipgloss.NewStyle().Foreground(colorTeal).Bold(true)
	dimStyle   = lipgloss.NewStyle().Foreground(colorDim)
)

func tuiRoleConfig(role string) (string, lipgloss.Color) {
	switch role {
	case "orchestrator":
		return "🧠", colorMagenta
	case "senior-dev":
		return "🧑‍💻", colorTeal
	case "qa-agent":
		return "🔬", colorYellow
	case "devops-agent":
		return "⚙", colorBlue
	case "researcher":
		return "📚", colorPurple
	case "reviewer":
		return "🔍", colorOrange
	default:
		return "👷", colorDim
	}
}

func tuiStatusConfig(status string, frame int) (string, string, lipgloss.Color) {
	codingFrames := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	thinkFrames  := []string{"◐", "◓", "◑", "◒", "◐", "◓", "◑", "◒"}
	waitFrames   := []string{"○", "●", "○", "●", "○", "●", "○", "●"}
	stuckFrames  := []string{"!", " ", "!", " ", "!", " ", "!", " "}
	switch status {
	case "coding":
		return codingFrames[frame], "Coding", colorGreen
	case "testing":
		return codingFrames[frame], "Testing", colorYellow
	case "thinking":
		return thinkFrames[frame], "Thinking…", colorBlue
	case "waiting":
		return waitFrames[frame], "Waiting", colorOrange
	case "stuck":
		return stuckFrames[frame], "STUCK", colorRed
	case "done":
		return "✓", "Done", colorGreen
	default:
		return "·", "Idle", colorDim
	}
}

func monitorBar(status string, frame int) string {
	switch status {
	case "coding", "testing":
		bars := []string{
			"▓▓▓░░░░░", "░▓▓▓░░░░", "░░▓▓▓░░░", "░░░▓▓▓░░",
			"░░░░▓▓▓░", "░░░░░▓▓▓", "▓░░░░░▓▓", "▓▓░░░░░▓",
		}
		return lipgloss.NewStyle().Foreground(colorGreen).Render(bars[frame])
	case "thinking":
		dots := []string{
			"·  ·  · ", " · ·  · ", " ·  · · ", "  · · · ",
			"  ·  ·· ", "   ·  ··", "    · ··", "     ···",
		}
		return lipgloss.NewStyle().Foreground(colorBlue).Render(dots[frame])
	case "waiting":
		return lipgloss.NewStyle().Foreground(colorOrange).Render("▶ ▷ ▷ ▷  ")
	case "stuck":
		if frame%2 == 0 {
			return lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render("! ERROR  ")
		}
		return lipgloss.NewStyle().Foreground(colorRed).Render("         ")
	case "done":
		return lipgloss.NewStyle().Foreground(colorGreen).Render("✓ DONE   ")
	default:
		return lipgloss.NewStyle().Foreground(colorSubtle).Render("─────────")
	}
}

func tuiStageColor(stage string) lipgloss.Color {
	switch stage {
	// Talos phases
	case "spec":
		return colorDim
	case "plan":
		return colorBlue
	case "implement":
		return colorTeal
	case "test":
		return colorYellow
	case "judge":
		return colorPurple
	case "deploy":
		return colorOrange
	case "document":
		return colorDim
	// Task lifecycle states
	case "queued":
		return colorDim
	case "assigned":
		return colorBlue
	case "accepted":
		return colorTeal
	case "running":
		return colorTeal
	case "blocked":
		return colorRed
	case "needs_review":
		return colorYellow
	case "needs_human":
		return colorOrange
	case "complete", "done":
		return colorGreen
	case "failed":
		return colorRed
	case "timed_out":
		return colorOrange
	case "cancelled":
		return colorDim
	default:
		return colorDim
	}
}

// shortStage returns a compact display label for a task stage.
func shortStage(stage string) string {
	switch stage {
	// Talos phases
	case "spec":
		return "spec  "
	case "plan":
		return "plan  "
	case "implement":
		return "impl  "
	case "test":
		return "test  "
	case "judge":
		return "judge "
	case "deploy":
		return "deploy"
	case "document":
		return "docs  "
	// Task lifecycle states
	case "queued":
		return "queued"
	case "assigned":
		return "assign"
	case "accepted":
		return "accept"
	case "running":
		return "runnin"
	case "blocked":
		return "BLOCK!"
	case "needs_review":
		return "review"
	case "needs_human":
		return "HUMAN?"
	case "complete", "done":
		return "done  "
	case "failed":
		return "FAIL  "
	case "timed_out":
		return "t/out "
	case "cancelled":
		return "cancel"
	default:
		return truncStr(stage, 6)
	}
}

// ciDot returns a dot character and style reflecting the task's CI status.
// Falls back to task-stage semantics when no CI status is present.
func ciDot(task *tuiTask) (string, lipgloss.Style) {
	if task.CIStatus != nil {
		switch *task.CIStatus {
		case "success":
			return "●", lipgloss.NewStyle().Foreground(colorGreen)
		case "failure":
			return "●", lipgloss.NewStyle().Foreground(colorRed)
		case "pending":
			return "●", lipgloss.NewStyle().Foreground(colorYellow)
		}
	}
	if task.Stage == "complete" {
		return "◆", lipgloss.NewStyle().Foreground(colorGreen)
	}
	return "◇", lipgloss.NewStyle().Foreground(colorDim)
}

// contextBar renders a coloured fill bar for context usage (0.0–1.0).
// Width is in terminal columns. Colour: green <70%, yellow 70–85%, red >85%.
func contextBar(pct float64, width int) string {
	if width < 4 {
		width = 4
	}
	filled := int(pct * float64(width))
	if filled > width {
		filled = width
	}
	var barC lipgloss.Color
	switch {
	case pct >= ctxPctRotate:
		barC = colorRed
	case pct >= ctxPctWarning:
		barC = colorYellow
	default:
		barC = colorGreen
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	label := fmt.Sprintf("ctx %3.0f%%", pct*100)
	return lipgloss.NewStyle().Foreground(barC).Render(label+" "+bar)
}

// ageStr returns a human-readable duration string for how long ago changedAt
// occurred. Returns "" when changedAt is zero (unknown / pre-migration).
func ageStr(changedAt int64) string {
	if changedAt == 0 {
		return ""
	}
	d := time.Since(time.Unix(changedAt, 0))
	if d < 30*time.Second {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

// ageColor returns a color for the age indicator based on elapsed time and
// status. Returns dim for normal statuses; traffic-light for problem statuses.
func ageColor(changedAt int64, status string) lipgloss.Color {
	switch status {
	case "waiting", "stuck", "blocked", "needs_human":
		// No change info — use dim
	default:
		return colorDim
	}
	if changedAt == 0 {
		return colorDim
	}
	d := time.Since(time.Unix(changedAt, 0))
	switch {
	case d < 5*time.Minute:
		return colorGreen
	case d < 20*time.Minute:
		return colorYellow
	default:
		return colorRed
	}
}

func truncStr(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

// wordWrap wraps s to at most width runes per line, breaking on spaces.
func wordWrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	var out strings.Builder
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out.WriteByte('\n')
			continue
		}
		col := 0
		for i, w := range words {
			wl := len([]rune(w))
			if i == 0 {
				out.WriteString(w)
				col = wl
			} else if col+1+wl > width {
				out.WriteByte('\n')
				out.WriteString(w)
				col = wl
			} else {
				out.WriteByte(' ')
				out.WriteString(w)
				col += 1 + wl
			}
		}
		out.WriteByte('\n')
	}
	result := out.String()
	if len(result) > 0 && result[len(result)-1] == '\n' {
		result = result[:len(result)-1]
	}
	return result
}

func tuiBaseName(path string, maxLen int) string {
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return truncStr(parts[len(parts)-1], maxLen)
	}
	return truncStr(path, maxLen)
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func formatTokensK(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	return fmt.Sprintf("%dk", n/1000)
}

// estimateCostUSD returns a rough blended cost estimate (input+output) based on
// the model name and total token count. Rates are per-million-token blended
// estimates (roughly 70% input / 30% output).
func estimateCostUSD(modelName string, tokens int64) float64 {
	// Blended rates in dollars per token.
	const (
		rateOpus   = 45.0 / 1_000_000 // claude-opus*
		rateSonnet = 9.0 / 1_000_000  // claude-sonnet* (default)
		rateHaiku  = 2.4 / 1_000_000  // claude-haiku*
	)
	var rate float64
	switch {
	case strings.Contains(modelName, "opus"):
		rate = rateOpus
	case strings.Contains(modelName, "haiku"):
		rate = rateHaiku
	default:
		rate = rateSonnet
	}
	return rate * float64(tokens)
}

func formatCostUSD(c float64) string {
	if c < 0.005 {
		return "<$0.01"
	}
	if c < 1.0 {
		return fmt.Sprintf("~$%.2f", c)
	}
	return fmt.Sprintf("~$%.1f", c)
}

// ─── View ─────────────────────────────────────────────────────────────────────

func (m tuiModel) View() string {
	if m.w == 0 {
		return "Loading…"
	}
	if m.helpVisible {
		return m.viewHelpScreen()
	}
	if m.modal != nil {
		return m.viewModal()
	}
	if m.cmdPalette != nil {
		return m.viewCmdPalette()
	}
	if m.settings != nil {
		return m.viewSettings()
	}
	if m.opsView {
		return m.viewOpsConsole()
	}
	if m.notesView {
		return m.viewNotesScreen()
	}
	if m.triageView {
		return m.viewTriageScreen()
	}
	if m.evtDetailView != nil {
		return m.viewEventDetailScreen()
	}
	if m.icingaView {
		return m.viewIcingaScreen()
	}
	if m.evtLogView {
		return m.viewEventLogScreen()
	}
	if m.workQueueView {
		return m.viewWorkQueueScreen()
	}
	if m.goalView {
		return m.viewGoalsScreen()
	}
	if m.escView {
		return m.viewEscalationScreen()
	}

	bodyH := m.h - 3 - 1 - tuiInputH - 2 - 1 - 1 // hud(content+border+join-newline) + help + input borders + status bar + fleet bar
	if bodyH < 5 {
		bodyH = 5
	}

	sidebar := m.viewSidebar(bodyH)
	detail := m.viewDetail(bodyH)
	divider := lipgloss.NewStyle().
		Foreground(colorDim).
		Width(1).
		Height(bodyH).
		Render(strings.Repeat("│\n", bodyH))
	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, divider, detail)

	return strings.Join([]string{m.viewHUD(), body, m.viewStatusBar(), viewFleetStatusBar(m.statusBar, m.w), m.viewInput(), m.viewHelp()}, "\n")
}

func (m tuiModel) viewHUD() string {
	var live, coding, thinking, waiting, stuck int
	var tasksDone, tasksFailed, tasksTotal int
	for _, st := range m.states {
		for _, a := range st.Agents {
			if a.TmuxSession != nil {
				live++
			}
			switch a.Status {
			case "coding":
				coding++
			case "thinking":
				thinking++
			case "waiting":
				waiting++
			case "stuck":
				stuck++
			}
		}
		for _, t := range st.Tasks {
			tasksTotal++
			switch t.Stage {
			case "done", "review", "deploy", "merged":
				tasksDone++
			case "blocked":
				tasksFailed++
			}
		}
	}

	parts := []string{titleStyle.Render("⬡ SwarmOps")}
	parts = append(parts, dimStyle.Render("│"))
	parts = append(parts, dimStyle.Render(fmt.Sprintf("%d session%s", len(m.sessions), pluralS(len(m.sessions)))))
	if tasksTotal > 0 {
		// Health score: done/(total-blocked) ratio as a progress-bar-style indicator
		healthPct := 0
		if tasksTotal > tasksFailed {
			healthPct = tasksDone * 100 / (tasksTotal - tasksFailed)
		}
		healthStr := fmt.Sprintf("%d/%d tasks", tasksDone, tasksTotal)
		var healthC lipgloss.Color
		switch {
		case tasksFailed > 0:
			healthC = colorRed
			healthStr += fmt.Sprintf(" (%d blocked)", tasksFailed)
		case healthPct >= 80:
			healthC = colorGreen
		case healthPct >= 40:
			healthC = colorTeal
		default:
			healthC = colorDim
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(healthC).Render(healthStr))
	}
	if live > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorTeal).Render(fmt.Sprintf("⚡%d live", live)))
	}
	if coding > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorGreen).Render(fmt.Sprintf("▓%d coding", coding)))
	}
	if thinking > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorBlue).Render(fmt.Sprintf("◐%d thinking", thinking)))
	}
	if waiting > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorOrange).Render(fmt.Sprintf("⏸%d waiting", waiting)))
	}
	if stuck > 0 {
		s := lipgloss.NewStyle().Foreground(colorRed).Bold(true)
		if m.frame%2 != 0 {
			s = s.Faint(true)
		}
		parts = append(parts, s.Render(fmt.Sprintf("⚠%d STUCK", stuck)))
	}
	escCount := 0
	for _, st := range m.states {
		escCount += len(st.Escalations)
	}
	if escCount > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorOrange).Bold(true).
			Render(fmt.Sprintf("🔔%d esc", escCount)))
	}
	autopilotCount := 0
	for _, st := range m.states {
		if st.Session.AutopilotEnabled {
			autopilotCount++
		}
	}
	if autopilotCount > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorTeal).Bold(true).
			Render(fmt.Sprintf("⚙%d auto", autopilotCount)))
	}
	var totalTokens int64
	var totalCost float64
	for _, st := range m.states {
		for _, a := range st.Agents {
			totalTokens += a.TokensUsed
			totalCost += estimateCostUSD(a.ModelName, a.TokensUsed)
		}
	}
	if totalTokens > 0 {
		tokStr := formatTokensK(totalTokens) + " tok"
		costStr := formatCostUSD(totalCost)
		costLimit := swarmCostLimitUSD()
		if costLimit > 0 && totalCost >= costLimit {
			warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555")).Bold(true)
			parts = append(parts, warnStyle.Render("⚠ COST LIMIT "+formatCostUSD(totalCost)+"/"+formatCostUSD(costLimit)))
		} else {
			parts = append(parts, dimStyle.Render(tokStr+"  "+costStr))
		}
	}
	if m.updateAvailable {
		updateStyle := lipgloss.NewStyle().Foreground(colorOrange).Bold(true)
		if m.frame%2 != 0 {
			updateStyle = updateStyle.Faint(true)
		}
		parts = append(parts, updateStyle.Render("⬆ update"))
	}
	return hudStyle.Width(m.w).Render(strings.Join(parts, "  "))
}

func (m tuiModel) viewDetail(bodyH int) string {
	rightW := m.w - tuiSidebarW - 1
	if rightW < 10 {
		rightW = 10
	}
	var det strings.Builder
	it := m.selItem()
	if it == nil {
		center := lipgloss.NewStyle().Foreground(colorDim).Width(rightW).Align(lipgloss.Center)
		det.WriteString(center.Render("No sessions") + "\n")
		det.WriteString(center.Render("Press c to create one") + "\n")
	} else {
		switch it.kind {
		case tuiItemSession:
			m.viewSessionDetail(&det, it.sid, rightW)
		case tuiItemAgent:
			m.viewAgentDetail(&det, it.sid, it.eid, rightW)
		case tuiItemTask:
			m.viewTaskDetail(&det, it.sid, it.eid, rightW)
		}
	}
	// Clamp detail to exactly tuiDetailH lines (pad up, truncate down).
	detStr := det.String()
	detLines := strings.Split(detStr, "\n")
	// Split always produces at least one element; trim trailing empty from WriteString's trailing \n.
	if len(detLines) > 0 && detLines[len(detLines)-1] == "" {
		detLines = detLines[:len(detLines)-1]
	}
	if len(detLines) > tuiDetailH {
		detLines = detLines[:tuiDetailH]
	}
	for len(detLines) < tuiDetailH {
		detLines = append(detLines, "")
	}
	detStr = strings.Join(detLines, "\n") + "\n"
	// Divider + viewport
	divLine := dimStyle.Render(strings.Repeat("─", rightW))
	vpStr := ""
	if m.vpReady {
		vpStr = m.vp.View()
	}
	return lipgloss.NewStyle().Width(rightW).
		Render(detStr + divLine + "\n" + vpStr)
}

func (m tuiModel) viewSessionDetail(w *strings.Builder, sid string, rightW int) {
	sess := m.lookupSession(sid)
	if sess == nil {
		return
	}
	st := m.states[sid]
	live := 0
	for _, a := range st.Agents {
		if a.TmuxSession != nil {
			live++
		}
	}
	w.WriteString(lipgloss.NewStyle().Foreground(colorTeal).Bold(true).
		Render("⬡ "+truncStr(sess.Name, rightW-4)) + "\n")
	w.WriteString(dimStyle.Render(fmt.Sprintf("  %d agents (%d live)  ·  %d tasks",
		len(st.Agents), live, len(st.Tasks))) + "\n")
	w.WriteString(dimStyle.Render(fmt.Sprintf("  ID: %s…", sess.ID[:12])) + "\n")
	activeGoals := 0
	for _, g := range st.Goals {
		if g.Status == "active" {
			activeGoals++
		}
	}
	if activeGoals > 0 {
		w.WriteString(lipgloss.NewStyle().Foreground(colorOrange).
			Render(fmt.Sprintf("  %d active goal(s) — /goal <desc> to add more", activeGoals)) + "\n")
	} else {
		w.WriteString(dimStyle.Render("  No active goals — /goal <desc> to set one") + "\n")
	}
	w.WriteString(dimStyle.Render("  n=agent  t=task  r=resume  e=escalations  R=refresh") + "\n")
}

func (m tuiModel) viewAgentDetail(w *strings.Builder, sid, aid string, rightW int) {
	agent := m.lookupAgent(sid, aid)
	if agent == nil {
		return
	}
	_, roleColor := tuiRoleConfig(agent.Role)
	spinFrame, statusLabel, statusColor := tuiStatusConfig(agent.Status, m.frame)
	monitor := monitorBar(agent.Status, m.frame)

	// ── Build info lines (right of sprite) ───────────────────────────────────
	// Tall sprite: 6 pixels × 2 chars each = 12 cols + 1 gap = 13 offset
	infoW := rightW - 14
	if infoW < 10 {
		infoW = 10
	}
	var info strings.Builder

	info.WriteString(lipgloss.NewStyle().Foreground(roleColor).Bold(true).
		Render(truncStr(agent.Name, infoW)) + "\n")

	statusPart := lipgloss.NewStyle().Foreground(statusColor).Render(spinFrame + " " + statusLabel)
	ageSuffix := ""
	if a := ageStr(agent.StatusChangedAt); a != "" {
		ageSuffix = " " + lipgloss.NewStyle().Foreground(ageColor(agent.StatusChangedAt, agent.Status)).Render(a)
	}
	info.WriteString(statusPart + ageSuffix + "  " + dimStyle.Render("["+monitor+"]") + "\n")

	if agent.Mission != nil {
		missionStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f8fafc")).Italic(true)
		info.WriteString(missionStyle.Render(truncStr(*agent.Mission, infoW)) + "\n")
	} else {
		info.WriteString("\n")
	}

	if agent.CurrentFile != nil {
		info.WriteString(dimStyle.Render("f "+truncStr(*agent.CurrentFile, infoW-2)) + "\n")
	} else {
		info.WriteString("\n")
	}
	if agent.CurrentTask != nil {
		task := m.lookupTask(sid, *agent.CurrentTask)
		if task != nil {
			stageC := tuiStageColor(task.Stage)
			stageStr := lipgloss.NewStyle().Foreground(stageC).Render("[" + task.Stage + "]")
			info.WriteString(truncStr(task.Title, infoW-10) + " " + stageStr + "\n")
		} else {
			info.WriteString("\n")
		}
	} else {
		info.WriteString("\n")
	}
	if agent.LatestNote != nil {
		noteStyle := lipgloss.NewStyle().Foreground(colorYellow).Italic(true)
		info.WriteString(noteStyle.Render(truncStr(*agent.LatestNote, infoW)) + "\n")
	} else {
		info.WriteString("\n")
	}
	if agent.TmuxSession != nil {
		info.WriteString(lipgloss.NewStyle().Foreground(colorTeal).
			Render(*agent.TmuxSession + " [Enter]") + "\n")
	} else {
		info.WriteString(dimStyle.Render("offline  s:spawn") + "\n")
	}
	// Git status line
	if gs, ok := m.gitStatus[agent.ID]; ok && (gs.Branch != "" || gs.Subject != "") {
		var gitParts []string
		if gs.Branch != "" {
			branchStyle := lipgloss.NewStyle().Foreground(colorPurple)
			branchStr := branchStyle.Render("⎇ " + truncStr(gs.Branch, 20))
			if gs.Dirty {
				branchStr += lipgloss.NewStyle().Foreground(colorOrange).Render(" ✎")
			}
			if gs.Ahead > 0 {
				branchStr += lipgloss.NewStyle().Foreground(colorYellow).Render(fmt.Sprintf(" ↑%d", gs.Ahead))
			}
			gitParts = append(gitParts, branchStr)
		}
		if gs.Subject != "" {
			gitParts = append(gitParts, dimStyle.Render(truncStr(gs.Subject, infoW-22)))
		}
		info.WriteString(strings.Join(gitParts, "  ") + "\n")
	} else {
		info.WriteString("\n")
	}
	// Context usage bar
	if agent.ContextPct > 0 {
		bar := contextBar(agent.ContextPct, infoW)
		info.WriteString(bar + "\n")
	} else if agent.ModelName != "" || agent.TokensUsed > 0 {
		var metaParts []string
		if agent.ModelName != "" {
			modelShort := strings.TrimPrefix(agent.ModelName, "claude-")
			metaParts = append(metaParts, dimStyle.Render(modelShort))
		}
		if agent.TokensUsed > 0 {
			metaParts = append(metaParts, lipgloss.NewStyle().Foreground(colorTeal).Render(formatTokensK(agent.TokensUsed)+" tok"))
		}
		info.WriteString(strings.Join(metaParts, "  ") + "\n")
	} else {
		info.WriteString(dimStyle.Render(agent.Role + "  " + agent.ID[:8] + "…") + "\n")
	}

	// Model + tokens line (shown when context bar is present too)
	if agent.ContextPct > 0 && (agent.ModelName != "" || agent.TokensUsed > 0) {
		var metaParts []string
		if agent.ModelName != "" {
			modelShort := strings.TrimPrefix(agent.ModelName, "claude-")
			metaParts = append(metaParts, dimStyle.Render(modelShort))
		}
		if agent.TokensUsed > 0 {
			metaParts = append(metaParts, lipgloss.NewStyle().Foreground(colorTeal).Render(formatTokensK(agent.TokensUsed)+" tok"))
		}
		info.WriteString(strings.Join(metaParts, "  ") + "\n")
	}

	// ── Sprite card ───────────────────────────────────────────────────────────
	sprite := GetAgentSpriteBrailleTall(agent.Role, agent.Status, m.frame)
	spriteCol := lipgloss.NewStyle().
		Background(SpritePanelBG).
		PaddingRight(1).
		Render(sprite)
	card := lipgloss.JoinHorizontal(lipgloss.Top, spriteCol, info.String())
	w.WriteString(card + "\n")

	// ── Diagnostic preview for stuck/waiting/failed agents ────────────────────
	switch agent.Status {
	case "stuck", "waiting", "failed":
		if content, ok := m.termContent[agent.ID]; ok && content != "" {
			// Extract last 4 non-empty lines from terminal capture
			allLines := strings.Split(content, "\n")
			var lastLines []string
			for i := len(allLines) - 1; i >= 0 && len(lastLines) < 4; i-- {
				if strings.TrimSpace(allLines[i]) != "" {
					lastLines = append([]string{allLines[i]}, lastLines...)
				}
			}
			if len(lastLines) > 0 {
				w.WriteString(dimStyle.Render("  last output:") + "\n")
				for _, l := range lastLines {
					w.WriteString(dimStyle.Render("    "+truncStr(l, rightW-4)) + "\n")
				}
			}
		} else if agent.TmuxSession != nil {
			w.WriteString(dimStyle.Render("  (press Enter to attach for live view)") + "\n")
		}
	}
}

func (m tuiModel) viewTaskDetail(w *strings.Builder, sid, tid string, rightW int) {
	task := m.lookupTask(sid, tid)
	if task == nil {
		return
	}
	stageC := tuiStageColor(task.Stage)
	w.WriteString(lipgloss.NewStyle().Foreground(stageC).Bold(true).
		Render("◆ "+truncStr(task.Title, rightW-4)) + "\n")
	stageAgeSuffix := ""
	if a := ageStr(task.StageChangedAt); a != "" {
		stageAgeSuffix = " " + lipgloss.NewStyle().Foreground(ageColor(task.StageChangedAt, task.Stage)).Render(a)
	}
	w.WriteString(dimStyle.Render("  Stage: ") +
		lipgloss.NewStyle().Foreground(stageC).Render(task.Stage) + stageAgeSuffix + "\n")
	w.WriteString(dimStyle.Render("  ID: "+task.ID[:12]+"…") + "\n")

	if task.Phase != nil {
		phaseLabel := fmt.Sprintf("  Phase: %s", *task.Phase)
		if task.PhaseOrder != nil {
			phaseLabel += fmt.Sprintf(" (%d/8)", *task.PhaseOrder)
		}
		w.WriteString(lipgloss.NewStyle().Foreground(colorBlue).Render(phaseLabel) + "\n")
	}

	if task.CIStatus != nil && *task.CIStatus != "" {
		var ciC lipgloss.Color
		var ciIcon string
		switch {
		case strings.HasPrefix(*task.CIStatus, "completed:success"):
			ciC, ciIcon = colorGreen, "✅"
		case strings.HasPrefix(*task.CIStatus, "completed:"):
			ciC, ciIcon = colorRed, "❌"
		case *task.CIStatus == "in_progress":
			ciC, ciIcon = colorYellow, "⏳"
		default:
			ciC, ciIcon = colorDim, "○"
		}
		w.WriteString(dimStyle.Render("  CI: ") +
			lipgloss.NewStyle().Foreground(ciC).Render(ciIcon+" "+*task.CIStatus) + "\n")
	}
	if task.PRUrl != nil && *task.PRUrl != "" {
		w.WriteString(dimStyle.Render("  PR: "+truncStr(*task.PRUrl, rightW-6)) + "\n")
	}

	if task.Confidence != nil {
		conf := *task.Confidence
		var confC lipgloss.Color
		var confLabel string
		switch {
		case conf >= 0.8:
			confC = colorGreen
			confLabel = fmt.Sprintf("%.0f%% confidence", conf*100)
		case conf >= 0.6:
			confC = colorYellow
			confLabel = fmt.Sprintf("%.0f%% confidence", conf*100)
		default:
			confC = colorRed
			confLabel = fmt.Sprintf("%.0f%% confidence (low)", conf*100)
		}
		w.WriteString(dimStyle.Render("  ") +
			lipgloss.NewStyle().Foreground(confC).Render(confLabel) + "\n")
	}
	if task.TokensUsed != nil {
		w.WriteString(dimStyle.Render(fmt.Sprintf("  tokens: %d", *task.TokensUsed)) + "\n")
	}
	if task.BlockedReason != nil {
		w.WriteString(lipgloss.NewStyle().Foreground(colorRed).
			Render("  blocked: "+truncStr(*task.BlockedReason, rightW-12)) + "\n")
	}
}

func (m tuiModel) viewInput() string {
	borderColor := colorDim
	if m.focus == tuiFocusInput {
		borderColor = colorTeal
	}
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(borderColor).
		Width(m.w - 2).
		Render(m.chatInput.View())
}

func (m tuiModel) viewStatusBar() string {
	// Left: contextual info about selected item
	var leftParts []string
	it := m.selItem()
	if it != nil {
		switch it.kind {
		case tuiItemSession:
			sess := m.lookupSession(it.sid)
			if sess != nil {
				st := m.states[it.sid]
				live := 0
				var totalTok int64
				var sessCost float64
				for _, a := range st.Agents {
					if a.TmuxSession != nil {
						live++
					}
					totalTok += a.TokensUsed
					sessCost += estimateCostUSD(a.ModelName, a.TokensUsed)
				}
				leftParts = append(leftParts,
					lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render(truncStr(sess.Name, 24)))
				leftParts = append(leftParts,
					dimStyle.Render(fmt.Sprintf("%d agents (%d live) · %d tasks", len(st.Agents), live, len(st.Tasks))))
				if totalTok > 0 {
					leftParts = append(leftParts,
						dimStyle.Render(formatTokensK(totalTok)+" tok  "+formatCostUSD(sessCost)))
				}
				if sess.AutopilotEnabled {
					leftParts = append(leftParts,
						lipgloss.NewStyle().Foreground(colorTeal).Render("⚙ auto"))
				}
			}
		case tuiItemAgent:
			agent := m.lookupAgent(it.sid, it.eid)
			if agent != nil {
				_, roleColor := tuiRoleConfig(agent.Role)
				_, statusLabel, statusColor := tuiStatusConfig(agent.Status, m.frame)
				leftParts = append(leftParts,
					lipgloss.NewStyle().Foreground(roleColor).Bold(true).Render(truncStr(agent.Name, 16)))
				leftParts = append(leftParts,
					lipgloss.NewStyle().Foreground(statusColor).Render(statusLabel))
				if agent.ModelName != "" {
					modelShort := strings.TrimPrefix(agent.ModelName, "claude-")
					leftParts = append(leftParts, dimStyle.Render(modelShort))
				}
				if agent.TokensUsed > 0 {
					costStr := formatCostUSD(estimateCostUSD(agent.ModelName, agent.TokensUsed))
					leftParts = append(leftParts,
						lipgloss.NewStyle().Foreground(colorTeal).Render(formatTokensK(agent.TokensUsed)+" tok  "+costStr))
				}
				if agent.ContextPct > 0 {
					var barC lipgloss.Color
					switch {
					case agent.ContextPct >= ctxPctRotate:
						barC = colorRed
					case agent.ContextPct >= ctxPctWarning:
						barC = colorYellow
					default:
						barC = colorGreen
					}
					leftParts = append(leftParts,
						lipgloss.NewStyle().Foreground(barC).Render(fmt.Sprintf("ctx %.0f%%", agent.ContextPct*100)))
				}
				if agent.CurrentFile != nil {
					leftParts = append(leftParts,
						dimStyle.Render("f:"+tuiBaseName(*agent.CurrentFile, 20)))
				}
			}
		case tuiItemTask:
			task := m.lookupTask(it.sid, it.eid)
			if task != nil {
				stageC := tuiStageColor(task.Stage)
				leftParts = append(leftParts,
					lipgloss.NewStyle().Foreground(stageC).Bold(true).Render(truncStr(task.Title, 28)))
				leftParts = append(leftParts,
					lipgloss.NewStyle().Foreground(stageC).Render(task.Stage))
				if task.Phase != nil {
					leftParts = append(leftParts, dimStyle.Render(*task.Phase))
				}
				if task.CIStatus != nil && *task.CIStatus != "" {
					leftParts = append(leftParts, dimStyle.Render("CI:"+*task.CIStatus))
				}
			}
		}
	}

	// Right: mode badge + flash message
	var rightParts []string
	switch {
	case m.pendingConfirm != nil:
		rightParts = append(rightParts,
			lipgloss.NewStyle().Background(colorRed).Foreground(colorText).Bold(true).Render(" CONFIRM "))
	case m.focus == tuiFocusModal:
		rightParts = append(rightParts,
			lipgloss.NewStyle().Background(colorYellow).Foreground(lipgloss.Color("#0a1628")).Bold(true).Render(" MODAL "))
	case m.focus == tuiFocusInput:
		rightParts = append(rightParts,
			lipgloss.NewStyle().Background(colorTeal).Foreground(lipgloss.Color("#0a1628")).Bold(true).Render(" CHAT "))
	}
	if m.flash != "" {
		icon := "✓"
		c := colorGreen
		if m.flashErr {
			icon = "✗"
			c = colorRed
		}
		rightParts = append(rightParts, lipgloss.NewStyle().Foreground(c).Render(icon+" "+m.flash))
	}
	var rightStr string
	if len(rightParts) > 0 {
		rightStr = strings.Join(rightParts, "  ")
	}

	leftStr := "  " + strings.Join(leftParts, "  ·  ")
	if len(leftParts) == 0 {
		leftStr = "  —"
	}

	barStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#0a1628")).
		Foreground(colorDim)

	if rightStr == "" {
		return barStyle.Width(m.w).Render(leftStr)
	}

	// Pad left to fill width, then append right
	rightLen := lipgloss.Width(rightStr) + 2
	leftWidth := m.w - rightLen
	if leftWidth < 0 {
		leftWidth = 0
	}
	leftRendered := barStyle.Width(leftWidth).Render(leftStr)
	rightRendered := barStyle.Render("  " + rightStr)
	return leftRendered + rightRendered
}

func (m tuiModel) viewHelp() string {
	keys := "  ↑↓/jk nav  ·  Enter attach/collapse  ·  Tab// input  ·  s spawn  ·  d stop  ·  i inject  ·  N notes  ·  D delete  ·  S stage  ·  + quick-agent  ·  n agent  ·  t task  ·  c session  ·  E edit  ·  T triage  ·  I icinga  ·  L log  ·  e esc  ·  g goals  ·  R refresh  ·  q quit"
	// Truncate to terminal width — do NOT use Width() here as that causes wrapping,
	// which pushes content off the top of the screen.
	return dimStyle.Render(truncStr(keys, m.w))
}

// ─── Help screen ──────────────────────────────────────────────────────────────

func (m tuiModel) viewHelpScreen() string {
	h := func(s string) string {
		return lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render(s)
	}
	dim := func(s string) string { return dimStyle.Render(s) }
	key := func(k, desc string) string {
		return fmt.Sprintf("  %-14s %s", lipgloss.NewStyle().Foreground(colorText).Render(k), dim(desc))
	}
	badge := func(s string, c lipgloss.Color) string {
		return lipgloss.NewStyle().Foreground(c).Bold(true).Render(s)
	}

	var sb strings.Builder

	sb.WriteString(lipgloss.NewStyle().Foreground(colorTeal).Bold(true).
		Render("SwarmOps") + dim("  —  AI Agent Swarm Orchestrator") + "\n\n")

	// ── Concepts ──
	sb.WriteString(h("CONCEPTS") + "\n\n")

	sb.WriteString(h("  Session") + "\n")
	sb.WriteString("    A swarm run. Groups agents, goals, and tasks into one workspace.\n")
	sb.WriteString(dim("    Create with c. Resumable — agents re-attach on restart.\n") + "\n")

	sb.WriteString(h("  Goal") + "\n")
	sb.WriteString("    A high-level objective. SiBot decomposes it into concrete tasks.\n")
	sb.WriteString(dim("    Set via /goal <description> in the chat bar, or synced from Plane.\n") + "\n")

	sb.WriteString(h("  Task") + "\n")
	sb.WriteString("    A unit of work assigned to one agent. Talos 8-phase lifecycle:\n")
	sb.WriteString("    " +
		badge("spec", colorDim) + " → " +
		badge("plan", colorBlue) + " → " +
		badge("review", colorPurple) + " → " +
		badge("impl", colorTeal) + " → " +
		badge("review", colorPurple) + " → " +
		badge("judge", colorPurple) + " → " +
		badge("deploy", colorOrange) + " → " +
		badge("docs", colorDim) + "\n")
	sb.WriteString(dim("    Task state machine: queued → assigned → running → needs_review / complete\n"))
	sb.WriteString(dim("    Blocked tasks auto-revert to queued. Auto-dispatch fires on new tasks\n"))
	sb.WriteString(dim("    and when idle agents become available.\n") + "\n")

	sb.WriteString(h("  Agent") + "\n")
	sb.WriteString("    A Claude Code instance running in a tmux session. Two roles:\n")
	sb.WriteString("    " + badge("orchestrator", colorTeal) + dim("  SiBot — decomposes goals, assigns tasks, manages workers\n"))
	sb.WriteString("    " + badge("worker     ", colorGreen) + dim("  Implements tasks, reports progress, requests escalation\n"))
	sb.WriteString(dim("    Context rotation fires at 70% / 85% / 95% usage with handoff.\n") + "\n")

	sb.WriteString(h("  Project") + "\n")
	sb.WriteString("    A Plane project linked to the session. In autopilot mode the backlog\n")
	sb.WriteString(dim("    is polled and issues become goals automatically (toggle with A).\n") + "\n")

	// ── Keys ──
	sb.WriteString(h("NAVIGATION") + "\n")
	sb.WriteString(key("↑↓  j k  w s", "Move sidebar cursor") + "\n")
	sb.WriteString(key("Enter (session)", "Collapse / expand session in sidebar") + "\n")
	sb.WriteString(key("Enter (agent)", "Attach to agent's tmux session") + "\n")
	sb.WriteString(key("Tab  /", "Focus chat / message bar") + "\n")
	sb.WriteString(key("q", "Quit") + "\n\n")

	sb.WriteString(h("ACTIONS") + "\n")
	sb.WriteString(key("Alt+S", "Spawn agent") + "\n")
	sb.WriteString(key("d  d", "Stop agent (press twice to confirm)") + "\n")
	sb.WriteString(key("i", "Inject message to agent's Claude Code session") + "\n")
	sb.WriteString(key("N", "View and add agent notes") + "\n")
	sb.WriteString(key("D  D", "Delete agent (offline) or task (press twice)") + "\n")
	sb.WriteString(key("S", "Manually set task stage") + "\n")
	sb.WriteString(key("X  [type]", "Delete session (type name to confirm)") + "\n")
	sb.WriteString(key("+", "Quick-spawn a worker (name only)") + "\n")
	sb.WriteString(key("n", "New agent (full form)") + "\n")
	sb.WriteString(key("t", "New task") + "\n")
	sb.WriteString(key("c", "New session  (optional: template=dev/research/fullstack/devops)") + "\n")
	sb.WriteString(key("E", "Edit selected session / agent / task") + "\n")
	sb.WriteString(key("P", "Edit role prompt for selected agent's role in $EDITOR") + "\n")
	sb.WriteString(key("A", "Toggle autopilot (Plane sync)") + "\n\n")

	sb.WriteString(h("VIEWS") + "\n")
	sb.WriteString(key("T", "Global triage — all blocked/stuck/escalations across sessions") + "\n")
	sb.WriteString(key("I", "Icinga monitor — services + recent alerts") + "\n")
	sb.WriteString(key("L", "Event log — full retrospective, agent filter, detail") + "\n")
	sb.WriteString(key("e", "Escalations — pending human-in-the-loop requests") + "\n")
	sb.WriteString(key("g", "Goals — goal status + budget tracking") + "\n")
	sb.WriteString(key("W", "Work queue — Plane backlog") + "\n")
	sb.WriteString(key("R", "Refresh all data") + "\n")
	sb.WriteString(key("Alt+F", "Submit feedback / bug report to Plane (snapshot auto-attached)") + "\n\n")

	sb.WriteString(dim("  Hold ? to keep this screen open · release to dismiss"))

	boxW := m.w - 4
	if boxW > 90 {
		boxW = 90
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorTeal).
		Padding(1, 3).
		Width(boxW).
		Render(sb.String())

	padTop := (m.h - lipgloss.Height(box)) / 2
	if padTop < 0 {
		padTop = 0
	}
	padLeft := (m.w - lipgloss.Width(box)) / 2
	if padLeft < 0 {
		padLeft = 0
	}
	indent := strings.Repeat(" ", padLeft)
	rows := strings.Split(box, "\n")
	for i, r := range rows {
		rows[i] = indent + r
	}
	return strings.Repeat("\n", padTop) + strings.Join(rows, "\n")
}
