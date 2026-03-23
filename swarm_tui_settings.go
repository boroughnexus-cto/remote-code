package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)


// ─── Settings Overlay ─────────────────────────────────────────────────────────
//
// The Settings overlay opens with Esc from the sidebar (when no modal is open).
// It is a tab-based shell; each tab owns a SettingsSectionModel.
//
// Key bindings:
//   Tab / Shift+Tab   – switch between sections
//   Esc               – close (prompts save if dirty)
//   Section keys      – forwarded to the active section

// ─── Section interface ────────────────────────────────────────────────────────

// SettingsSectionModel is implemented by each settings tab. It owns its own
// state, draft values, and commit/discard logic.
type SettingsSectionModel interface {
	// Title returns the tab label.
	Title() string
	// Init is called when the section first becomes active.
	Init() tea.Cmd
	// Update handles a key message forwarded from the settings shell.
	Update(msg tea.KeyMsg) (SettingsSectionModel, []tea.Cmd)
	// View renders the section body (no border — shell draws the outer frame).
	View(w, h int) string
	// IsDirty returns true if there are unsaved changes.
	IsDirty() bool
	// Commit persists draft state. Called when user presses Enter on a Save action.
	Commit() tea.Cmd
	// Discard drops unsaved changes.
	Discard()
	// ConsumesEsc returns true when the section wants to handle Esc itself
	// (e.g., to exit a sub-edit mode) rather than letting the shell close.
	ConsumesEsc() bool
}

// ─── Shell model ──────────────────────────────────────────────────────────────

type tuiSettingsModel struct {
	sections []SettingsSectionModel
	active   int // index of active section
}

func newTUISettings(client TUIClient) *tuiSettingsModel {
	return &tuiSettingsModel{
		sections: []SettingsSectionModel{
			newPersonasSection(client),
			newSessionContextsSection(client),
			newSwarmConfigSection(),
			newIntegrationsSection(),
		},
	}
}

func (s *tuiSettingsModel) activeSection() SettingsSectionModel {
	if s.active >= 0 && s.active < len(s.sections) {
		return s.sections[s.active]
	}
	return nil
}

func (s *tuiSettingsModel) IsDirty() bool {
	for _, sec := range s.sections {
		if sec.IsDirty() {
			return true
		}
	}
	return false
}

// ─── Update (shell level) ─────────────────────────────────────────────────────

func (m tuiModel) updateSettings(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	st := m.settings
	if st == nil {
		return m, nil
	}
	var cmds []tea.Cmd

	switch msg.String() {
	case "esc", "q":
		// If the active section is in a sub-edit mode, let it handle Esc first.
		if sec := st.activeSection(); sec != nil && sec.ConsumesEsc() {
			newSec, secCmds := sec.Update(msg)
			st.sections[st.active] = newSec
			cmds = append(cmds, secCmds...)
			return m, cmds
		}
		m.settings = nil
		return m, nil

	case "tab":
		if len(st.sections) > 0 {
			st.active = (st.active + 1) % len(st.sections)
			cmds = append(cmds, st.sections[st.active].Init())
		}

	case "shift+tab":
		if len(st.sections) > 0 {
			st.active = (st.active - 1 + len(st.sections)) % len(st.sections)
			cmds = append(cmds, st.sections[st.active].Init())
		}

	default:
		if sec := st.activeSection(); sec != nil {
			newSec, secCmds := sec.Update(msg)
			st.sections[st.active] = newSec
			cmds = append(cmds, secCmds...)
		}
	}

	return m, cmds
}

// ─── View (shell level) ───────────────────────────────────────────────────────

