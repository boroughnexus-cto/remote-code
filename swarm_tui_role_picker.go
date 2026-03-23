package main

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── Role Picker ──────────────────────────────────────────────────────────────
//
// Opened with 'n' when creating a new agent, and automatically after a new
// session's context picker closes (post-session flow).
//
// Lists defined agent personas from /api/swarm/role-prompts plus a "Custom…"
// option. Enter opens tuiModalNewAgent pre-filled with the selected role.
// Esc cancels entirely (no modal opened).

type rolePickerItem struct {
	role    string
	preview string // first non-empty line of prompt, truncated
}

type tuiRolePickerMsg struct {
	items []rolePickerItem
}

type tuiRolePickerModel struct {
	sid           string
	items         []rolePickerItem
	cursor        int
	ready         bool
	isPostSession bool // title/hint differs; Esc just skips instead of cancelling
}

func newRolePicker(sid string, isPostSession bool) *tuiRolePickerModel {
	return &tuiRolePickerModel{sid: sid, isPostSession: isPostSession}
}

// fetchRolesForPicker loads agent personas via the HTTP API.
func fetchRolesForPicker(client TUIClient) tea.Cmd {
	return func() tea.Msg {
		b, err := client.getSync("/api/swarm/role-prompts")
		if err != nil {
			return tuiRolePickerMsg{}
		}
		var raw []struct {
			Role   string `json:"role"`
			Prompt string `json:"prompt"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			return tuiRolePickerMsg{}
		}
		items := make([]rolePickerItem, 0, len(raw))
		for _, r := range raw {
			if strings.HasPrefix(r.Role, "_") {
				continue
			}
			preview := ""
			for _, line := range strings.SplitN(strings.TrimSpace(r.Prompt), "\n", 5) {
				line = strings.TrimSpace(line)
				if line != "" {
					preview = line
					break
				}
			}
			if len([]rune(preview)) > 58 {
				preview = string([]rune(preview)[:58]) + "…"
			}
			items = append(items, rolePickerItem{role: r.Role, preview: preview})
		}
		return tuiRolePickerMsg{items: items}
	}
}

// updateRolePicker handles key events when the role picker is open.
func (m tuiModel) updateRolePicker(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	rp := m.rolePicker
	if rp == nil {
		return m, nil
	}
	// total = defined roles + 1 "Custom…" slot
	total := len(rp.items) + 1

	switch msg.String() {
	case "esc", "q":
		// Cancel — no modal opened in either flow
		m.rolePicker = nil

	case "up", "k":
		if rp.cursor > 0 {
			rp.cursor--
		}

	case "down", "j":
		if rp.cursor < total-1 {
			rp.cursor++
		}

	case "enter":
		sid := rp.sid
		var selectedRole string
		if rp.cursor < len(rp.items) {
			selectedRole = rp.items[rp.cursor].role
		}
		// Custom… or Esc → blank role in modal
		m.rolePicker = nil
		mo := newTUIModal(tuiModalNewAgent, sid)
		if selectedRole != "" {
			mo.fields[1].ti.SetValue(selectedRole)
		}
		m.modal = mo
		m.focus = tuiFocusModal
	}

	return m, nil
}

// viewRolePicker renders the role picker as a centered overlay.
func (m tuiModel) viewRolePicker() string {
	rp := m.rolePicker
	if rp == nil {
		return ""
	}
	w := m.w
	boxW := min(70, w-4)

	title := "Select Agent Role"
	if rp.isPostSession {
		title = "Add Agent to Session"
	}

	var sb strings.Builder
	sb.WriteString(lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("⬡ "+title) + "\n\n")

	if !rp.ready {
		sb.WriteString(dimStyle.Render("  Loading roles…") + "\n")
	} else if len(rp.items) == 0 {
		sb.WriteString(dimStyle.Render("  No custom roles defined yet.") + "\n")
		sb.WriteString(dimStyle.Render("  Define roles in Settings → Agent Personas (Esc to open).") + "\n")
	} else {
		maxShow := min(10, len(rp.items))
		start := 0
		if rp.cursor < len(rp.items) && rp.cursor >= maxShow {
			start = rp.cursor - maxShow + 1
		}
		end := min(start+maxShow, len(rp.items))
		for i := start; i < end; i++ {
			item := rp.items[i]
			csr := "  "
			nameStyle := lipgloss.NewStyle().Foreground(colorText)
			if i == rp.cursor {
				csr = lipgloss.NewStyle().Foreground(colorTeal).Render("▶ ")
				nameStyle = lipgloss.NewStyle().Foreground(colorTeal).Bold(true)
			}
			name := fmt.Sprintf("%-22s", truncStr(item.role, 22))
			preview := truncStr(item.preview, boxW-30)
			sb.WriteString(csr + nameStyle.Render(name) + "  " + dimStyle.Render(preview) + "\n")
		}
		if len(rp.items) > maxShow {
			more := len(rp.items) - maxShow
			sb.WriteString(dimStyle.Render(fmt.Sprintf("\n  %d more — ↑/↓ to scroll", more)) + "\n")
		}
	}

	// "Custom…" option — always shown at bottom
	customCsr := "  "
	customStyle := dimStyle
	if rp.ready && rp.cursor >= len(rp.items) {
		customCsr = lipgloss.NewStyle().Foreground(colorTeal).Render("▶ ")
		customStyle = lipgloss.NewStyle().Foreground(colorTeal)
	}
	sb.WriteString(customCsr + customStyle.Render("Custom…") + "  " + dimStyle.Render("type role manually in next form") + "\n")

	helpLine := "↑/↓ navigate  Enter select"
	if rp.isPostSession {
		helpLine += "  Esc skip"
	} else {
		helpLine += "  Esc cancel"
	}
	sb.WriteString("\n" + dimStyle.Render(helpLine))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorTeal).
		Padding(1, 2).
		Width(boxW).
		Render(sb.String())

	padTop := (m.h - lipgloss.Height(box)) / 3
	padLeft := (m.w - lipgloss.Width(box)) / 2
	if padTop < 0 {
		padTop = 0
	}
	if padLeft < 0 {
		padLeft = 0
	}
	top := strings.Repeat("\n", padTop)
	indent := strings.Repeat(" ", padLeft)
	rowStrs := strings.Split(box, "\n")
	for i, r := range rowStrs {
		rowStrs[i] = indent + r
	}
	return top + strings.Join(rowStrs, "\n")
}
