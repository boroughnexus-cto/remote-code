package main

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type tuiModalKind int

const (
	tuiModalNone tuiModalKind = iota
	tuiModalNewSession
	tuiModalNewAgent
	tuiModalNewTask
	tuiModalQuickAgent   // fast 1-field worker spawn
	tuiModalEditSession  // rename session
	tuiModalEditAgent    // edit agent name/mission/project/repo
	tuiModalEditTask     // edit task title/description/project
	tuiModalConfirmTyped   // destructive action requiring user to type name to confirm
	tuiModalInjectAgent   // direct instruction to a specific agent
	tuiModalEditTaskStage // manually set task stage
	tuiModalAddNote       // add a note to an agent's memory
)

// pendingConfirmAction holds state for a two-press confirmation. The first key
// press sets this; a second matching key executes onConfirm; any other key
// (including esc) clears it.
type pendingConfirmAction struct {
	label      string          // displayed in flash bar
	confirmKey string          // key that executes on second press
	targetItem *tuiSidebarItem // item being acted on
	onConfirm  tea.Cmd         // command to run on confirmation
}

// ─── Modal ────────────────────────────────────────────────────────────────────

type tuiModalField struct {
	label string
	ti    textinput.Model
}

type tuiModal struct {
	kind      tuiModalKind
	title     string
	fields    []tuiModalField
	cursor    int
	sid       string // context session ID
	eid       string // entity ID (agent/task being edited)
	err       string
	validator func(string) bool // for tuiModalConfirmTyped: submit enabled iff validator(input)==true
}

func newTUIModal(kind tuiModalKind, sid string) *tuiModal {
	type spec struct{ label, placeholder string }
	var specs []spec
	switch kind {
	case tuiModalNewSession:
		specs = []spec{
			{"Name", "e.g. Feature: User Auth"},
			{"Template", "blank · dev · research · fullstack · devops  (optional)"},
		}
	case tuiModalNewAgent:
		specs = []spec{
			{"Name", "e.g. Alice"},
			{"Role", "orchestrator / senior-dev / qa-agent / devops-agent / researcher / worker"},
			{"Mission", "north star objective, e.g. Keep all tests green (optional)"},
			{"Project", "project name (optional)"},
			{"Repo Path", "/absolute/path/to/repo (optional)"},
		}
	case tuiModalNewTask:
		specs = []spec{
			{"Title", "e.g. Implement user auth"},
			{"Description", "details (optional)"},
			{"Project", "project (optional)"},
		}
	case tuiModalQuickAgent:
		specs = []spec{{"Name", "e.g. Alice  (spawns as worker)"}}
	case tuiModalInjectAgent:
		specs = []spec{{"Message", "Direct instruction to agent's Claude Code session…"}}
	case tuiModalAddNote:
		specs = []spec{{"Note", "Observation, instruction, or context for the agent…"}}
	}
	fields := make([]tuiModalField, len(specs))
	for i, s := range specs {
		ti := textinput.New()
		ti.Placeholder = s.placeholder
		ti.CharLimit = 500
		if i == 0 {
			ti.Focus()
		}
		fields[i] = tuiModalField{label: s.label, ti: ti}
	}
	titles := map[tuiModalKind]string{
		tuiModalNewSession:  "New Session",
		tuiModalNewAgent:    "New Agent",
		tuiModalNewTask:     "New Task",
		tuiModalQuickAgent:  "+ Quick Spawn Worker",
		tuiModalInjectAgent: "Inject to Agent",
		tuiModalAddNote:     "Add Agent Note",
	}
	return &tuiModal{kind: kind, title: titles[kind], fields: fields, sid: sid}
}