func (m tuiModel) viewSettings() string {
	st := m.settings
	if st == nil {
		return ""
	}
	w := m.w
	h := m.h

	// ── Tab bar ───────────────────────────────────────────────────────────────
	var tabParts []string
	for i, sec := range st.sections {
		label := sec.Title()
		if sec.IsDirty() {
			label += " •"
		}
		if i == st.active {
			tabParts = append(tabParts, lipgloss.NewStyle().
				Foreground(colorTeal).Bold(true).
				Underline(true).
				Render(" "+label+" "))
		} else {
			tabParts = append(tabParts, lipgloss.NewStyle().
				Foreground(colorDim).
				Render(" "+label+" "))
		}
	}
	tabBar := strings.Join(tabParts, dimStyle.Render("│"))

	// ── Section body ──────────────────────────────────────────────────────────
	innerW := min(w-4, 88)
	innerH := h - 8 // tabs + title + border + footer
	if innerH < 5 {
		innerH = 5
	}

	var body string
	if sec := st.activeSection(); sec != nil {
		body = sec.View(innerW, innerH)
	}

	// ── Footer ────────────────────────────────────────────────────────────────
	footer := dimStyle.Render("Tab next section  Shift+Tab prev  Esc close")

	// ── Assemble ──────────────────────────────────────────────────────────────
	title := lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("⚙ Settings")
	content := title + "\n\n" + tabBar + "\n\n" + body + "\n\n" + footer

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorTeal).
		Padding(1, 2).
		Width(innerW).
		Render(content)

	padTop := max(0, (h-lipgloss.Height(box))/2)
	padLeft := max(0, (w-lipgloss.Width(box))/2)
	top := strings.Repeat("\n", padTop)
	indent := strings.Repeat(" ", padLeft)
	rows := strings.Split(box, "\n")
	for i, r := range rows {
		rows[i] = indent + r
	}
	return top + strings.Join(rows, "\n")
}

// ─── Section: Agent Personas ──────────────────────────────────────────────────

type personasSection struct {
	client  TUIClient
	items   []personaItem
	cursor  int
	loading bool
	err     string

	// Edit state (existing or new persona)
	editing      bool
	editIsNew    bool            // true when creating a new persona
	editRole     string          // role being edited (empty for new)
	roleInput    textinput.Model // role name input (new personas only)
	promptTA     textarea.Model  // prompt text editor
	newFocusRole bool            // true = focus on roleInput (new flow only)

	// Delete confirm
	confirmDelete bool
	deleteTarget  string

	// Pending: auto-open this role for editing once items finish loading
	pendingRole string
}

type personaItem struct {
	role    string
	prompt  string
	version int
}

// personasLoadedMsg carries persona data from the DB fetch command.
type personasLoadedMsg struct {
	items []personaItem
	err   error
}

func newPersonasSection(client TUIClient) *personasSection {
	ri := textinput.New()
	ri.CharLimit = 50
	ri.Placeholder = "role name (e.g. worker, architect)"

	ta := textarea.New()
	ta.Placeholder = "Enter the system prompt for this role…"
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.SetWidth(76)
	ta.SetHeight(15)

	return &personasSection{client: client, loading: true, roleInput: ri, promptTA: ta}
}

func (s *personasSection) Title() string   { return "Agent Personas" }
func (s *personasSection) IsDirty() bool   { return s.editing }
func (s *personasSection) Commit() tea.Cmd { return nil } // handled internally via doSave
func (s *personasSection) Discard() {
	s.editing = false
	s.confirmDelete = false
	s.promptTA.Blur()
	s.roleInput.Blur()
}
func (s *personasSection) ConsumesEsc() bool { return s.editing || s.confirmDelete }

func (s *personasSection) startEdit(item personaItem) {
	s.editRole = item.role
	s.editIsNew = false
	s.newFocusRole = false
	s.promptTA.SetValue(item.prompt)
	s.promptTA.Focus()
	s.editing = true
}

func (s *personasSection) startNew() {
	s.editRole = ""
	s.editIsNew = true
	s.newFocusRole = true
	s.roleInput.SetValue("")
	s.roleInput.Focus()
	s.promptTA.SetValue("")
	s.promptTA.Blur()
	s.editing = true
}

