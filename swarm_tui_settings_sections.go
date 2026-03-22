package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── Section: Swarm Config ────────────────────────────────────────────────────
//
// Shows all keys from configRegistry, grouped by prefix. Cursor selects a key;
// Enter opens inline edit; Enter again saves via globalConfigService.Set().
// Changes persist to the system_config table immediately.

type swarmConfigSection struct {
	keys    []configEntry // sorted snapshot from GetAll
	cursor  int
	loading bool
	err     string

	// Inline edit state
	editing  bool
	editKey  string
	editTI   textinput.Model
	editDirty bool
	saveErr  string
}

// swarmConfigLoadedMsg carries a fresh snapshot of all config entries.
type swarmConfigLoadedMsg struct {
	entries []configEntry
}

// swarmConfigSavedMsg is returned after a Set call.
type swarmConfigSavedMsg struct {
	key string
	err error
}

func newSwarmConfigSection() *swarmConfigSection {
	ti := textinput.New()
	ti.CharLimit = 200
	return &swarmConfigSection{loading: true, editTI: ti}
}

func (s *swarmConfigSection) Title() string { return "Swarm Config" }
func (s *swarmConfigSection) IsDirty() bool { return s.editDirty }
func (s *swarmConfigSection) Commit() tea.Cmd {
	key := s.editKey
	val := strings.TrimSpace(s.editTI.Value())
	return func() tea.Msg {
		if globalConfigService == nil {
			return swarmConfigSavedMsg{key: key, err: fmt.Errorf("config service not initialised")}
		}
		err := globalConfigService.Set(key, val, "user")
		return swarmConfigSavedMsg{key: key, err: err}
	}
}
func (s *swarmConfigSection) Discard() {
	s.editDirty = false
	s.editing = false
	s.saveErr = ""
}

func (s *swarmConfigSection) Init() tea.Cmd {
	s.loading = true
	return func() tea.Msg {
		if globalConfigService == nil {
			return swarmConfigLoadedMsg{}
		}
		return swarmConfigLoadedMsg{entries: globalConfigService.GetAll("")}
	}
}

func (s *swarmConfigSection) Update(msg tea.KeyMsg) (SettingsSectionModel, []tea.Cmd) {
	if s.editing {
		return s.updateEditing(msg)
	}
	switch msg.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j":
		if s.cursor < len(s.keys)-1 {
			s.cursor++
		}
	case "enter", "e":
		if len(s.keys) > 0 {
			entry := s.keys[s.cursor]
			s.editKey = entry.Key
			s.editTI.SetValue(entry.Value)
			s.editTI.CursorEnd()
			s.editTI.Focus()
			s.editing = true
			s.saveErr = ""
		}
	}
	return s, nil
}

func (s *swarmConfigSection) updateEditing(msg tea.KeyMsg) (SettingsSectionModel, []tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.editing = false
		s.editDirty = false
		s.saveErr = ""
	case "enter":
		s.editDirty = true
		cmd := s.Commit()
		s.editing = false
		return s, []tea.Cmd{cmd}
	default:
		var cmd tea.Cmd
		s.editTI, cmd = s.editTI.Update(msg)
		return s, []tea.Cmd{cmd}
	}
	return s, nil
}

