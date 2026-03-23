package main

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── Context Picker ───────────────────────────────────────────────────────────
//
// Opened with 'C' on a session item. Lists available session contexts; Enter
// assigns the selected context to the session; Backspace/Del clears it; Esc cancels.

type tuiCtxPickerMsg struct {
	items []ctxPickerItem
}

// tuiCtxPickerModel holds ephemeral picker state (session being edited + items).
type ctxPickerItem struct {
	id         string
	name       string
	description string
	summary    string
	hasDynamic bool
}

type tuiCtxPickerModel struct {
	sid    string
	items  []ctxPickerItem
	cursor int
	ready  bool
}

func newCtxPicker(sid string) *tuiCtxPickerModel {
	return &tuiCtxPickerModel{sid: sid}
}

// fetchContextsForPicker loads all session_contexts for the picker via the HTTP API.
func fetchContextsForPicker(client TUIClient) tea.Cmd {
	return func() tea.Msg {
		b, err := client.getSync("/api/swarm/contexts")
		if err != nil {
			return tuiErrMsg{op: "ctx-picker", text: err.Error()}
		}
		var raw []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			Description    string `json:"description"`
			Summary        string `json:"summary"`
			DynamicContext string `json:"dynamic_context"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			return tuiErrMsg{op: "ctx-picker", text: err.Error()}
		}
		items := make([]ctxPickerItem, 0, len(raw))
		for _, r := range raw {
			items = append(items, ctxPickerItem{
				id:          r.ID,
				name:        r.Name,
				description: r.Description,
				summary:     r.Summary,
				hasDynamic:  r.DynamicContext != "",
			})
		}
		return tuiCtxPickerMsg{items: items}
	}
}

// updateCtxPicker handles key events when the context picker is open.
func (m tuiModel) updateCtxPicker(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	cp := m.ctxPicker
	if cp == nil {
		return m, nil
	}

	switch msg.String() {
	case "esc", "q":
		m.ctxPicker = nil

	case "up", "k":
		if cp.cursor > 0 {
			cp.cursor--
		}

	case "down", "j":
		if cp.cursor < len(cp.items)-1 {
			cp.cursor++
		}

	case "enter":
		if len(cp.items) > 0 {
			item := cp.items[cp.cursor]
			m.ctxPicker = nil
			path := "/api/swarm/sessions/" + cp.sid + "/context"
			return m, []tea.Cmd{m.client.patch("set-context", path, map[string]string{"context_id": item.id})}
		}

	case "backspace", "delete", "x":
		// Clear context from session
		m.ctxPicker = nil
		path := "/api/swarm/sessions/" + cp.sid + "/context"
		return m, []tea.Cmd{m.client.patch("set-context", path, map[string]interface{}{"context_id": nil})}
	}

	return m, nil
}

// viewCtxPicker renders the context picker as a centered overlay.
func (m tuiModel) viewCtxPicker() string {
	cp := m.ctxPicker
	if cp == nil {
		return ""
	}
	w := m.w
	boxW := min(66, w-4)

	var sb strings.Builder
	sb.WriteString(lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("⬡ Assign Session Context") + "\n\n")

	sess := m.lookupSession(cp.sid)
	if sess != nil {
		cur := dimStyle.Render("(none)")
		if sess.ContextName != nil {
			cur = lipgloss.NewStyle().Foreground(colorTeal).Render(*sess.ContextName)
		}
		sb.WriteString(dimStyle.Render("Session: ") + lipgloss.NewStyle().Foreground(colorText).Render(sess.Name) + "\n")
		sb.WriteString(dimStyle.Render("Current: ") + cur + "\n\n")
	}

	if !cp.ready {
		sb.WriteString(dimStyle.Render("  Loading contexts…") + "\n")
	} else if len(cp.items) == 0 {
		sb.WriteString(dimStyle.Render("  No contexts defined — create one in Settings (Esc → Session Contexts)") + "\n")
	} else {
		maxShow := min(10, len(cp.items))
		start := 0
		if cp.cursor >= maxShow {
			start = cp.cursor - maxShow + 1
		}
		end := start + maxShow
		if end > len(cp.items) {
			end = len(cp.items)
		}
		for i := start; i < end; i++ {
			item := cp.items[i]
			cursor := "  "
			nameStyle := lipgloss.NewStyle().Foreground(colorText)
			descStyle := dimStyle
			if i == cp.cursor {
				cursor = lipgloss.NewStyle().Foreground(colorTeal).Render("▶ ")
				nameStyle = lipgloss.NewStyle().Foreground(colorTeal).Bold(true)
			}
			name := fmt.Sprintf("%-24s", truncStr(item.name, 24))
			desc := truncStr(item.description, boxW-30)
			sb.WriteString(cursor + nameStyle.Render(name) + "  " + descStyle.Render(desc) + "\n")
		}
		if len(cp.items) > maxShow {
			more := len(cp.items) - maxShow
			sb.WriteString(dimStyle.Render(fmt.Sprintf("\n  %d more — ↑/↓ to scroll", more)) + "\n")
		}
		// Detail pane for highlighted item
		if cp.cursor < len(cp.items) {
			sel := cp.items[cp.cursor]
			if sel.summary != "" || sel.hasDynamic {
				sb.WriteString("\n" + dimStyle.Render(strings.Repeat("─", boxW-4)) + "\n")
				if sel.summary != "" {
					sumLine := truncStr(sel.summary, boxW-4)
					sb.WriteString(dimStyle.Render(sumLine) + "\n")
				}
				if sel.hasDynamic {
					sb.WriteString(lipgloss.NewStyle().Foreground(colorOrange).Render("⚡ dynamic context active") + "\n")
				}
			}
		}
	}

	sb.WriteString("\n" + dimStyle.Render("↑/↓ navigate  Enter assign  x/Del clear  Esc cancel"))

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
	rows := strings.Split(box, "\n")
	for i, r := range rows {
		rows[i] = indent + r
	}
	return top + strings.Join(rows, "\n")
}