func (s *personasSection) Init() tea.Cmd {
	s.loading = true
	client := s.client
	return func() tea.Msg {
		b, err := client.getSync("/api/swarm/role-prompts")
		if err != nil {
			return personasLoadedMsg{err: err}
		}
		var raw []struct {
			Role    string `json:"role"`
			Prompt  string `json:"prompt"`
			Version int    `json:"version"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			return personasLoadedMsg{err: err}
		}
		items := make([]personaItem, 0, len(raw))
		for _, r := range raw {
			if strings.HasPrefix(r.Role, "_") {
				continue
			}
			items = append(items, personaItem{role: r.Role, prompt: r.Prompt, version: r.Version})
		}
		return personasLoadedMsg{items: items}
	}
}

func (s *personasSection) Update(msg tea.KeyMsg) (SettingsSectionModel, []tea.Cmd) {
	if s.editing {
		return s.updateEditing(msg)
	}
	if s.confirmDelete {
		return s.updateConfirmDelete(msg)
	}
	switch msg.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j":
		if s.cursor < len(s.items)-1 {
			s.cursor++
		}
	case "e", "enter":
		if s.cursor < len(s.items) {
			s.startEdit(s.items[s.cursor])
		}
	case "n":
		s.startNew()
	case "d", "delete":
		if s.cursor < len(s.items) {
			s.confirmDelete = true
			s.deleteTarget = s.items[s.cursor].role
		}
	}
	return s, nil
}

func (s *personasSection) updateEditing(msg tea.KeyMsg) (SettingsSectionModel, []tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.editing = false
		s.promptTA.Blur()
		s.roleInput.Blur()
	case "ctrl+s":
		return s.doSave()
	case "tab":
		if s.editIsNew && s.newFocusRole {
			s.roleInput.Blur()
			s.newFocusRole = false
			s.promptTA.Focus()
		}
	default:
		if s.editIsNew && s.newFocusRole {
			var cmd tea.Cmd
			s.roleInput, cmd = s.roleInput.Update(msg)
			return s, []tea.Cmd{cmd}
		}
		var cmd tea.Cmd
		s.promptTA, cmd = s.promptTA.Update(msg)
		return s, []tea.Cmd{cmd}
	}
	return s, nil
}

func (s *personasSection) doSave() (SettingsSectionModel, []tea.Cmd) {
	role := s.editRole
	if s.editIsNew {
		role = strings.TrimSpace(s.roleInput.Value())
	}
	if role == "" {
		return s, nil // can't save without a role name
	}
	prompt := s.promptTA.Value()
	s.editing = false
	s.promptTA.Blur()
	s.roleInput.Blur()
	client := s.client
	return s, []tea.Cmd{func() tea.Msg {
		body, _ := json.Marshal(map[string]string{"prompt": prompt})
		if err := client.putSync("/api/swarm/role-prompts/"+role, body); err != nil {
			return personaSavedMsg{role: role, err: err}
		}
		return personaSavedMsg{role: role}
	}}
}

func (s *personasSection) updateConfirmDelete(msg tea.KeyMsg) (SettingsSectionModel, []tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		role := s.deleteTarget
		s.confirmDelete = false
		s.deleteTarget = ""
		client := s.client
		return s, []tea.Cmd{func() tea.Msg {
			if err := client.deleteSync("/api/swarm/role-prompts/" + role); err != nil {
				return personaDeletedMsg{role: role, err: err}
			}
			return personaDeletedMsg{role: role}
		}}
	default:
		s.confirmDelete = false
		s.deleteTarget = ""
	}
	return s, nil
}

func (s *personasSection) View(w, h int) string {
	if s.loading {
		return dimStyle.Render("  Loading personas…")
	}
	if s.err != "" {
		return lipgloss.NewStyle().Foreground(colorRed).Render("  Error: " + s.err)
	}
	if s.editing {
		s.promptTA.SetWidth(w - 4)
		s.promptTA.SetHeight(max(5, h-10))
		return s.viewEditing()
	}
	if s.confirmDelete {
		return s.viewConfirmDelete()
	}
	if len(s.items) == 0 {
		return dimStyle.Render("  No personas defined.") +
			"\n\n" + dimStyle.Render("  Press 'n' to create a new persona.") +
			"\n\n" + dimStyle.Render("  n new  (changes take effect immediately)")
	}

	var sb strings.Builder
	sb.WriteString(dimStyle.Render(fmt.Sprintf("  %d personas  ·  n new  e edit  d delete  ↑/↓ navigate\n\n", len(s.items))))

	maxRows := max(3, h-6)
	start := 0
	if s.cursor >= maxRows {
		start = s.cursor - maxRows + 1
	}
	end := min(start+maxRows, len(s.items))

	for i := start; i < end; i++ {
		p := s.items[i]
		cursor := "  "
		nameStyle := lipgloss.NewStyle().Foreground(colorText)
		if i == s.cursor {
			cursor = lipgloss.NewStyle().Foreground(colorTeal).Render("▶ ")
			nameStyle = lipgloss.NewStyle().Foreground(colorTeal).Bold(true)
		}
		promptPreview := strings.ReplaceAll(strings.TrimSpace(p.prompt), "\n", " ")
		if len(promptPreview) > w-28 {
			promptPreview = promptPreview[:w-28] + "…"
		}
		line := cursor +
			nameStyle.Render(fmt.Sprintf("%-20s", p.role)) + "  " +
			dimStyle.Render(fmt.Sprintf("v%-3d  ", p.version)) +
			dimStyle.Render(promptPreview)
		sb.WriteString(line + "\n")
	}

	sb.WriteString("\n" + dimStyle.Render("↑/↓ navigate  e/Enter edit  n new  d delete"))
	return sb.String()
}

func (s *personasSection) viewEditing() string {
	var sb strings.Builder
	if s.editIsNew {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("New Persona") + "\n\n")
		cursor := "  "
		if s.newFocusRole {
			cursor = lipgloss.NewStyle().Foreground(colorTeal).Render("▶ ")
		}
		sb.WriteString(cursor + dimStyle.Render("Role name:") + "\n")
		sb.WriteString("  " + s.roleInput.View() + "\n\n")
	} else {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("Edit: "+s.editRole) + "\n\n")
	}
	sb.WriteString(dimStyle.Render("Prompt:") + "\n")
	sb.WriteString(s.promptTA.View() + "\n\n")

	if s.editIsNew && s.newFocusRole {
		sb.WriteString(dimStyle.Render("Tab switch to prompt  Esc cancel"))
	} else {
		sb.WriteString(dimStyle.Render("Ctrl+S save  Esc cancel"))
	}
	return sb.String()
}

func (s *personasSection) viewConfirmDelete() string {
	return lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render("Delete persona: "+s.deleteTarget+"?") +
		"\n\n" + dimStyle.Render("y confirm  n/Esc cancel")
}

// applyPersonasLoaded is called from the main Update loop to push loaded data
// into the settings persona section.
func applyPersonasLoaded(m *tuiModel, msg personasLoadedMsg) {
	if m.settings == nil {
		return
	}
	for _, sec := range m.settings.sections {
		if ps, ok := sec.(*personasSection); ok {
			ps.loading = false
			if msg.err != nil {
				ps.err = msg.err.Error()
			} else {
				ps.items = msg.items
				ps.err = ""
				// Auto-open edit for a pending role (triggered by sidebar P key).
				if ps.pendingRole != "" {
					for i, item := range ps.items {
						if item.role == ps.pendingRole {
							ps.cursor = i
							ps.startEdit(item)
							break
						}
					}
					ps.pendingRole = ""
				}
			}
			return
		}
	}
}

// ─── Section: Session Contexts ────────────────────────────────────────────────

type sessionContext struct {
	id             string
	name           string
	description    string
	summary        string
	content        string
	dynamicContext string
	tags           string
}

type sessionContextsSection struct {
	client  TUIClient
	items   []sessionContext
	cursor  int
	loading bool
	err     string

	// Metadata edit state
	editing   bool
	editIdx   int // index in items being edited (-1 for new)
	editField int // 0=name,1=description,2=summary,3=tags
	fields    [4]textinput.Model
	draft     sessionContext
	dirty     bool

	// Large-text editing (content and dynamic_context)
	contentTA      textarea.Model
	dynamicTA      textarea.Model
	editingContent bool
	editingDynamic bool
}

type sessionContextsLoadedMsg struct {
	items []sessionContext
	err   error
}

type sessionContextSavedMsg struct {
	id  string
	err error
}

func newSessionContextsSection(client TUIClient) *sessionContextsSection {
	s := &sessionContextsSection{client: client, loading: true}
	for i := range s.fields {
		ti := textinput.New()
		ti.CharLimit = 300
		s.fields[i] = ti
	}
	s.fields[0].Placeholder = "context name"
	s.fields[1].Placeholder = "one-line description"
	s.fields[2].Placeholder = "1-2 line summary shown in picker and right panel"
	s.fields[3].Placeholder = "tags (comma-separated)"

	s.contentTA = textarea.New()
	s.contentTA.Placeholder = "Static context content (markdown, injected verbatim into agent prompts)…"
	s.contentTA.CharLimit = 0
	s.contentTA.ShowLineNumbers = false
	s.contentTA.SetWidth(76)
	s.contentTA.SetHeight(20)

	s.dynamicTA = textarea.New()
	s.dynamicTA.Placeholder = "Dynamic context instructions (shell command or template to generate context at spawn)…"
	s.dynamicTA.CharLimit = 0
	s.dynamicTA.ShowLineNumbers = false
	s.dynamicTA.SetWidth(76)
	s.dynamicTA.SetHeight(20)

	return s
}

func (s *sessionContextsSection) Title() string { return "Session Contexts" }
func (s *sessionContextsSection) IsDirty() bool { return s.dirty }
func (s *sessionContextsSection) ConsumesEsc() bool {
	return s.editing || s.editingContent || s.editingDynamic
}
func (s *sessionContextsSection) Discard() {
	s.dirty = false
	s.editing = false
	s.editingContent = false
	s.editingDynamic = false
}

func (s *sessionContextsSection) Init() tea.Cmd {
	s.loading = true
	client := s.client
	return func() tea.Msg {
		b, err := client.getSync("/api/swarm/contexts")
		if err != nil {
			return sessionContextsLoadedMsg{err: err}
		}
		var raw []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			Description    string `json:"description"`
			Summary        string `json:"summary"`
			Content        string `json:"content"`
			DynamicContext string `json:"dynamic_context"`
			Tags           string `json:"tags"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			return sessionContextsLoadedMsg{err: err}
		}
		items := make([]sessionContext, 0, len(raw))
		for _, r := range raw {
			items = append(items, sessionContext{
				id: r.ID, name: r.Name, description: r.Description,
				summary: r.Summary, content: r.Content,
				dynamicContext: r.DynamicContext, tags: r.Tags,
			})
		}
		return sessionContextsLoadedMsg{items: items}
	}
}