func (s *swarmConfigSection) View(w, h int) string {
	if s.loading {
		return dimStyle.Render("  Loading config…")
	}
	if s.err != "" {
		return lipgloss.NewStyle().Foreground(colorRed).Render("  Error: " + s.err)
	}
	if globalConfigService == nil {
		return dimStyle.Render("  Config service not available (server mode only)")
	}

	if s.editing {
		return s.viewEditing(w)
	}

	var sb strings.Builder
	sb.WriteString(dimStyle.Render(fmt.Sprintf("  %d settings  ·  ↑/↓ navigate  e/Enter edit\n\n", len(s.keys))))

	// Group by prefix
	type group struct {
		prefix string
		keys   []configEntry
	}
	var groups []group
	groupMap := map[string]*group{}
	for _, e := range s.keys {
		prefix := e.Key
		if idx := strings.Index(e.Key, "."); idx >= 0 {
			prefix = e.Key[:idx]
		}
		if _, ok := groupMap[prefix]; !ok {
			groups = append(groups, group{prefix: prefix})
			groupMap[prefix] = &groups[len(groups)-1]
		}
		groupMap[prefix].keys = append(groupMap[prefix].keys, e)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].prefix < groups[j].prefix })

	maxRows := max(3, h-8)
	rowsShown := 0

	for gi, g := range groups {
		if rowsShown >= maxRows {
			break
		}
		if gi > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(lipgloss.NewStyle().Foreground(colorTeal).Render("  ["+g.prefix+"]") + "\n")

		for _, entry := range g.keys {
			if rowsShown >= maxRows {
				break
			}
			// Find the global cursor position for this entry
			var entryIdx int
			for idx, k := range s.keys {
				if k.Key == entry.Key {
					entryIdx = idx
					break
				}
			}

			cursor := "  "
			keyStyle := lipgloss.NewStyle().Foreground(colorText)
			if entryIdx == s.cursor {
				cursor = lipgloss.NewStyle().Foreground(colorTeal).Render("▶ ")
				keyStyle = lipgloss.NewStyle().Foreground(colorTeal).Bold(true)
			}

			// Scope badge
			scopeBadge := ""
			switch entry.Source {
			case scopeRuntime:
				scopeBadge = lipgloss.NewStyle().Foreground(colorOrange).Render("[rt]")
			case scopeDB:
				scopeBadge = lipgloss.NewStyle().Foreground(colorGreen).Render("[db]")
			case scopeEnv:
				scopeBadge = lipgloss.NewStyle().Foreground(colorYellow).Render("[env]")
			default:
				scopeBadge = dimStyle.Render("[def]")
			}

			meta, _ := configRegistry[entry.Key]
			desc := meta.Description
			valueStr := entry.Value
			if len(valueStr) > 18 {
				valueStr = valueStr[:18] + "…"
			}

			line := fmt.Sprintf("  %s  %s  %s  %-18s  %s",
				cursor,
				keyStyle.Render(fmt.Sprintf("%-32s", strings.TrimPrefix(entry.Key, g.prefix+"."))),
				scopeBadge,
				lipgloss.NewStyle().Foreground(colorTeal).Render(valueStr),
				dimStyle.Render(desc),
			)
			sb.WriteString(line + "\n")
			rowsShown++
		}
	}

	if s.saveErr != "" {
		sb.WriteString("\n" + lipgloss.NewStyle().Foreground(colorRed).Render("  ✗ "+s.saveErr))
	}
	sb.WriteString("\n" + dimStyle.Render("↑/↓ navigate  Enter/e edit  [def]=default  [db]=persisted  [env]=env var  [rt]=runtime"))
	return sb.String()
}

func (s *swarmConfigSection) viewEditing(w int) string {
	meta, _ := configRegistry[s.editKey]
	var sb strings.Builder
	sb.WriteString(lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("Edit: "+s.editKey) + "\n\n")
	sb.WriteString(dimStyle.Render(meta.Description) + "\n\n")

	// Show useful metadata
	if meta.EnvVar != "" {
		envVal := os.Getenv(meta.EnvVar)
		if envVal != "" {
			sb.WriteString(dimStyle.Render("  Env "+meta.EnvVar+"="+truncStr(envVal, 40)) + "\n")
		} else {
			sb.WriteString(dimStyle.Render("  Env "+meta.EnvVar+" (not set)") + "\n")
		}
	}
	sb.WriteString(dimStyle.Render("  Default: "+meta.Default) + "\n")
	if meta.DangerLevel >= 2 {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorRed).Render("  ⚠ Danger level 2 — affects fleet operation") + "\n")
	} else if meta.DangerLevel == 1 {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorOrange).Render("  ⚡ Danger level 1 — validate before saving") + "\n")
	}
	if meta.Restartable {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorYellow).Render("  ↩ Requires restart to take effect") + "\n")
	}
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("Value:") + "\n")
	sb.WriteString("  " + s.editTI.View() + "\n")
	if s.saveErr != "" {
		sb.WriteString("\n" + lipgloss.NewStyle().Foreground(colorRed).Render("  ✗ "+s.saveErr))
	}
	sb.WriteString("\n\n" + dimStyle.Render("Enter save  Esc cancel"))
	return sb.String()
}

// applySwarmConfigLoaded updates the section from a loaded message.
func applySwarmConfigLoaded(m *tuiModel, msg swarmConfigLoadedMsg) {
	if m.settings == nil {
		return
	}
	for _, sec := range m.settings.sections {
		if sc, ok := sec.(*swarmConfigSection); ok {
			sc.loading = false
			// Sort by key for stable display
			entries := msg.entries
			sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
			sc.keys = entries
			if sc.cursor >= len(sc.keys) {
				sc.cursor = max(0, len(sc.keys)-1)
			}
			return
		}
	}
}