// newTUIEditModal opens a pre-populated edit modal for an existing session/agent/task.
func newTUIEditModal(kind tuiModalKind, sid, eid string, values []string) *tuiModal {
	type spec struct{ label, placeholder string }
	var specs []spec
	switch kind {
	case tuiModalEditSession:
		specs = []spec{{"Name", "session name"}}
	case tuiModalEditAgent:
		specs = []spec{
			{"Name", "e.g. Alice"},
			{"Mission", "north star objective (optional)"},
			{"Project", "project name (optional)"},
			{"Repo Path", "/absolute/path/to/repo (optional)"},
		}
	case tuiModalEditTask:
		specs = []spec{
			{"Title", "task title"},
			{"Description", "details (optional)"},
			{"Project", "project (optional)"},
		}
	case tuiModalEditTaskStage:
		specs = []spec{{"Stage", "spec · implement · test · deploy · done · blocked · failed"}}
	}
	fields := make([]tuiModalField, len(specs))
	for i, s := range specs {
		ti := textinput.New()
		ti.Placeholder = s.placeholder
		ti.CharLimit = 300
		if i < len(values) {
			ti.SetValue(values[i])
		}
		if i == 0 {
			ti.Focus()
			// Move cursor to end of pre-filled text
			ti.CursorEnd()
		}
		fields[i] = tuiModalField{label: s.label, ti: ti}
	}
	titles := map[tuiModalKind]string{
		tuiModalEditSession:   "Edit Session",
		tuiModalEditAgent:     "Edit Agent",
		tuiModalEditTask:      "Edit Task",
		tuiModalEditTaskStage: "Set Task Stage",
	}
	return &tuiModal{kind: kind, title: titles[kind], fields: fields, sid: sid, eid: eid}
}

func (mo *tuiModal) value(i int) string {
	if i >= len(mo.fields) {
		return ""
	}
	return strings.TrimSpace(mo.fields[i].ti.Value())
}

// newTUIConfirmModal opens a typed-name confirmation modal. The title is the
// prompt shown above the input. validator returns true iff the typed value
// should enable the submit button. The caller stores onConfirm in pendingConfirm.
func newTUIConfirmModal(title string, validator func(string) bool) *tuiModal {
	ti := textinput.New()
	ti.Placeholder = "type name to confirm"
	ti.CharLimit = 200
	ti.Focus()
	return &tuiModal{
		kind:      tuiModalConfirmTyped,
		title:     title,
		fields:    []tuiModalField{{label: "Confirm", ti: ti}},
		validator: validator,
	}
}