func (s *sessionContextsSection) Commit() tea.Cmd {
	sc := s.draft
	client := s.client
	return func() tea.Msg {
		body, _ := json.Marshal(map[string]string{
			"name": sc.name, "description": sc.description,
			"summary": sc.summary, "content": sc.content,
			"dynamic_context": sc.dynamicContext, "tags": sc.tags,
		})
		if sc.id == "" {
			// Create
			resp, err := client.postSync("/api/swarm/contexts", body)
			if err != nil {
				return sessionContextSavedMsg{err: err}
			}
			var result struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(resp, &result); err != nil {
				return sessionContextSavedMsg{err: err}
			}
			return sessionContextSavedMsg{id: result.ID}
		}
		// Update
		if err := client.putSync("/api/swarm/contexts/"+sc.id, body); err != nil {
			return sessionContextSavedMsg{err: err}
		}
		return sessionContextSavedMsg{id: sc.id}
	}
}

func (s *sessionContextsSection) Update(msg tea.KeyMsg) (SettingsSectionModel, []tea.Cmd) {
	if s.editing {
		return s.updateEditing(msg)
	}
	switch msg.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j":
		if s.cursor < len(s.items)-1 {
			s.cursor++
		}
	case "n":
		// New context — open edit mode with blank draft
		s.draft = sessionContext{}
		s.editIdx = -1
		s.fields[0].SetValue("")
		s.fields[1].SetValue("")
		s.fields[2].SetValue("")
		s.fields[3].SetValue("")
		s.fields[0].Focus()
		s.editField = 0
		s.editing = true
	case "e", "enter":
		if len(s.items) > 0 {
			sc := s.items[s.cursor]
			s.draft = sc
			s.editIdx = s.cursor
			s.fields[0].SetValue(sc.name)
			s.fields[1].SetValue(sc.description)
			s.fields[2].SetValue(sc.summary)
			s.fields[3].SetValue(sc.tags)
			s.fields[0].Focus()
			s.editField = 0
			s.editing = true
		}
	case "d", "delete":
		if len(s.items) > 0 {
			id := s.items[s.cursor].id
			client := s.client
			return s, []tea.Cmd{func() tea.Msg {
				if err := client.deleteSync("/api/swarm/contexts/" + id); err != nil {
					return tuiErrMsg{op: "delete-context", text: err.Error()}
				}
				return tuiDoneMsg{op: "delete-context"}
			}}
		}
	}
	return s, nil
}