// applySwarmConfigSaved updates the section after a Set completes.
func applySwarmConfigSaved(m *tuiModel, msg swarmConfigSavedMsg) {
	if m.settings == nil {
		return
	}
	for _, sec := range m.settings.sections {
		if sc, ok := sec.(*swarmConfigSection); ok {
			sc.editDirty = false
			if msg.err != nil {
				sc.saveErr = msg.err.Error()
			} else {
				sc.saveErr = ""
				// Reload all entries to reflect the saved value
				sc.loading = true
			}
			return
		}
	}
}

// ─── Section: Integrations ────────────────────────────────────────────────────
//
// Read-only view of integration status derived from env vars.
// Shows set/unset and a single-line health hint per integration.

type integrationsSection struct{}

func newIntegrationsSection() *integrationsSection { return &integrationsSection{} }
func (s *integrationsSection) Title() string        { return "Integrations" }
func (s *integrationsSection) IsDirty() bool        { return false }
func (s *integrationsSection) Commit() tea.Cmd      { return nil }
func (s *integrationsSection) Discard()              {}
func (s *integrationsSection) Init() tea.Cmd         { return nil }
func (s *integrationsSection) Update(msg tea.KeyMsg) (SettingsSectionModel, []tea.Cmd) {
	return s, nil
}

func (s *integrationsSection) View(w, h int) string {
	type integration struct {
		name    string
		vars    []string
		note    string
	}
	integrations := []integration{
		{
			name: "Plane",
			vars: []string{"PLANE_API_URL", "PLANE_API_KEY", "PLANE_WORKSPACE"},
			note: "Work queue, autopilot issue pull, CI poller",
		},
		{
			name: "Icinga",
			vars: []string{"ICINGA_URL", "ICINGA_USER", "ICINGA_PASS"},
			note: "Infrastructure monitoring, alert proxy, Icinga view (I key)",
		},
		{
			name: "Telegram",
			vars: []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID"},
			note: "Agent escalation routing, /ack command, webhook at POST /api/telegram/webhook",
		},
		{
			name: "SwarmOps Auth",
			vars: []string{"SWARMOPS_AUTH_TOKEN"},
			note: "Shared bearer token for API + TUI (optional; dev mode skips auth)",
		},
	}

	var sb strings.Builder
	sb.WriteString(dimStyle.Render("  Integration env var status (read-only — set in .env or systemd unit)\n\n"))

	for _, integ := range integrations {
		allSet := true
		anySet := false
		for _, v := range integ.vars {
			if os.Getenv(v) != "" {
				anySet = true
			} else {
				allSet = false
			}
		}

		var statusIcon string
		var nameStyle lipgloss.Style
		if allSet {
			statusIcon = lipgloss.NewStyle().Foreground(colorGreen).Render("✓")
			nameStyle = lipgloss.NewStyle().Foreground(colorGreen).Bold(true)
		} else if anySet {
			statusIcon = lipgloss.NewStyle().Foreground(colorOrange).Render("~")
			nameStyle = lipgloss.NewStyle().Foreground(colorOrange).Bold(true)
		} else {
			statusIcon = dimStyle.Render("—")
			nameStyle = lipgloss.NewStyle().Foreground(colorDim)
		}

		sb.WriteString(fmt.Sprintf("  %s  %s\n", statusIcon, nameStyle.Render(integ.name)))
		sb.WriteString(fmt.Sprintf("     %s\n", dimStyle.Render(integ.note)))

		for _, v := range integ.vars {
			val := os.Getenv(v)
			if val != "" {
				masked := maskSecret(val)
				sb.WriteString(fmt.Sprintf("     %s  %s\n",
					lipgloss.NewStyle().Foreground(colorGreen).Render("✓"),
					dimStyle.Render(v+"="+masked),
				))
			} else {
				sb.WriteString(fmt.Sprintf("     %s  %s\n",
					dimStyle.Render("—"),
					dimStyle.Render(v+" (not set)"),
				))
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString(dimStyle.Render("  To configure: edit ~/remote-code/.env or the swarmops.service unit"))
	return sb.String()
}

// maskSecret shows first 4 chars then asterisks, preserving total length awareness.
func maskSecret(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:4] + strings.Repeat("*", min(len(s)-4, 12))
}
