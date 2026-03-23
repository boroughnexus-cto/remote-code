package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

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
	return &personasSection{client: client, loading: true}
}
func (s *personasSection) Title() string   { return "Agent Personas" }
func (s *personasSection) IsDirty() bool   { return false }
func (s *personasSection) Commit() tea.Cmd { return nil }
func (s *personasSection) Discard()        {}

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
			items = append(items, personaItem{role: r.Role, prompt: r.Prompt, version: r.Version})
		}
		return personasLoadedMsg{items: items}
	}
}

func (s *personasSection) Update(msg tea.KeyMsg) (SettingsSectionModel, []tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j":
		if s.cursor < len(s.items)-1 {
			s.cursor++
		}
	case "e":
		if s.cursor < len(s.items) {
			item := s.items[s.cursor]
			roleCapture := item.role
			promptCapture := item.prompt
			cmd := func() tea.Msg {
				f, err := os.CreateTemp("", "swarmops-prompt-*.md")
				if err != nil {
					return tuiErrMsg{op: "role-prompts", text: err.Error()}
				}
				tmpPath := f.Name()
				f.WriteString(promptCapture) //nolint:errcheck
				f.Close()
				editorBin, editorArgs := resolveEditor()
				return tuiRolePromptEditMsg{role: roleCapture, tmpPath: tmpPath, editor: editorBin, editorArgs: editorArgs}
			}
			return s, []tea.Cmd{cmd}
		}
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
	if len(s.items) == 0 {
		return dimStyle.Render("  No personas defined.") +
			"\n\n" + dimStyle.Render("  Add roles via the API: PUT /api/swarm/role-prompts/{role}")
	}

	var sb strings.Builder
	sb.WriteString(dimStyle.Render(fmt.Sprintf("  %d personas  ·  press 'e' on selected to edit in $EDITOR\n\n", len(s.items))))

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

	sb.WriteString("\n" + dimStyle.Render("↑/↓ navigate  e edit in $EDITOR  (changes take effect immediately)"))
	return sb.String()
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

	// Edit state
	editing   bool
	editIdx   int // index in items being edited
	editField int // 0=name,1=description,2=summary,3=tags
	fields    [4]textinput.Model
	draft     sessionContext
	dirty     bool
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
	return s
}

func (s *sessionContextsSection) Title() string { return "Session Contexts" }
func (s *sessionContextsSection) IsDirty() bool { return s.dirty }
func (s *sessionContextsSection) Discard() {
	s.dirty = false
	s.editing = false
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
	switch msg.String() {
	case "esc":
		s.editing = false
	case "tab", "down":
		s.fields[s.editField].Blur()
		s.editField = (s.editField + 1) % len(s.fields)
		s.fields[s.editField].Focus()
	case "shift+tab", "up":
		s.fields[s.editField].Blur()
		s.editField = (s.editField - 1 + len(s.fields)) % len(s.fields)
		s.fields[s.editField].Focus()
	case "ctrl+e":
		// Open static content in $EDITOR — only for existing (saved) contexts
		if s.draft.id == "" {
			break // must save metadata first
		}
		f, err := os.CreateTemp("", "swarm-ctx-*.md")
		if err != nil {
			break
		}
		f.WriteString(s.draft.content) //nolint:errcheck
		f.Close()
		tmpPath := f.Name()
		ctxID := s.draft.id
		ctxName := strings.TrimSpace(s.fields[0].Value())
		ctxDesc := strings.TrimSpace(s.fields[1].Value())
		ctxSummary := strings.TrimSpace(s.fields[2].Value())
		ctxTags := strings.TrimSpace(s.fields[3].Value())
		if ctxName == "" {
			ctxName = s.draft.name
		}
		editorBin, editorArgs := resolveEditor()
		return s, []tea.Cmd{func() tea.Msg {
			return tuiCtxContentEditMsg{
				ctxID:      ctxID,
				ctxName:    ctxName,
				ctxDesc:    ctxDesc,
				ctxSummary: ctxSummary,
				ctxTags:    ctxTags,
				tmpPath:    tmpPath,
				editor:     editorBin,
				editorArgs: editorArgs,
			}
		}}

	case "ctrl+d":
		// Open dynamic_context in $EDITOR — only for existing (saved) contexts
		if s.draft.id == "" {
			break // must save metadata first
		}
		f, err := os.CreateTemp("", "swarm-ctx-dyn-*.md")
		if err != nil {
			break
		}
		f.WriteString(s.draft.dynamicContext) //nolint:errcheck
		f.Close()
		tmpPath := f.Name()
		ctxID := s.draft.id
		ctxName := strings.TrimSpace(s.fields[0].Value())
		ctxDesc := strings.TrimSpace(s.fields[1].Value())
		ctxSummary := strings.TrimSpace(s.fields[2].Value())
		ctxTags := strings.TrimSpace(s.fields[3].Value())
		if ctxName == "" {
			ctxName = s.draft.name
		}
		editorBin, editorArgs := resolveEditor()
		return s, []tea.Cmd{func() tea.Msg {
			return tuiCtxDynamicEditMsg{
				ctxID:      ctxID,
				ctxName:    ctxName,
				ctxDesc:    ctxDesc,
				ctxSummary: ctxSummary,
				ctxTags:    ctxTags,
				tmpPath:    tmpPath,
				editor:     editorBin,
				editorArgs: editorArgs,
			}
		}}

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
		sb.WriteString(dimStyle.Render("Ctrl+E  edit static content in $EDITOR") + "\n")
		sb.WriteString(dimStyle.Render("Ctrl+D  edit dynamic context in $EDITOR") + "\n\n")
	} else {
		sb.WriteString(dimStyle.Render("Save metadata first, then Ctrl+E/Ctrl+D to edit content") + "\n\n")
	}
	sb.WriteString(dimStyle.Render("Tab/↑↓ next field  Enter save (on last field)  Esc cancel"))
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