func (s *sessionContextsSection) updateEditing(msg tea.KeyMsg) (SettingsSectionModel, []tea.Cmd) {
	// Nested content/dynamic textarea editing — intercept all keys.
	if s.editingContent || s.editingDynamic {
		return s.updateContentEdit(msg)
	}

	switch msg.String() {
	case "esc":
		s.editing = false
		for i := range s.fields {
			s.fields[i].Blur()
		}
	case "tab", "down":
		s.fields[s.editField].Blur()
		s.editField = (s.editField + 1) % len(s.fields)
		s.fields[s.editField].Focus()
	case "shift+tab", "up":
		s.fields[s.editField].Blur()
		s.editField = (s.editField - 1 + len(s.fields)) % len(s.fields)
		s.fields[s.editField].Focus()
	case "ctrl+e":
		// Open static content in the in-TUI textarea editor.
		if s.draft.id == "" {
			break // save metadata first
		}
		s.contentTA.SetWidth(76)
		s.contentTA.SetHeight(20)
		s.contentTA.SetValue(s.draft.content)
		s.contentTA.Focus()
		s.editingContent = true
	case "ctrl+d":
		// Open dynamic_context in the in-TUI textarea editor.
		if s.draft.id == "" {
			break // save metadata first
		}
		s.dynamicTA.SetWidth(76)
		s.dynamicTA.SetHeight(20)
		s.dynamicTA.SetValue(s.draft.dynamicContext)
		s.dynamicTA.Focus()
		s.editingDynamic = true
	case "enter":
		if s.editField < len(s.fields)-1 {
			s.fields[s.editField].Blur()
			s.editField++
			s.fields[s.editField].Focus()
		} else {
			// Save: update draft from fields
			s.draft.name = strings.TrimSpace(s.fields[0].Value())
			s.draft.description = strings.TrimSpace(s.fields[1].Value())
			s.draft.summary = strings.TrimSpace(s.fields[2].Value())
			s.draft.tags = strings.TrimSpace(s.fields[3].Value())
			if s.draft.name != "" {
				s.dirty = true
				cmd := s.Commit()
				s.editing = false
				return s, []tea.Cmd{cmd}
			}
		}
	default:
		var cmd tea.Cmd
		s.fields[s.editField], cmd = s.fields[s.editField].Update(msg)
		return s, []tea.Cmd{cmd}
	}
	return s, nil
}