func (m tuiModel) updateModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	mo := m.modal
	if mo == nil {
		return m, nil
	}
	var cmds []tea.Cmd
	switch msg.String() {
	case "esc":
		m.modal = nil
		m.focus = tuiFocusSidebar

	case "ctrl+c":
		m.ws.closeAll()
		return m, tea.Quit

	case "tab", "down":
		mo.fields[mo.cursor].ti.Blur()
		mo.cursor = (mo.cursor + 1) % len(mo.fields)
		cmds = append(cmds, mo.fields[mo.cursor].ti.Focus())

	case "shift+tab", "up":
		mo.fields[mo.cursor].ti.Blur()
		mo.cursor = (mo.cursor - 1 + len(mo.fields)) % len(mo.fields)
		cmds = append(cmds, mo.fields[mo.cursor].ti.Focus())

	case "enter":
		if mo.cursor < len(mo.fields)-1 {
			mo.fields[mo.cursor].ti.Blur()
			mo.cursor++
			cmds = append(cmds, mo.fields[mo.cursor].ti.Focus())
		} else {
			// For typed-confirm modals, only proceed if validator passes
			if mo.kind == tuiModalConfirmTyped {
				if mo.validator == nil || !mo.validator(mo.value(0)) {
					mo.err = "name does not match — try again"
					return m, tea.Batch(cmds...)
				}
			}
			cmd := m.submitModal()
			m.modal = nil
			m.pendingConfirm = nil
			m.focus = tuiFocusSidebar
			if cmd != nil {
				return m, cmd
			}
		}

	default:
		var cmd tea.Cmd
		mo.fields[mo.cursor].ti, cmd = mo.fields[mo.cursor].ti.Update(msg)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

func (m tuiModel) submitModal() tea.Cmd {
	mo := m.modal
	if mo == nil {
		return nil
	}
	switch mo.kind {
	case tuiModalConfirmTyped:
		// Action stored in pendingConfirm by the caller
		if m.pendingConfirm != nil {
			cmd := m.pendingConfirm.onConfirm
			return cmd
		}
		return nil
	case tuiModalNewSession:
		name := mo.value(0)
		if name == "" {
			return nil
		}
		return m.client.post("create-session", "/api/swarm/sessions",
			map[string]string{"name": name, "template": mo.value(1)})
	case tuiModalNewAgent:
		name := mo.value(0)
		if name == "" {
			return nil
		}
		role := mo.value(1)
		if role == "" {
			role = "worker"
		}
		return m.client.post("create-agent",
			"/api/swarm/sessions/"+mo.sid+"/agents",
			map[string]string{"name": name, "role": role, "mission": mo.value(2), "project": mo.value(3), "repo_path": mo.value(4)},
		)
	case tuiModalNewTask:
		title := mo.value(0)
		if title == "" {
			return nil
		}
		return m.client.post("create-task",
			"/api/swarm/sessions/"+mo.sid+"/tasks",
			map[string]string{"title": title, "description": mo.value(1), "project": mo.value(2)},
		)
	case tuiModalQuickAgent:
		name := mo.value(0)
		if name == "" {
			return nil
		}
		return m.client.post("create-agent",
			"/api/swarm/sessions/"+mo.sid+"/agents",
			map[string]string{"name": name, "role": "worker"},
		)

	case tuiModalEditSession:
		name := mo.value(0)
		if name == "" {
			return nil
		}
		return m.client.patch("edit-session",
			"/api/swarm/sessions/"+mo.sid,
			map[string]string{"name": name},
		)

	case tuiModalEditAgent:
		name := mo.value(0)
		if name == "" {
			return nil
		}
		return m.client.patch("edit-agent",
			"/api/swarm/sessions/"+mo.sid+"/agents/"+mo.eid,
			map[string]interface{}{
				"name":      name,
				"mission":   mo.value(1),
				"project":   mo.value(2),
				"repo_path": mo.value(3),
			},
		)

	case tuiModalEditTask:
		title := mo.value(0)
		if title == "" {
			return nil
		}
		return m.client.patch("edit-task",
			"/api/swarm/sessions/"+mo.sid+"/tasks/"+mo.eid,
			map[string]interface{}{
				"title":       title,
				"description": mo.value(1),
				"project":     mo.value(2),
			},
		)

	case tuiModalInjectAgent:
		msg := mo.value(0)
		if msg == "" {
			return nil
		}
		return m.client.post("inject-agent",
			"/api/swarm/sessions/"+mo.sid+"/agents/"+mo.eid+"/inject",
			map[string]string{"message": msg},
		)

	case tuiModalEditTaskStage:
		stage := mo.value(0)
		if stage == "" {
			return nil
		}
		return m.client.patch("edit-task-stage",
			"/api/swarm/sessions/"+mo.sid+"/tasks/"+mo.eid,
			map[string]interface{}{"stage": stage},
		)

	case tuiModalAddNote:
		content := mo.value(0)
		if content == "" {
			return nil
		}
		return m.client.post("add-note",
			"/api/swarm/sessions/"+mo.sid+"/agents/"+mo.eid+"/note",
			map[string]string{"content": content, "created_by": "user"},
		)
	}
	return nil
}

func (m tuiModel) viewModal() string {
	mo := m.modal
	if mo == nil {
		return ""
	}
	boxW := min(64, m.w-4)
	var sb strings.Builder
	sb.WriteString(lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render(mo.title) + "\n\n")
	for i, f := range mo.fields {
		cursor := "  "
		if i == mo.cursor {
			cursor = lipgloss.NewStyle().Foreground(colorTeal).Render("▶ ")
		}
		label := lipgloss.NewStyle().Foreground(colorText).Render(f.label + ":")
		sb.WriteString(cursor + label + "\n")
		sb.WriteString("  " + f.ti.View() + "\n\n")
	}
	if mo.err != "" {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorRed).Render("✗ "+mo.err) + "\n\n")
	}
	if mo.kind == tuiModalConfirmTyped {
		hint := "type exact name to enable confirm  ·  Enter confirm  ·  Esc cancel"
		sb.WriteString(dimStyle.Render(hint))
	} else {
		sb.WriteString(dimStyle.Render("Tab/↑↓ next field  ·  Enter confirm  ·  Esc cancel"))
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorTeal).
		Padding(1, 2).
		Width(boxW).
		Render(sb.String())
	padTop := (m.h - lipgloss.Height(box)) / 2
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