// updateContentEdit handles keys while editing content or dynamic_context in a textarea.
func (s *sessionContextsSection) updateContentEdit(msg tea.KeyMsg) (SettingsSectionModel, []tea.Cmd) {
	switch msg.String() {
	case "ctrl+s":
		if s.editingContent {
			s.draft.content = s.contentTA.Value()
			s.contentTA.Blur()
			s.editingContent = false
		} else {
			s.draft.dynamicContext = s.dynamicTA.Value()
			s.dynamicTA.Blur()
			s.editingDynamic = false
		}
		s.dirty = true
		cmd := s.Commit()
		return s, []tea.Cmd{cmd}
	case "esc":
		s.contentTA.Blur()
		s.dynamicTA.Blur()
		s.editingContent = false
		s.editingDynamic = false
	default:
		var cmd tea.Cmd
		if s.editingContent {
			s.contentTA, cmd = s.contentTA.Update(msg)
		} else {
			s.dynamicTA, cmd = s.dynamicTA.Update(msg)
		}
		return s, []tea.Cmd{cmd}
	}
	return s, nil
}

func (s *sessionContextsSection) View(w, h int) string {
	if s.loading {
		return dimStyle.Render("  Loading session contexts…")
	}
	if s.err != "" {
		return lipgloss.NewStyle().Foreground(colorRed).Render("  Error: " + s.err)
	}

	if s.editing {
		return s.viewEditing(w)
	}

	var sb strings.Builder
	if len(s.items) == 0 {
		sb.WriteString(dimStyle.Render("  No session contexts defined yet.") + "\n\n")
		sb.WriteString(dimStyle.Render("  Session contexts inject domain-specific instructions into agents at spawn.") + "\n")
		sb.WriteString(dimStyle.Render("  Press 'n' to create your first context.") + "\n")
	} else {
		sb.WriteString(dimStyle.Render(fmt.Sprintf("  %d contexts  ·  n new  e edit  ↑/↓ navigate\n\n", len(s.items))))

		maxRows := max(3, h-8)
		start := 0
		if s.cursor >= maxRows {
			start = s.cursor - maxRows + 1
		}
		end := min(start+maxRows, len(s.items))

		for i := start; i < end; i++ {
			sc := s.items[i]
			cursor := "  "
			nameStyle := lipgloss.NewStyle().Foreground(colorText)
			if i == s.cursor {
				cursor = lipgloss.NewStyle().Foreground(colorTeal).Render("▶ ")
				nameStyle = lipgloss.NewStyle().Foreground(colorTeal).Bold(true)
			}
			desc := sc.description
			if desc == "" {
				desc = dimStyle.Render("(no description)")
			}
			if len(desc) > w-28 {
				desc = desc[:w-28] + "…"
			}
			sb.WriteString(cursor + nameStyle.Render(fmt.Sprintf("%-20s", sc.name)) +
				"  " + dimStyle.Render(desc) + "\n")
			if sc.summary != "" {
				sumLine := sc.summary
				if len(sumLine) > w-6 {
					sumLine = sumLine[:w-6] + "…"
				}
				sb.WriteString("    " + dimStyle.Render(sumLine) + "\n")
			}
			if sc.dynamicContext != "" {
				sb.WriteString("    " + lipgloss.NewStyle().Foreground(colorOrange).Render("⚡ dynamic context") + "\n")
			}
			if sc.tags != "" {
				sb.WriteString("    " + dimStyle.Render("tags: "+sc.tags) + "\n")
			}
		}
	}

	sb.WriteString("\n" + dimStyle.Render("n new  e/Enter edit  d delete  ↑/↓ navigate"))
	return sb.String()
}

func (s *sessionContextsSection) viewEditing(w int) string {
	// If editing content or dynamic_context, show the full textarea.
	if s.editingContent || s.editingDynamic {
		return s.viewContentEdit(w)
	}

	var sb strings.Builder
	isNew := s.draft.id == ""
	if isNew {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("New Session Context") + "\n\n")
	} else {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("Edit: "+s.draft.name) + "\n\n")
	}

	labels := []string{"Name", "Description", "Summary", "Tags"}
	for i, f := range s.fields {
		cursor := "  "
		if i == s.editField {
			cursor = lipgloss.NewStyle().Foreground(colorTeal).Render("▶ ")
		}
		sb.WriteString(cursor + dimStyle.Render(labels[i]+":") + "\n")
		sb.WriteString("  " + f.View() + "\n\n")
	}

	if s.draft.id != "" {
		sb.WriteString(dimStyle.Render("Ctrl+E  edit static content  ·  Ctrl+D  edit dynamic context") + "\n\n")
	} else {
		sb.WriteString(dimStyle.Render("Save metadata first, then Ctrl+E/Ctrl+D to edit content") + "\n\n")
	}
	sb.WriteString(dimStyle.Render("Tab/↑↓ next field  Enter save (on last field)  Esc cancel"))
	return sb.String()
}

func (s *sessionContextsSection) viewContentEdit(w int) string {
	var title string
	var ta *textarea.Model
	if s.editingContent {
		title = "Edit Static Content"
		ta = &s.contentTA
	} else {
		title = "Edit Dynamic Context"
		ta = &s.dynamicTA
	}
	ta.SetWidth(w - 4)
	ta.SetHeight(20)

	var sb strings.Builder
	sb.WriteString(lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render(title) + "\n\n")
	sb.WriteString(ta.View() + "\n\n")
	sb.WriteString(dimStyle.Render("Ctrl+S save  Esc cancel (unsaved changes discarded)"))
	return sb.String()
}

// applySessionContextsLoaded is called from the main Update loop.
func applySessionContextsLoaded(m *tuiModel, msg sessionContextsLoadedMsg) {
	if m.settings == nil {
		return
	}
	for _, sec := range m.settings.sections {
		if sc, ok := sec.(*sessionContextsSection); ok {
			sc.loading = false
			if msg.err != nil {
				sc.err = msg.err.Error()
			} else {
				sc.items = msg.items
				sc.err = ""
			}
			return
		}
	}
}

func applySessionContextSaved(m *tuiModel, msg sessionContextSavedMsg) {
	if m.settings == nil {
		return
	}
	for _, sec := range m.settings.sections {
		if sc, ok := sec.(*sessionContextsSection); ok {
			sc.dirty = false
			if msg.err != nil {
				sc.err = msg.err.Error()
			} else {
				sc.loading = true // caller must dispatch sc.Init() to complete the reload
			}
			return
		}
	}
}

// ─── HTTP endpoint for session contexts ───────────────────────────────────────

// handleSessionContextsAPI serves GET/POST /api/swarm/contexts
// and GET/PUT/DELETE /api/swarm/contexts/{id}
func handleSessionContextsAPI(w http.ResponseWriter, r *http.Request, pathParts []string) {
	ctx := r.Context()

	if len(pathParts) == 0 {
		switch r.Method {
		case http.MethodGet:
			rows, err := database.QueryContext(ctx,
				"SELECT id, name, description, summary, content, dynamic_context, tags, created_at, updated_at FROM session_contexts ORDER BY name")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer rows.Close()
			var result []map[string]interface{}
			for rows.Next() {
				var id, name, content string
				var desc, summary, dynCtx, tags sql.NullString
				var createdAt, updatedAt int64
				if err := rows.Scan(&id, &name, &desc, &summary, &content, &dynCtx, &tags, &createdAt, &updatedAt); err != nil {
					continue
				}
				result = append(result, map[string]interface{}{
					"id": id, "name": name, "description": desc.String,
					"summary": summary.String, "content": content,
					"dynamic_context": dynCtx.String, "tags": tags.String,
					"created_at": createdAt, "updated_at": updatedAt,
				})
			}
			if result == nil {
				result = []map[string]interface{}{}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(result) //nolint:errcheck

		case http.MethodPost:
			var body struct {
				Name           string `json:"name"`
				Description    string `json:"description"`
				Summary        string `json:"summary"`
				Content        string `json:"content"`
				DynamicContext string `json:"dynamic_context"`
				Tags           string `json:"tags"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
				http.Error(w, "name required", http.StatusBadRequest)
				return
			}
			var id string
			err := database.QueryRowContext(ctx,
				`INSERT INTO session_contexts (name, description, summary, content, dynamic_context, tags)
				 VALUES (?, ?, ?, ?, ?, ?) RETURNING id`,
				body.Name, body.Description, body.Summary, body.Content, body.DynamicContext, body.Tags,
			).Scan(&id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"id": id}) //nolint:errcheck
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	// /api/swarm/contexts/{id}
	id := pathParts[0]
	switch r.Method {
	case http.MethodGet:
		var sc sessionContext
		var desc, summary, dynCtx, tags sql.NullString
		err := database.QueryRowContext(ctx,
			"SELECT id, name, description, summary, content, dynamic_context, tags FROM session_contexts WHERE id=?", id,
		).Scan(&sc.id, &sc.name, &desc, &summary, &sc.content, &dynCtx, &tags)
		if err == sql.ErrNoRows {
			http.Error(w, "not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sc.description = desc.String
		sc.summary = summary.String
		sc.dynamicContext = dynCtx.String
		sc.tags = tags.String
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id": sc.id, "name": sc.name, "description": sc.description,
			"summary": sc.summary, "content": sc.content,
			"dynamic_context": sc.dynamicContext, "tags": sc.tags,
		}) //nolint:errcheck

	case http.MethodPut:
		var body struct {
			Name           string `json:"name"`
			Description    string `json:"description"`
			Summary        string `json:"summary"`
			Content        string `json:"content"`
			DynamicContext string `json:"dynamic_context"`
			Tags           string `json:"tags"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, err := database.ExecContext(ctx,
			`UPDATE session_contexts SET name=?, description=?, summary=?, content=?, dynamic_context=?, tags=?, updated_at=unixepoch()
			 WHERE id=?`,
			body.Name, body.Description, body.Summary, body.Content, body.DynamicContext, body.Tags, id,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodDelete:
		database.ExecContext(ctx, "DELETE FROM session_contexts WHERE id=?", id) //nolint:errcheck
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
