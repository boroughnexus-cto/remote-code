package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/gorilla/websocket"
)

// ─── Layout ───────────────────────────────────────────────────────────────────

const (
	tuiSidebarW = 32 // left column width
	tuiInputH   = 3  // textarea row height
	tuiDetailH  = 11 // agent/session detail rows in right pane
)

// ─── Focus / modal kinds ──────────────────────────────────────────────────────

type tuiFocus int

const (
	tuiFocusSidebar tuiFocus = iota
	tuiFocusInput
	tuiFocusModal
)

type tuiModalKind int

const (
	tuiModalNone tuiModalKind = iota
	tuiModalNewSession
	tuiModalNewAgent
	tuiModalNewTask
	tuiModalQuickAgent  // fast 1-field worker spawn
	tuiModalEditSession // rename session
	tuiModalEditAgent   // edit agent name/mission/project/repo
	tuiModalEditTask    // edit task title/description/project
)

// ─── Data types ───────────────────────────────────────────────────────────────

type tuiAgent struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Role         string  `json:"role"`
	Status       string  `json:"status"`
	Mission      *string `json:"mission"`
	Project      *string `json:"project,omitempty"`
	RepoPath     *string `json:"repo_path,omitempty"`
	TmuxSession  *string `json:"tmux_session"`
	CurrentTask  *string `json:"current_task_id"`
	CurrentFile  *string `json:"current_file"`
	LatestNote   *string `json:"latest_note"`
	ContextPct   float64 `json:"context_pct"`
	ContextState string  `json:"context_state"`
	ModelName    string  `json:"model_name,omitempty"`
	TokensUsed   int64   `json:"tokens_used,omitempty"`
}

type tuiTask struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Description   *string  `json:"description,omitempty"`
	Project       *string  `json:"project,omitempty"`
	Stage         string   `json:"stage"`
	Phase         *string  `json:"phase,omitempty"`
	PhaseOrder    *int64   `json:"phase_order,omitempty"`
	GoalID        *string  `json:"goal_id,omitempty"`
	PRUrl         *string  `json:"pr_url,omitempty"`
	CIStatus      *string  `json:"ci_status,omitempty"`
	Confidence    *float64 `json:"confidence,omitempty"`
	TokensUsed    *int64   `json:"tokens_used,omitempty"`
	BlockedReason *string  `json:"blocked_reason,omitempty"`
}

type tuiSession struct {
	ID                    string  `json:"id"`
	Name                  string  `json:"name"`
	AutopilotEnabled      bool    `json:"autopilot_enabled"`
	AutopilotPlaneProject *string `json:"autopilot_plane_project_id,omitempty"`
}

type tuiEvent struct {
	AgentID string `json:"agent_id"`
	TaskID  string `json:"task_id"`
	Type    string `json:"type"`
	Payload string `json:"payload"`
	Ts      int64  `json:"ts"`
}

type tuiGoal struct {
	ID           string `json:"id"`
	Description  string `json:"description"`
	Status       string `json:"status"`
	Complexity   string `json:"complexity"`
	TokenBudget  int64  `json:"token_budget"`
	TokensUsed   int64  `json:"tokens_used"`
	JudgeNotes   string `json:"judge_notes"`
	CreatedAt    int64  `json:"created_at"`
}

type tuiEscalation struct {
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	TaskID  string `json:"task_id"`
	Reason  string `json:"reason"`
	Ts      int64  `json:"ts"`
}

type tuiState struct {
	Session     tuiSession      `json:"session"`
	Agents      []tuiAgent      `json:"agents"`
	Tasks       []tuiTask       `json:"tasks"`
	Events      []tuiEvent      `json:"events"`
	Goals       []tuiGoal       `json:"goals"`
	Escalations []tuiEscalation `json:"escalations"`
}

// ─── Messages ─────────────────────────────────────────────────────────────────

type tuiAnimTickMsg struct{}
type tuiTermMsg struct {
	agentID string
	content string
}
type tuiWSUpdateMsg struct {
	sid   string
	state tuiState
}
type tuiDataMsg struct {
	sessions []tuiSession
	states   map[string]tuiState
}
type tuiErrMsg       struct{ op, text string }
type tuiDoneMsg      struct{ op string }
type tuiAttachMsg    struct{ err error }
type tuiWorkQueueMsg struct{ items []WorkQueueItem }
type tuiIcingaMsg   struct{ services []IcingaService }
type tuiHelpHideMsg struct{ version int }

func tuiAnimTick() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return tuiAnimTickMsg{} })
}

func hideHelpAfter(version int) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(700 * time.Millisecond)
		return tuiHelpHideMsg{version: version}
	}
}

// ─── API client ───────────────────────────────────────────────────────────────

type swarmClient struct {
	baseURL string
	hc      *http.Client
}

func newSwarmClient() *swarmClient {
	return &swarmClient{
		baseURL: "http://localhost:8080",
		hc:      &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *swarmClient) do(method, path string, body interface{}) ([]byte, int, error) {
	var rb io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rb = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, rb)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b, resp.StatusCode, nil
}

func (c *swarmClient) fetchAll() tea.Cmd {
	return func() tea.Msg {
		b, _, err := c.do("GET", "/api/swarm/dashboard", nil)
		if err != nil {
			return tuiErrMsg{op: "fetch", text: "server unreachable: " + err.Error()}
		}
		var dash struct {
			Sessions []tuiSession `json:"sessions"`
		}
		if err := json.Unmarshal(b, &dash); err != nil {
			return tuiErrMsg{op: "fetch", text: "parse error: " + err.Error()}
		}
		states := make(map[string]tuiState, len(dash.Sessions))
		for _, s := range dash.Sessions {
			b2, st2, err2 := c.do("GET", "/api/swarm/sessions/"+s.ID, nil)
			if err2 != nil || st2 != 200 {
				continue
			}
			var st tuiState
			if json.Unmarshal(b2, &st) == nil {
				states[s.ID] = st
			}
		}
		return tuiDataMsg{sessions: dash.Sessions, states: states}
	}
}

func (c *swarmClient) post(op, path string, body interface{}) tea.Cmd {
	return func() tea.Msg {
		b, status, err := c.do("POST", path, body)
		if err != nil {
			return tuiErrMsg{op: op, text: err.Error()}
		}
		if status >= 400 {
			var errResp struct {
				Error string `json:"error"`
			}
			json.Unmarshal(b, &errResp)
			text := errResp.Error
			if text == "" {
				text = fmt.Sprintf("HTTP %d", status)
			}
			return tuiErrMsg{op: op, text: text}
		}
		return tuiDoneMsg{op: op}
	}
}

func (c *swarmClient) patch(op, path string, body interface{}) tea.Cmd {
	return func() tea.Msg {
		b, status, err := c.do("PATCH", path, body)
		if err != nil {
			return tuiErrMsg{op: op, text: err.Error()}
		}
		if status >= 400 {
			var errResp struct {
				Error string `json:"error"`
			}
			json.Unmarshal(b, &errResp)
			text := errResp.Error
			if text == "" {
				text = fmt.Sprintf("HTTP %d", status)
			}
			return tuiErrMsg{op: op, text: text}
		}
		return tuiDoneMsg{op: op}
	}
}

func (c *swarmClient) get(op, path string) tea.Cmd {
	return func() tea.Msg {
		b, status, err := c.do("GET", path, nil)
		if err != nil {
			return tuiErrMsg{op: op, text: err.Error()}
		}
		if status >= 400 {
			var errResp struct {
				Error string `json:"error"`
			}
			json.Unmarshal(b, &errResp)
			text := errResp.Error
			if text == "" {
				text = fmt.Sprintf("HTTP %d", status)
			}
			return tuiErrMsg{op: op, text: text}
		}
		if op == "workqueue" {
			var items []WorkQueueItem
			if json.Unmarshal(b, &items) == nil {
				return tuiWorkQueueMsg{items: items}
			}
		}
		if op == "icinga" {
			var svcs []IcingaService
			if json.Unmarshal(b, &svcs) == nil {
				return tuiIcingaMsg{services: svcs}
			}
		}
		return tuiDoneMsg{op: op}
	}
}

func (c *swarmClient) fetchTerminal(sid, agentID string) tea.Cmd {
	return func() tea.Msg {
		b, status, err := c.do("GET", "/api/swarm/sessions/"+sid+"/agents/"+agentID+"/terminal", nil)
		if err != nil || status != 200 {
			return tuiTermMsg{agentID: agentID, content: ""}
		}
		var resp struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(b, &resp) != nil {
			return tuiTermMsg{agentID: agentID, content: ""}
		}
		return tuiTermMsg{agentID: agentID, content: resp.Content}
	}
}

// ─── WebSocket manager ────────────────────────────────────────────────────────

type tuiWSManager struct {
	mu    sync.Mutex
	conns map[string]*websocket.Conn
	ch    chan tuiWSUpdateMsg
}

func newTUIWSManager() *tuiWSManager {
	return &tuiWSManager{
		conns: make(map[string]*websocket.Conn),
		ch:    make(chan tuiWSUpdateMsg, 64),
	}
}

func (m *tuiWSManager) connect(sid string) {
	m.mu.Lock()
	if _, ok := m.conns[sid]; ok {
		m.mu.Unlock()
		return
	}
	m.conns[sid] = nil // placeholder — prevents double-connect
	m.mu.Unlock()

	go func() {
		for {
			u := "ws://localhost:8080/ws/swarm?session=" + url.QueryEscape(sid)
			conn, _, err := websocket.DefaultDialer.Dial(u, nil)
			if err != nil {
				time.Sleep(5 * time.Second)
				continue
			}
			m.mu.Lock()
			m.conns[sid] = conn
			m.mu.Unlock()
			for {
				_, data, err := conn.ReadMessage()
				if err != nil {
					break
				}
				var env struct {
					Type  string   `json:"type"`
					State tuiState `json:"state"`
				}
				if json.Unmarshal(data, &env) == nil && env.Type == "swarm_state" {
					m.ch <- tuiWSUpdateMsg{sid: sid, state: env.State}
				}
			}
			m.mu.Lock()
			delete(m.conns, sid)
			m.mu.Unlock()
			time.Sleep(3 * time.Second)
		}
	}()
}

func (m *tuiWSManager) closeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.conns {
		if c != nil {
			c.Close()
		}
	}
}

func waitForWS(ch <-chan tuiWSUpdateMsg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

// ─── Sidebar items ────────────────────────────────────────────────────────────

type tuiItemKind int

const (
	tuiItemSession tuiItemKind = iota
	tuiItemAgent
	tuiItemTask
)

type tuiSidebarItem struct {
	kind tuiItemKind
	sid  string // session ID
	eid  string // entity ID (agent or task)
}

// ─── Modal ────────────────────────────────────────────────────────────────────

type tuiModalField struct {
	label string
	ti    textinput.Model
}

type tuiModal struct {
	kind   tuiModalKind
	title  string
	fields []tuiModalField
	cursor int
	sid    string // context session ID
	eid    string // entity ID (agent/task being edited)
	err    string
}

func newTUIModal(kind tuiModalKind, sid string) *tuiModal {
	type spec struct{ label, placeholder string }
	var specs []spec
	switch kind {
	case tuiModalNewSession:
		specs = []spec{{"Name", "e.g. Feature: User Auth"}}
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
		specs = []spec{{"Name", "e.g. Alice  (spawns as worker)"},
		}
	}
	fields := make([]tuiModalField, len(specs))
	for i, s := range specs {
		ti := textinput.New()
		ti.Placeholder = s.placeholder
		ti.CharLimit = 200
		if i == 0 {
			ti.Focus()
		}
		fields[i] = tuiModalField{label: s.label, ti: ti}
	}
	titles := map[tuiModalKind]string{
		tuiModalNewSession: "New Session",
		tuiModalNewAgent:   "New Agent",
		tuiModalNewTask:    "New Task",
		tuiModalQuickAgent: "+ Quick Spawn Worker",
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
		tuiModalEditSession: "Edit Session",
		tuiModalEditAgent:   "Edit Agent",
		tuiModalEditTask:    "Edit Task",
	}
	return &tuiModal{kind: kind, title: titles[kind], fields: fields, sid: sid, eid: eid}
}

func (mo *tuiModal) value(i int) string {
	if i >= len(mo.fields) {
		return ""
	}
	return strings.TrimSpace(mo.fields[i].ti.Value())
}

// ─── Main model ───────────────────────────────────────────────────────────────

type tuiModel struct {
	// Data
	sessions []tuiSession
	states   map[string]tuiState

	// Sidebar
	items  []tuiSidebarItem
	cursor int

	// Right pane
	vp      viewport.Model
	vpReady bool
	vpLines map[string][]string // sid → rendered log lines

	// Input
	chatInput textarea.Model

	// Modal
	modal *tuiModal

	// Focus
	focus tuiFocus

	// Animation
	frame int

	// Terminal size
	w, h int

	// Status bar (persistent — stays until replaced)
	flash    string
	flashErr bool

	// Terminal snapshot (agentID → last captured content)
	termContent  map[string]string
	termFetching bool

	// Escalation view
	escView      bool
	escCursor    int
	escInputting bool
	escActive    *tuiEscalation
	escInput     textinput.Model

	// Goals view
	goalView   bool
	goalCursor int

	// Work queue view
	workQueueView  bool
	workQueueItems []WorkQueueItem
	workQueueSID   string

	// Despawn confirmation (two-press: first d sets, second d executes, esc clears)
	pendingDespawn *tuiSidebarItem

	// Help overlay (hold ?)
	helpVisible bool
	helpVersion int

	// Icinga monitor view (I key)
	icingaView     bool
	icingaServices []IcingaService // sorted by state
	icingaTopCur   int             // cursor in top pane (services)
	icingaBotCur   int             // cursor in bottom pane (recent alerts)
	icingaFocus    int             // 0=top, 1=bottom

	// Event log view (L key)
	evtLogView      bool
	evtCursor       int
	evtAgentFilter  string
	evtDetailView   *tuiEvent
	vpRawEvents     map[string][]tuiEvent

	// Clients
	client *swarmClient
	ws     *tuiWSManager
}

func newTUIModel() tuiModel {
	c := newSwarmClient()
	ws := newTUIWSManager()
	ta := textarea.New()
	ta.Placeholder = "Message to orchestrator… /goal <desc> to set a goal  (Enter sends, Esc unfocuses)"
	ta.ShowLineNumbers = false
	ta.CharLimit = 2000
	ei := textinput.New()
	ei.Placeholder = "Type response to escalation…"
	ei.CharLimit = 500
	return tuiModel{
		states:      make(map[string]tuiState),
		vpLines:     make(map[string][]string),
		vpRawEvents: make(map[string][]tuiEvent),
		termContent: make(map[string]string),
		chatInput:   ta,
		escInput:    ei,
		client:      c,
		ws:          ws,
	}
}

// ─── Sidebar helpers ──────────────────────────────────────────────────────────

func (m *tuiModel) rebuildItems() {
	m.items = nil
	for _, sess := range m.sessions {
		m.items = append(m.items, tuiSidebarItem{kind: tuiItemSession, sid: sess.ID})
		st, ok := m.states[sess.ID]
		if !ok {
			continue
		}
		for _, a := range st.Agents {
			m.items = append(m.items, tuiSidebarItem{kind: tuiItemAgent, sid: sess.ID, eid: a.ID})
		}
		for _, t := range st.Tasks {
			m.items = append(m.items, tuiSidebarItem{kind: tuiItemTask, sid: sess.ID, eid: t.ID})
		}
	}
	if m.cursor >= len(m.items) {
		m.cursor = max(0, len(m.items)-1)
	}
}

func (m tuiModel) selItem() *tuiSidebarItem {
	if m.cursor < len(m.items) {
		it := m.items[m.cursor]
		return &it
	}
	return nil
}

func (m tuiModel) selSessionID() string {
	it := m.selItem()
	if it != nil {
		return it.sid
	}
	if len(m.sessions) > 0 {
		return m.sessions[0].ID
	}
	return ""
}

func (m tuiModel) lookupAgent(sid, aid string) *tuiAgent {
	st := m.states[sid]
	for i := range st.Agents {
		if st.Agents[i].ID == aid {
			return &st.Agents[i]
		}
	}
	return nil
}

func (m tuiModel) lookupTask(sid, tid string) *tuiTask {
	st := m.states[sid]
	for i := range st.Tasks {
		if st.Tasks[i].ID == tid {
			return &st.Tasks[i]
		}
	}
	return nil
}

func (m tuiModel) lookupSession(sid string) *tuiSession {
	for i := range m.sessions {
		if m.sessions[i].ID == sid {
			return &m.sessions[i]
		}
	}
	return nil
}

// ─── Event feed ───────────────────────────────────────────────────────────────

func (m *tuiModel) appendEvents(sid string, state tuiState) {
	// Build agent name lookup
	agentNames := make(map[string]string, len(state.Agents))
	for _, a := range state.Agents {
		agentNames[a.ID] = a.Name
	}

	// Apply agent filter
	var evts []tuiEvent
	for _, ev := range state.Events {
		if m.evtAgentFilter != "" {
			name := agentNames[ev.AgentID]
			if !strings.Contains(strings.ToLower(name), strings.ToLower(m.evtAgentFilter)) {
				continue
			}
		}
		evts = append(evts, ev)
	}

	// Cap at 500 raw events
	if len(evts) > 500 {
		evts = evts[len(evts)-500:]
	}
	m.vpRawEvents[sid] = evts

	// Render lines
	lines := make([]string, 0, len(evts))
	for _, ev := range evts {
		ts := time.Unix(ev.Ts, 0).Format("15:04:05")
		agentName := truncStr(agentNames[ev.AgentID], 12)
		payload := truncStr(ev.Payload, 80)
		typeStr := lipgloss.NewStyle().Foreground(tuiEventColor(ev.Type)).Render(truncStr(ev.Type, 26))
		line := fmt.Sprintf("%s  %s  %-12s  %s", ts, typeStr, agentName, payload)
		lines = append(lines, line)
	}
	m.vpLines[sid] = lines
}

func (m *tuiModel) updateVP() {
	if !m.vpReady {
		return
	}
	// Agent with active tmux → show live terminal capture
	it := m.selItem()
	if it != nil && it.kind == tuiItemAgent {
		agent := m.lookupAgent(it.sid, it.eid)
		if agent != nil && agent.TmuxSession != nil {
			if content, ok := m.termContent[it.eid]; ok && content != "" {
				m.vp.SetContent(content)
				m.vp.GotoBottom()
				return
			}
			m.vp.SetContent(dimStyle.Render("  Fetching terminal…"))
			return
		}
	}
	sid := m.selSessionID()
	lines := m.vpLines[sid]
	if len(lines) == 0 {
		m.vp.SetContent(dimStyle.Render("  No events yet"))
		return
	}
	m.vp.SetContent(strings.Join(lines, "\n"))
	m.vp.GotoBottom()
}

func tuiEventColor(evType string) lipgloss.Color {
	switch {
	case strings.Contains(evType, "stuck"):
		return colorRed
	case strings.Contains(evType, "spawn"), strings.Contains(evType, "start"):
		return colorGreen
	case strings.Contains(evType, "message"), strings.Contains(evType, "inject"):
		return colorTeal
	case strings.Contains(evType, "task"):
		return colorBlue
	default:
		return colorDim
	}
}

// ─── Flash ────────────────────────────────────────────────────────────────────

func (m *tuiModel) setFlash(text string, isErr bool) {
	m.flash = text
	m.flashErr = isErr
}

// ─── Init ─────────────────────────────────────────────────────────────────────

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(
		tea.EnterAltScreen,
		tuiAnimTick(),
		m.client.fetchAll(),
		waitForWS(m.ws.ch),
	)
}

// ─── Update ──────────────────────────────────────────────────────────────────

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		bodyH := m.h - 1 - 1 - tuiInputH - 2 // hud + help + input rows + input borders
		if bodyH < 5 {
			bodyH = 5
		}
		vpH := bodyH - tuiDetailH - 1
		if vpH < 3 {
			vpH = 3
		}
		vpW := m.w - tuiSidebarW - 1
		if vpW < 10 {
			vpW = 10
		}
		if !m.vpReady {
			m.vp = viewport.New(vpW, vpH)
			m.vpReady = true
		} else {
			m.vp.Width = vpW
			m.vp.Height = vpH
		}
		m.chatInput.SetWidth(m.w - 4)
		m.chatInput.SetHeight(tuiInputH)
		m.updateVP()

	case tuiAnimTickMsg:
		m.frame = (m.frame + 1) % 8
		cmds = append(cmds, tuiAnimTick())
		if m.frame%3 == 0 && !m.termFetching {
			it := m.selItem()
			if it != nil && it.kind == tuiItemAgent {
				agent := m.lookupAgent(it.sid, it.eid)
				if agent != nil && agent.TmuxSession != nil {
					m.termFetching = true
					cmds = append(cmds, m.client.fetchTerminal(it.sid, it.eid))
				}
			}
		}

	case tuiTermMsg:
		m.termFetching = false
		if msg.content != "" {
			m.termContent[msg.agentID] = msg.content
			m.updateVP()
		}

	case tuiWSUpdateMsg:
		m.states[msg.sid] = msg.state
		m.appendEvents(msg.sid, msg.state)
		m.rebuildItems()
		m.updateVP()
		cmds = append(cmds, waitForWS(m.ws.ch))

	case tuiDataMsg:
		m.sessions = msg.sessions
		m.states = msg.states
		for _, sess := range msg.sessions {
			m.ws.connect(sess.ID)
			if st, ok := msg.states[sess.ID]; ok {
				m.appendEvents(sess.ID, st)
			}
		}
		m.rebuildItems()
		m.updateVP()

	case tuiErrMsg:
		m.setFlash(msg.text, true)

	case tuiDoneMsg:
		labels := map[string]string{
			"spawn": "Agent spawned", "despawn": "Agent stopped",
			"msg": "Message sent", "create-session": "Session created",
			"create-agent": "Agent created", "create-task": "Task created",
			"resume": "Session resumed", "create-goal": "Goal created",
			"esc-respond": "Escalation resolved",
		}
		label := labels[msg.op]
		if label == "" {
			label = msg.op + " OK"
		}
		m.setFlash(label, false)
		cmds = append(cmds, m.client.fetchAll())

	case tuiHelpHideMsg:
		if msg.version == m.helpVersion {
			m.helpVisible = false
		}

	case tuiIcingaMsg:
		m.icingaServices = msg.services
		m.icingaTopCur = 0
		// Scroll bottom pane to show most recent alert at top.
		m.icingaBotCur = 0

	case tuiWorkQueueMsg:
		m.workQueueItems = msg.items
		m.updateVP()

	case tuiAttachMsg:
		m.setFlash("Returned from tmux session", false)
		cmds = append(cmds, m.client.fetchAll())

	case tea.KeyMsg:
		// ? shows hold-to-view help from anywhere; each press resets the hide timer.
		if msg.String() == "?" {
			m.helpVisible = true
			m.helpVersion++
			cmds = append(cmds, hideHelpAfter(m.helpVersion))
			break
		}
		// Any other key hides the help overlay immediately.
		if m.helpVisible {
			m.helpVisible = false
		}
		if m.evtDetailView != nil {
			if msg.String() == "q" || msg.String() == "esc" {
				m.evtDetailView = nil
			}
		} else if m.icingaView {
			m, cmds = m.updateIcingaView(msg)
		} else if m.evtLogView {
			m, cmds = m.updateEventLog(msg)
		} else if m.workQueueView {
			if msg.String() == "q" || msg.String() == "esc" {
				m.workQueueView = false
				m.workQueueItems = nil
			}
		} else if m.goalView {
			m, cmds = m.updateGoalView(msg)
		} else if m.escView {
			m, cmds = m.updateEscalation(msg)
		} else {
			switch m.focus {
			case tuiFocusModal:
				return m.updateModal(msg)
			case tuiFocusInput:
				m, cmds = m.updateInput(msg)
			case tuiFocusSidebar:
				m, cmds = m.updateSidebar(msg)
			}
		}
	}

	// Pass scroll events to viewport when sidebar focused
	if m.focus == tuiFocusSidebar && m.vpReady {
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	return m, tea.Batch(cmds...)
}

func (m tuiModel) updateInput(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	var cmds []tea.Cmd
	switch msg.String() {
	case "esc":
		m.chatInput.Blur()
		m.focus = tuiFocusSidebar
	case "ctrl+c":
		m.ws.closeAll()
		cmds = append(cmds, tea.Quit)
	case "enter":
		text := strings.TrimSpace(m.chatInput.Value())
		if text != "" {
			sid := m.selSessionID()
			if sid != "" {
				m.chatInput.Reset()
				m.focus = tuiFocusSidebar
				if strings.HasPrefix(text, "/goal ") {
					desc := strings.TrimSpace(text[6:])
					if desc != "" {
						path := "/api/swarm/sessions/" + sid + "/goals"
						cmds = append(cmds, m.client.post("create-goal", path, map[string]string{"description": desc}))
					}
				} else {
					path := "/api/swarm/sessions/" + sid + "/orchestrator/message"
					cmds = append(cmds, m.client.post("msg", path, map[string]string{"text": text}))
					// Optimistic local echo
					ts := time.Now().Format("15:04:05")
					line := lipgloss.NewStyle().Foreground(colorTeal).Render(
						fmt.Sprintf("%s  %-22s  %s", ts, "→ you", truncStr(text, 44)))
					m.vpLines[sid] = append(m.vpLines[sid], line)
					m.updateVP()
				}
			}
		}
	default:
		var cmd tea.Cmd
		m.chatInput, cmd = m.chatInput.Update(msg)
		cmds = append(cmds, cmd)
	}
	return m, cmds
}

func (m tuiModel) updateSidebar(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	var cmds []tea.Cmd
	// Cancel pending despawn on any key except d
	if m.pendingDespawn != nil && msg.String() != "d" {
		m.pendingDespawn = nil
		m.flash = ""
	}
	switch msg.String() {
	case "q", "ctrl+c":
		m.ws.closeAll()
		cmds = append(cmds, tea.Quit)

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.updateVP()
		}
	case "down", "j":
		if m.cursor < len(m.items)-1 {
			m.cursor++
			m.updateVP()
		}

	case "tab", "/":
		m.focus = tuiFocusInput
		cmds = append(cmds, m.chatInput.Focus())

	case "enter":
		it := m.selItem()
		if it != nil && it.kind == tuiItemAgent {
			agent := m.lookupAgent(it.sid, it.eid)
			if agent != nil && agent.TmuxSession != nil {
				// Use switch-client when already inside tmux (avoids nesting warning),
				// fall back to attach-session otherwise.
				var cmd *exec.Cmd
				if os.Getenv("TMUX") != "" {
					cmd = exec.Command("tmux", "switch-client", "-t", *agent.TmuxSession)
				} else {
					cmd = exec.Command("tmux", "attach-session", "-t", *agent.TmuxSession)
				}
				cmds = append(cmds, tea.ExecProcess(cmd, func(err error) tea.Msg {
					return tuiAttachMsg{err: err}
				}))
			} else {
				m.setFlash("No tmux session — press s to spawn", false)
			}
		}

	case "s":
		it := m.selItem()
		if it != nil && it.kind == tuiItemAgent {
			agent := m.lookupAgent(it.sid, it.eid)
			if agent != nil && agent.TmuxSession == nil {
				path := "/api/swarm/sessions/" + it.sid + "/agents/" + it.eid + "/spawn"
				cmds = append(cmds, m.client.post("spawn", path, nil))
			} else {
				m.setFlash("Agent already running", false)
			}
		}

	case "d":
		it := m.selItem()
		if it != nil && it.kind == tuiItemAgent {
			agent := m.lookupAgent(it.sid, it.eid)
			if agent != nil && agent.TmuxSession != nil {
				if m.pendingDespawn != nil && m.pendingDespawn.eid == it.eid {
					// Second press — confirmed, execute despawn
					path := "/api/swarm/sessions/" + it.sid + "/agents/" + it.eid + "/despawn"
					cmds = append(cmds, m.client.post("despawn", path, nil))
					m.pendingDespawn = nil
				} else {
					// First press — require confirmation
					m.pendingDespawn = it
					m.setFlash("Despawn "+agent.Name+"? Press d again to confirm, Esc to cancel", true)
				}
			} else {
				m.setFlash("Agent not running", false)
			}
		}

	case "r":
		it := m.selItem()
		if it != nil {
			path := "/api/swarm/sessions/" + it.sid + "/resume"
			cmds = append(cmds, m.client.post("resume", path, nil))
		}

	case "+":
		sid := m.selSessionID()
		if sid != "" {
			m.modal = newTUIModal(tuiModalQuickAgent, sid)
			m.focus = tuiFocusModal
		}

	case "n":
		sid := m.selSessionID()
		if sid != "" {
			m.modal = newTUIModal(tuiModalNewAgent, sid)
			m.focus = tuiFocusModal
		}

	case "t":
		sid := m.selSessionID()
		if sid != "" {
			m.modal = newTUIModal(tuiModalNewTask, sid)
			m.focus = tuiFocusModal
		}

	case "c":
		m.modal = newTUIModal(tuiModalNewSession, "")
		m.focus = tuiFocusModal

	case "e":
		sid := m.selSessionID()
		if sid != "" {
			m.escView = true
			m.escCursor = 0
			m.escActive = nil
			m.escInputting = false
		}

	case "E":
		// Edit selected item in-place
		it := m.selItem()
		if it != nil {
			switch it.kind {
			case tuiItemSession:
				sess := m.lookupSession(it.sid)
				if sess != nil {
					m.modal = newTUIEditModal(tuiModalEditSession, it.sid, "", []string{sess.Name})
					m.focus = tuiFocusModal
				}
			case tuiItemAgent:
				agent := m.lookupAgent(it.sid, it.eid)
				if agent != nil {
					mission := ""
					if agent.Mission != nil {
						mission = *agent.Mission
					}
					project := ""
					if agent.Project != nil {
						project = *agent.Project
					}
					repoPath := ""
					if agent.RepoPath != nil {
						repoPath = *agent.RepoPath
					}
					m.modal = newTUIEditModal(tuiModalEditAgent, it.sid, it.eid, []string{agent.Name, mission, project, repoPath})
					m.focus = tuiFocusModal
				}
			case tuiItemTask:
				task := m.lookupTask(it.sid, it.eid)
				if task != nil {
					desc := ""
					if task.Description != nil {
						desc = *task.Description
					}
					proj := ""
					if task.Project != nil {
						proj = *task.Project
					}
					m.modal = newTUIEditModal(tuiModalEditTask, it.sid, it.eid, []string{task.Title, desc, proj})
					m.focus = tuiFocusModal
				}
			}
		}

	case "g":
		sid := m.selSessionID()
		if sid != "" {
			m.goalView = true
			m.goalCursor = 0
		}

	case "A":
		sid := m.selSessionID()
		if sid != "" {
			st := m.states[sid]
			newVal := !st.Session.AutopilotEnabled
			body := map[string]interface{}{"enabled": newVal}
			if st.Session.AutopilotPlaneProject != nil {
				body["plane_project_id"] = *st.Session.AutopilotPlaneProject
			}
			path := "/api/swarm/sessions/" + sid + "/autopilot"
			cmds = append(cmds, m.client.patch("autopilot", path, body))
			if newVal {
				m.setFlash("Autopilot ON — syncing Plane issues", false)
			} else {
				m.setFlash("Autopilot OFF", false)
			}
		}

	case "W":
		sid := m.selSessionID()
		if sid != "" {
			m.workQueueView = true
			m.workQueueSID = sid
			m.workQueueItems = nil
			path := "/api/swarm/sessions/" + sid + "/plane/issues?state_group=backlog,unstarted"
			cmds = append(cmds, m.client.get("workqueue", path))
		}

	case "I":
		m.icingaView = true
		m.icingaServices = nil // show loading state
		m.icingaTopCur = 0
		m.icingaBotCur = 0
		m.icingaFocus = 0
		cmds = append(cmds, m.client.get("icinga", "/api/icinga/services"))

	case "L":
		sid := m.selSessionID()
		if sid != "" {
			evts := m.vpRawEvents[sid]
			m.evtLogView = true
			m.evtDetailView = nil
			m.evtCursor = max(0, len(evts)-1)
		}

	case "R":
		cmds = append(cmds, m.client.fetchAll())
	}
	return m, cmds
}

func (m tuiModel) updateEventLog(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	sid := m.selSessionID()
	evts := m.vpRawEvents[sid]
	n := len(evts)
	state := m.states[sid]

	agentNames := make(map[string]string)
	for _, a := range state.Agents {
		agentNames[a.ID] = a.Name
	}

	switch msg.String() {
	case "q", "esc":
		m.evtLogView = false
		m.evtDetailView = nil
	case "j", "down":
		if m.evtCursor < n-1 {
			m.evtCursor++
		}
	case "k", "up":
		if m.evtCursor > 0 {
			m.evtCursor--
		}
	case "g":
		m.evtCursor = 0
	case "G":
		m.evtCursor = max(0, n-1)
	case "f":
		// Toggle filter by agent of event under cursor
		if m.evtCursor >= 0 && m.evtCursor < n {
			ev := evts[m.evtCursor]
			name := agentNames[ev.AgentID]
			if name != "" {
				if m.evtAgentFilter == name {
					m.evtAgentFilter = ""
				} else {
					m.evtAgentFilter = name
				}
				// Refilter
				for _, sess := range m.sessions {
					if st, ok := m.states[sess.ID]; ok {
						m.appendEvents(sess.ID, st)
					}
				}
				m.updateVP()
				// Reposition cursor at bottom of filtered results
				evts = m.vpRawEvents[sid]
				m.evtCursor = max(0, len(evts)-1)
			}
		}
	case "F":
		m.evtAgentFilter = ""
		for _, sess := range m.sessions {
			if st, ok := m.states[sess.ID]; ok {
				m.appendEvents(sess.ID, st)
			}
		}
		m.updateVP()
		evts = m.vpRawEvents[sid]
		m.evtCursor = max(0, len(evts)-1)
	case "enter":
		if m.evtCursor >= 0 && m.evtCursor < n {
			ev := evts[m.evtCursor]
			m.evtDetailView = &ev
		}
	}
	return m, nil
}

func (m tuiModel) updateEscalation(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	var cmds []tea.Cmd
	sid := m.selSessionID()
	st := m.states[sid]
	escs := st.Escalations

	if m.escInputting {
		switch msg.String() {
		case "esc":
			m.escInputting = false
			m.escInput.Blur()
		case "enter":
			text := strings.TrimSpace(m.escInput.Value())
			if text != "" && m.escActive != nil {
				path := "/api/swarm/sessions/" + sid + "/escalations/" + m.escActive.ID + "/respond"
				cmds = append(cmds, m.client.post("esc-respond", path, map[string]string{"text": text}))
				m.escInput.Reset()
				m.escInput.Blur()
				m.escInputting = false
				m.escActive = nil
			}
		default:
			var cmd tea.Cmd
			m.escInput, cmd = m.escInput.Update(msg)
			cmds = append(cmds, cmd)
		}
		return m, cmds
	}

	switch msg.String() {
	case "e", "esc", "q":
		m.escView = false
	case "up", "k":
		if m.escCursor > 0 {
			m.escCursor--
		}
	case "down", "j":
		if m.escCursor < len(escs)-1 {
			m.escCursor++
		}
	case "enter":
		if m.escCursor < len(escs) {
			esc := escs[m.escCursor]
			m.escActive = &esc
			m.escInputting = true
			cmds = append(cmds, m.escInput.Focus())
		}
	}
	return m, cmds
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
			cmd := m.submitModal()
			m.modal = nil
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
	case tuiModalNewSession:
		name := mo.value(0)
		if name == "" {
			return nil
		}
		return m.client.post("create-session", "/api/swarm/sessions", map[string]string{"name": name})
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
	}
	return nil
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

	bodyH := m.h - 1 - 1 - tuiInputH - 2 - 1 // hud + help + input borders + status bar
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

	return strings.Join([]string{m.viewHUD(), body, m.viewStatusBar(), m.viewInput(), m.viewHelp()}, "\n")
}

func (m tuiModel) viewHUD() string {
	var live, coding, thinking, waiting, stuck int
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
	}

	parts := []string{titleStyle.Render("⬡ RC SWARM")}
	parts = append(parts, dimStyle.Render("│"))
	parts = append(parts, dimStyle.Render(fmt.Sprintf("%d session%s", len(m.sessions), pluralS(len(m.sessions)))))
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
		parts = append(parts, dimStyle.Render(tokStr+"  "+costStr))
	}
	return hudStyle.Width(m.w).Render(strings.Join(parts, "  "))
}

func (m tuiModel) viewSidebar(h int) string {
	var lines []string
	for i, it := range m.items {
		if len(lines) >= h {
			break
		}
		sel := i == m.cursor && m.focus == tuiFocusSidebar
		switch it.kind {
		case tuiItemSession:
			sess := m.lookupSession(it.sid)
			if sess == nil {
				continue
			}
			st := m.states[it.sid]
			live := 0
			for _, a := range st.Agents {
				if a.TmuxSession != nil {
					live++
				}
			}
			name := truncStr(sess.Name, tuiSidebarW-7)
			suffix := ""
			if live > 0 {
				suffix = lipgloss.NewStyle().Foreground(colorTeal).Render(fmt.Sprintf(" ·%d", live))
			}
			base := lipgloss.NewStyle().Foreground(colorText).Bold(true)
			if sel {
				base = base.Background(colorSubtle)
			}
			lines = append(lines, base.Width(tuiSidebarW).Render("  "+name)+suffix)

		case tuiItemAgent:
			agent := m.lookupAgent(it.sid, it.eid)
			if agent == nil {
				continue
			}
			_, roleColor := tuiRoleConfig(agent.Role)
			spinFrame, statusLabel, statusColor := tuiStatusConfig(agent.Status, m.frame)
			bg := SpritePanelBG
			if sel {
				bg = colorSubtle
			}
			// 4-line sprite portrait (6 chars wide)
			sprite := GetAgentSprite(agent.Role, agent.Status, m.frame)
			spriteStyled := lipgloss.NewStyle().Background(bg).Render(sprite)

			// Info column (tuiSidebarW - 6 sprite cols - 1 gap)
			infoW := tuiSidebarW - 7
			faint := agent.TmuxSession == nil
			nameStyle := lipgloss.NewStyle().Foreground(roleColor).Bold(true)
			if faint {
				nameStyle = nameStyle.Faint(true)
			}
			statusStyle := lipgloss.NewStyle().Foreground(statusColor)
			dimInfo := lipgloss.NewStyle().Foreground(colorDim)
			if sel {
				nameStyle = nameStyle.Background(colorSubtle)
				statusStyle = statusStyle.Background(colorSubtle)
				dimInfo = dimInfo.Background(colorSubtle)
			}
			infoLine0 := nameStyle.Render(truncStr(agent.Name, infoW))
			infoLine1 := statusStyle.Render(spinFrame + " " + truncStr(statusLabel, infoW-2))
			infoLine2 := ""
			if agent.Mission != nil {
				infoLine2 = dimInfo.Render(truncStr(*agent.Mission, infoW))
			}
			infoLine3 := dimInfo.Render(truncStr(agent.Role, infoW))

			// Join sprite + info side by side, 4 rows each
			spriteLines := strings.Split(spriteStyled, "\n")
			infoLines := []string{infoLine0, infoLine1, infoLine2, infoLine3}
			for i := 0; i < 4; i++ {
				sl := ""
				if i < len(spriteLines) {
					sl = spriteLines[i]
				}
				il := ""
				if i < len(infoLines) {
					il = infoLines[i]
				}
				rowBg := lipgloss.NewStyle()
				if sel {
					rowBg = rowBg.Background(colorSubtle)
				}
				lines = append(lines, rowBg.Width(tuiSidebarW).Render(sl+" "+il))
			}

		case tuiItemTask:
			task := m.lookupTask(it.sid, it.eid)
			if task == nil {
				continue
			}
			stageC := tuiStageColor(task.Stage)
			stageStr := lipgloss.NewStyle().Foreground(stageC).Render(fmt.Sprintf("[%-6s]", shortStage(task.Stage)))
			dot, dotStyle := ciDot(task)
			dotStr := dotStyle.Render(dot)
			title := truncStr(task.Title, tuiSidebarW-13)
			row := fmt.Sprintf("  %s %-*s %s", dotStr, tuiSidebarW-13, title, stageStr)
			base := lipgloss.NewStyle().Foreground(colorDim)
			if sel {
				base = base.Background(colorSubtle).Foreground(colorText)
			}
			lines = append(lines, base.Width(tuiSidebarW).Render(row))
		}
	}
	// Pad to height
	for len(lines) < h {
		lines = append(lines, lipgloss.NewStyle().Width(tuiSidebarW).Render(""))
	}
	return strings.Join(lines[:h], "\n")
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
	// Pad detail to tuiDetailH lines
	detStr := det.String()
	n := strings.Count(detStr, "\n")
	for n < tuiDetailH {
		detStr += "\n"
		n++
	}
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
	info.WriteString(statusPart + "  " + dimStyle.Render("["+monitor+"]") + "\n")

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
}

func (m tuiModel) viewTaskDetail(w *strings.Builder, sid, tid string, rightW int) {
	task := m.lookupTask(sid, tid)
	if task == nil {
		return
	}
	stageC := tuiStageColor(task.Stage)
	w.WriteString(lipgloss.NewStyle().Foreground(stageC).Bold(true).
		Render("◆ "+truncStr(task.Title, rightW-4)) + "\n")
	w.WriteString(dimStyle.Render("  Stage: ") +
		lipgloss.NewStyle().Foreground(stageC).Render(task.Stage) + "\n")
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

	// Right: flash message
	var rightStr string
	if m.flash != "" {
		icon := "✓"
		c := colorGreen
		if m.flashErr {
			icon = "✗"
			c = colorRed
		}
		rightStr = lipgloss.NewStyle().Foreground(c).Render(icon + " " + m.flash)
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
	keys := "  ↑↓/jk nav  ·  Enter attach  ·  Tab// input  ·  s spawn  ·  d stop  ·  + quick-agent  ·  n agent  ·  t task  ·  c session  ·  E edit  ·  I icinga  ·  L event-log  ·  e escalations  ·  g goals  ·  A autopilot  ·  W queue  ·  R refresh  ·  q quit"
	return dimStyle.Width(m.w).Render(keys)
}

func (m tuiModel) viewWorkQueueScreen() string {
	var sb strings.Builder
	sb.WriteString(m.viewHUD() + "\n")
	title := lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("Work Queue — Plane Backlog")
	sb.WriteString(title + "\n\n")

	if m.workQueueItems == nil {
		sb.WriteString(dimStyle.Render("  Loading…") + "\n")
	} else if len(m.workQueueItems) == 0 {
		sb.WriteString(dimStyle.Render("  No items in backlog or unstarted state.") + "\n")
	} else {
		priIcon := map[string]string{
			"urgent": "🔴", "high": "🟠", "medium": "🟡",
		}
		for _, item := range m.workQueueItems {
			icon := priIcon[item.Priority]
			if icon == "" {
				icon = "⚪"
			}
			stateLabel := item.StateGroup
			line := fmt.Sprintf("  %s  [%-9s]  %s", icon, stateLabel, item.Title)
			sb.WriteString(line + "\n")
		}
	}

	sb.WriteString("\n" + dimStyle.Render("  Press q or Esc to close"))
	return sb.String()
}

func (m tuiModel) viewEventLogScreen() string {
	sid := m.selSessionID()
	evts := m.vpRawEvents[sid]
	state := m.states[sid]

	agentNames := make(map[string]string)
	for _, a := range state.Agents {
		agentNames[a.ID] = a.Name
	}
	taskTitles := make(map[string]string)
	for _, t := range state.Tasks {
		taskTitles[t.ID] = t.Title
	}

	var sb strings.Builder
	sb.WriteString(m.viewHUD() + "\n")

	title := lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("Event Log")
	filterBadge := ""
	if m.evtAgentFilter != "" {
		filterBadge = " " + lipgloss.NewStyle().Foreground(colorOrange).Render("[agent: "+m.evtAgentFilter+"]") +
			dimStyle.Render(" (F clears)")
	}
	countStr := dimStyle.Render(fmt.Sprintf("  %d events", len(evts)))
	sb.WriteString(title + filterBadge + countStr + "\n\n")

	if len(evts) == 0 {
		sb.WriteString(dimStyle.Render("  No events") + "\n")
	} else {
		// Show a window of events around the cursor
		visible := m.h - 9
		if visible < 5 {
			visible = 5
		}
		start := m.evtCursor - visible/2
		if start < 0 {
			start = 0
		}
		end := start + visible
		if end > len(evts) {
			end = len(evts)
		}
		if end-start < visible && start > 0 {
			start = end - visible
			if start < 0 {
				start = 0
			}
		}

		for i := start; i < end; i++ {
			ev := evts[i]
			ts := time.Unix(ev.Ts, 0).Format("15:04:05")
			agentName := truncStr(agentNames[ev.AgentID], 12)
			typeColored := lipgloss.NewStyle().Foreground(tuiEventColor(ev.Type)).Render(fmt.Sprintf("%-26s", truncStr(ev.Type, 26)))
			payload := truncStr(ev.Payload, 60)
			line := fmt.Sprintf("%s  %s  %-12s  %s", ts, typeColored, agentName, payload)
			if i == m.evtCursor {
				line = lipgloss.NewStyle().Background(lipgloss.Color("#1a3050")).Foreground(lipgloss.Color("#e0e8f0")).Render(
					fmt.Sprintf("%s  %-26s  %-12s  %s", ts, truncStr(ev.Type, 26), agentName, payload),
				)
			}
			sb.WriteString("  " + line + "\n")
		}
		sb.WriteString(fmt.Sprintf("\n"+dimStyle.Render("  %d/%d"), m.evtCursor+1, len(evts)))
	}

	sb.WriteString("\n" + dimStyle.Render("  j/k navigate  ·  Enter detail  ·  f filter-agent  ·  F clear-filter  ·  g/G first/last  ·  q close"))
	return sb.String()
}

func (m tuiModel) viewEventDetailScreen() string {
	ev := m.evtDetailView
	if ev == nil {
		return ""
	}
	sid := m.selSessionID()
	state := m.states[sid]
	agentName := ""
	for _, a := range state.Agents {
		if a.ID == ev.AgentID {
			agentName = a.Name
			break
		}
	}
	taskTitle := ""
	for _, t := range state.Tasks {
		if t.ID == ev.TaskID {
			taskTitle = t.Title
			break
		}
	}

	var sb strings.Builder
	sb.WriteString(m.viewHUD() + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("Event Detail") + "\n\n")

	meta := func(label, val string) string {
		if val == "" {
			return ""
		}
		return lipgloss.NewStyle().Foreground(colorText).Render(label+": ") +
			lipgloss.NewStyle().Foreground(colorTeal).Render(val) + "\n"
	}
	sb.WriteString(meta("Type   ", ev.Type))
	sb.WriteString(meta("Time   ", time.Unix(ev.Ts, 0).Format("2006-01-02 15:04:05")))
	if agentName != "" {
		sb.WriteString(meta("Agent  ", agentName))
	}
	if taskTitle != "" {
		sb.WriteString(meta("Task   ", taskTitle))
	}
	sb.WriteString("\n")

	if ev.Payload == "" {
		sb.WriteString(dimStyle.Render("  (no payload)") + "\n")
	} else {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorText).Render("Payload:") + "\n")
		// Word-wrap payload to terminal width
		wrapW := m.w - 4
		if wrapW < 40 {
			wrapW = 40
		}
		wrapped := wordWrap(ev.Payload, wrapW)
		for _, line := range strings.Split(wrapped, "\n") {
			sb.WriteString("  " + line + "\n")
		}
	}

	sb.WriteString("\n" + dimStyle.Render("  Esc / q to return to event log"))
	return sb.String()
}

// ─── Icinga monitor view ──────────────────────────────────────────────────────

func (m tuiModel) updateIcingaView(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	// Non-OK services (top pane data) and recent alerts (bottom pane — same set sorted by time)
	nonOK := icingaNonOK(m.icingaServices)
	n := len(m.icingaServices)
	nb := len(nonOK)

	switch msg.String() {
	case "q", "esc":
		m.icingaView = false
	case "tab":
		m.icingaFocus = 1 - m.icingaFocus
	case "r", "R":
		return m, []tea.Cmd{m.client.get("icinga", "/api/icinga/services")}
	case "j", "down":
		if m.icingaFocus == 0 {
			if m.icingaTopCur < n-1 {
				m.icingaTopCur++
			}
		} else {
			if m.icingaBotCur < nb-1 {
				m.icingaBotCur++
			}
		}
	case "k", "up":
		if m.icingaFocus == 0 {
			if m.icingaTopCur > 0 {
				m.icingaTopCur--
			}
		} else {
			if m.icingaBotCur > 0 {
				m.icingaBotCur--
			}
		}
	case "g":
		if m.icingaFocus == 0 {
			m.icingaTopCur = 0
		} else {
			m.icingaBotCur = 0
		}
	case "G":
		if m.icingaFocus == 0 {
			m.icingaTopCur = max(0, n-1)
		} else {
			m.icingaBotCur = max(0, nb-1)
		}
	}
	return m, nil
}

// icingaNonOK returns non-OK services sorted by last_change descending (most recent alert first).
func icingaNonOK(svcs []IcingaService) []IcingaService {
	var out []IcingaService
	for _, s := range svcs {
		if s.State != 0 {
			out = append(out, s)
		}
	}
	// Sort by last_change desc (most recently fired first).
	for i := 0; i < len(out)-1; i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].LastChange > out[i].LastChange {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func icingaStateLabel(state, stateType int, acked, downtime bool) string {
	label := map[int]string{0: "OK", 1: "WARN", 2: "CRIT", 3: "UNKN"}[state]
	if stateType == 0 {
		label += "(SOFT)"
	}
	color := map[int]lipgloss.Color{
		0: colorGreen, 1: colorOrange, 2: colorRed, 3: colorDim,
	}[state]
	s := lipgloss.NewStyle().Foreground(color).Bold(state == 2).Render(label)
	if acked {
		s += dimStyle.Render("✓")
	}
	if downtime {
		s += dimStyle.Render("⏸")
	}
	return s
}

func (m tuiModel) viewIcingaScreen() string {
	svcs := m.icingaServices
	alerts := icingaNonOK(svcs)

	// Counts for header.
	var nCrit, nWarn, nUnkn, nOK int
	for _, s := range svcs {
		switch s.State {
		case 2:
			nCrit++
		case 1:
			nWarn++
		case 3:
			nUnkn++
		default:
			nOK++
		}
	}

	var sb strings.Builder
	sb.WriteString(m.viewHUD() + "\n")

	// ── Header ──
	hdr := lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("Icinga Services")
	if svcs == nil {
		hdr += dimStyle.Render("  Loading…")
	} else {
		hdr += "  " +
			lipgloss.NewStyle().Foreground(colorRed).Render(fmt.Sprintf("%d CRIT", nCrit)) + "  " +
			lipgloss.NewStyle().Foreground(colorOrange).Render(fmt.Sprintf("%d WARN", nWarn)) + "  " +
			lipgloss.NewStyle().Foreground(colorDim).Render(fmt.Sprintf("%d UNKN", nUnkn)) + "  " +
			lipgloss.NewStyle().Foreground(colorGreen).Render(fmt.Sprintf("%d OK", nOK)) +
			dimStyle.Render(fmt.Sprintf("  (%d total)", len(svcs)))
	}
	sb.WriteString(hdr + "\n\n")

	// Split the available height: subtract HUD (2) + header (2) + divider (1) + help (1).
	bodyH := m.h - 6
	if bodyH < 8 {
		bodyH = 8
	}
	topH := bodyH * 60 / 100
	botH := bodyH - topH

	topFocused := m.icingaFocus == 0
	botFocused := m.icingaFocus == 1

	focusBorder := func(focused bool) lipgloss.Color {
		if focused {
			return colorTeal
		}
		return colorDim
	}

	// ── Top pane: all services, sorted CRIT→WARN→UNKN→OK ──
	topTitle := lipgloss.NewStyle().Foreground(focusBorder(topFocused)).Render("All Services")
	sb.WriteString(topTitle + "\n")

	if svcs == nil {
		sb.WriteString(dimStyle.Render("  Fetching…") + "\n")
	} else if len(svcs) == 0 {
		sb.WriteString(dimStyle.Render("  No services found") + "\n")
	} else {
		visible := topH - 1
		if visible < 1 {
			visible = 1
		}
		start := m.icingaTopCur - visible/2
		if start < 0 {
			start = 0
		}
		end := start + visible
		if end > len(svcs) {
			end = len(svcs)
		}
		if end-start < visible && start > 0 {
			start = end - visible
			if start < 0 {
				start = 0
			}
		}
		nameW := max(20, m.w/5)
		hostW := max(14, m.w/8)
		outW := m.w - nameW - hostW - 18
		if outW < 10 {
			outW = 10
		}

		for i := start; i < end; i++ {
			s := svcs[i]
			hostStr := truncStr(s.Host, hostW)
			svcStr := truncStr(s.Service, nameW)
			outStr := truncStr(s.Output, outW)
			rawLine := fmt.Sprintf("%-*s  %-*s  %s", hostW, hostStr, nameW, svcStr, outStr)
			if i == m.icingaTopCur && topFocused {
				rawLine = lipgloss.NewStyle().Background(lipgloss.Color("#1a3050")).Render(rawLine)
			}
			sb.WriteString("  " + icingaStateLabel(s.State, s.StateType, s.Acked, s.Downtime) + "  " + rawLine + "\n")
		}
		if len(svcs) > visible {
			sb.WriteString(dimStyle.Render(fmt.Sprintf("  %d/%d", m.icingaTopCur+1, len(svcs))) + "\n")
		}
	}

	// ── Divider ──
	divColor := colorDim
	sb.WriteString(lipgloss.NewStyle().Foreground(divColor).Render(strings.Repeat("─", m.w)) + "\n")

	// ── Bottom pane: recent non-OK alerts ──
	botTitle := lipgloss.NewStyle().Foreground(focusBorder(botFocused)).Render(
		fmt.Sprintf("Recent Alerts (%d non-OK)", len(alerts)),
	)
	sb.WriteString(botTitle + "\n")

	if len(alerts) == 0 && svcs != nil {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorGreen).Render("  ✓ All services OK") + "\n")
	} else {
		visible := botH - 2
		if visible < 1 {
			visible = 1
		}
		start := m.icingaBotCur - visible/2
		if start < 0 {
			start = 0
		}
		end := start + visible
		if end > len(alerts) {
			end = len(alerts)
		}
		if end-start < visible && start > 0 {
			start = end - visible
			if start < 0 {
				start = 0
			}
		}
		nameW := max(20, m.w/5)
		hostW := max(14, m.w/8)
		outW := m.w - nameW - hostW - 28
		if outW < 10 {
			outW = 10
		}

		for i := start; i < end; i++ {
			s := alerts[i]
			ts := time.Unix(s.LastChange, 0).Format("01-02 15:04")
			hostStr := truncStr(s.Host, hostW)
			svcStr := truncStr(s.Service, nameW)
			outStr := truncStr(s.Output, outW)
			rawLine := fmt.Sprintf("%s  %-*s  %-*s  %s", ts, hostW, hostStr, nameW, svcStr, outStr)
			if i == m.icingaBotCur && botFocused {
				rawLine = lipgloss.NewStyle().Background(lipgloss.Color("#1a3050")).Render(rawLine)
			}
			sb.WriteString("  " + icingaStateLabel(s.State, s.StateType, s.Acked, s.Downtime) + "  " + rawLine + "\n")
		}
		if len(alerts) > visible {
			sb.WriteString(dimStyle.Render(fmt.Sprintf("  %d/%d", m.icingaBotCur+1, len(alerts))) + "\n")
		}
	}

	sb.WriteString("\n" + dimStyle.Render("  Tab switch-pane  ·  j/k navigate  ·  g/G first/last  ·  r refresh  ·  q close"))
	return sb.String()
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
	sb.WriteString("    A unit of work assigned to one agent. Lifecycle:\n")
	sb.WriteString("    " +
		badge("spec", colorDim) + " → " +
		badge("queued", colorBlue) + " → " +
		badge("assigned", colorTeal) + " → " +
		badge("running", colorGreen) + " → " +
		badge("review", colorOrange) + " → " +
		badge("deploy", colorOrange) + " → " +
		badge("done", colorGreen) + "\n")
	sb.WriteString(dim("    Blocked tasks auto-revert to queued. Auto-dispatch fires immediately\n"))
	sb.WriteString(dim("    when a task is created or an idle agent becomes available.\n") + "\n")

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
	sb.WriteString(key("↑↓  j k", "Move sidebar cursor") + "\n")
	sb.WriteString(key("Enter", "Attach to agent's tmux session") + "\n")
	sb.WriteString(key("Tab  /", "Focus chat / message bar") + "\n")
	sb.WriteString(key("q", "Quit") + "\n\n")

	sb.WriteString(h("ACTIONS") + "\n")
	sb.WriteString(key("s", "Spawn agent (opens browser)") + "\n")
	sb.WriteString(key("d  d", "Stop agent (press twice to confirm)") + "\n")
	sb.WriteString(key("+", "Quick-spawn a worker (name only)") + "\n")
	sb.WriteString(key("n", "New agent (full form)") + "\n")
	sb.WriteString(key("t", "New task") + "\n")
	sb.WriteString(key("c", "New session") + "\n")
	sb.WriteString(key("E", "Edit selected session / agent / task") + "\n")
	sb.WriteString(key("A", "Toggle autopilot (Plane sync)") + "\n\n")

	sb.WriteString(h("VIEWS") + "\n")
	sb.WriteString(key("I", "Icinga monitor — services + recent alerts") + "\n")
	sb.WriteString(key("L", "Event log — full retrospective, agent filter, detail") + "\n")
	sb.WriteString(key("e", "Escalations — pending human-in-the-loop requests") + "\n")
	sb.WriteString(key("g", "Goals — goal status + budget tracking") + "\n")
	sb.WriteString(key("W", "Work queue — Plane backlog") + "\n")
	sb.WriteString(key("R", "Refresh all data") + "\n\n")

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
	sb.WriteString(dimStyle.Render("Tab/↑↓ next field  ·  Enter confirm  ·  Esc cancel"))
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

func (m tuiModel) viewEscalationScreen() string {
	sid := m.selSessionID()
	var escs []tuiEscalation
	if st, ok := m.states[sid]; ok {
		escs = st.Escalations
	}

	var sb strings.Builder
	sb.WriteString(m.viewHUD() + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(colorOrange).Bold(true).
		Render("  ⚠ ESCALATIONS — pending human responses") + "\n\n")

	if len(escs) == 0 {
		sb.WriteString(dimStyle.Render("  No pending escalations") + "\n")
	} else {
		for i, esc := range escs {
			sel := i == m.escCursor
			prefix := "  "
			style := dimStyle
			if sel {
				prefix = lipgloss.NewStyle().Foreground(colorOrange).Render("▶ ")
				style = lipgloss.NewStyle().Foreground(colorText)
			}
			ts := time.Unix(esc.Ts, 0).Format("15:04")
			maxReason := m.w - 30
			if maxReason < 20 {
				maxReason = 20
			}
			line := fmt.Sprintf("[%s] task:%-8s  %s", ts, truncStr(esc.TaskID, 8), truncStr(esc.Reason, maxReason))
			sb.WriteString(prefix + style.Render(line) + "\n")
		}
	}

	sb.WriteString("\n")
	if m.escActive != nil {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorOrange).Render("  Responding to: "+truncStr(m.escActive.Reason, m.w-20)) + "\n")
		sb.WriteString("  " + m.escInput.View() + "\n")
		sb.WriteString(dimStyle.Render("  Enter to send  ·  Esc to cancel") + "\n")
	} else if len(escs) > 0 {
		sb.WriteString(dimStyle.Render("  Enter to respond  ·  ↑↓/jk navigate") + "\n")
	}
	sb.WriteString("\n" + dimStyle.Render("  e/Esc/q — close escalation view"))
	return sb.String()
}

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
	case "complete":
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
	case "complete":
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

// ─── Goals view ──────────────────────────────────────────────────────────────

func (m tuiModel) updateGoalView(msg tea.KeyMsg) (tuiModel, []tea.Cmd) {
	sid := m.selSessionID()
	goals := m.states[sid].Goals
	switch msg.String() {
	case "g", "esc", "q":
		m.goalView = false
	case "up", "k":
		if m.goalCursor > 0 {
			m.goalCursor--
		}
	case "down", "j":
		if m.goalCursor < len(goals)-1 {
			m.goalCursor++
		}
	}
	return m, nil
}

// goalTaskStats returns (total, done) task counts for a goal.
func (m tuiModel) goalTaskStats(sid, goalID string) (total, done int) {
	st := m.states[sid]
	for _, t := range st.Tasks {
		if t.GoalID != nil && *t.GoalID == goalID {
			total++
			if t.Stage == "complete" || t.Stage == "failed" || t.Stage == "cancelled" || t.Stage == "timed_out" {
				done++
			}
		}
	}
	return
}

func (m tuiModel) viewGoalsScreen() string {
	sid := m.selSessionID()
	if sid == "" {
		return dimStyle.Width(m.w).Render("No session selected")
	}
	goals := m.states[sid].Goals
	var sb strings.Builder
	title := lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Width(m.w).Render("  Goals  (↑↓/jk navigate · g/esc close)")
	sb.WriteString(title + "\n")
	sb.WriteString(dimStyle.Render(strings.Repeat("─", m.w)) + "\n")

	if len(goals) == 0 {
		sb.WriteString(dimStyle.Width(m.w).Render("  No goals yet. Use /goal <description> in the chat input.") + "\n")
	}
	for i, g := range goals {
		sel := i == m.goalCursor
		var statusC lipgloss.Color
		var statusIcon string
		switch g.Status {
		case "complete":
			statusC, statusIcon = colorGreen, "✓"
		case "failed":
			statusC, statusIcon = colorRed, "✗"
		case "cancelled":
			statusC, statusIcon = colorDim, "○"
		default:
			statusC, statusIcon = colorTeal, "▶"
		}
		total, done := m.goalTaskStats(sid, g.ID)
		progress := fmt.Sprintf("%d/%d tasks", done, total)

		// Complexity badge
		complexityBadge := ""
		switch g.Complexity {
		case "trivial":
			complexityBadge = dimStyle.Render(" (trv)")
		case "complex":
			complexityBadge = lipgloss.NewStyle().Foreground(colorOrange).Render(" (cpx)")
		}

		// Budget bar (only shown when budget is set)
		budgetBar := ""
		if g.TokenBudget > 0 {
			pct := float64(g.TokensUsed) / float64(g.TokenBudget)
			filled := int(pct * 8)
			if filled > 8 {
				filled = 8
			}
			barColor := colorGreen
			if pct >= 1.0 {
				barColor = colorRed
			} else if pct >= 0.8 {
				barColor = colorOrange
			}
			bar := strings.Repeat("█", filled) + strings.Repeat("░", 8-filled)
			budgetBar = " " + lipgloss.NewStyle().Foreground(barColor).Render(fmt.Sprintf("[%s %3.0f%%]", bar, pct*100))
		}

		descWidth := m.w - 28
		if descWidth < 10 {
			descWidth = 10
		}
		prefix := fmt.Sprintf("  %s  %-*s%s  %s%s",
			lipgloss.NewStyle().Foreground(statusC).Render(statusIcon),
			descWidth,
			truncStr(g.Description, descWidth),
			complexityBadge,
			dimStyle.Render(progress),
			budgetBar,
		)
		row := lipgloss.NewStyle()
		if sel {
			row = row.Background(colorSubtle).Foreground(colorText)
		}
		sb.WriteString(row.Width(m.w).Render(prefix) + "\n")

		// If selected, expand phase tasks below
		if sel && total > 0 {
			st := m.states[sid]
			for _, t := range st.Tasks {
				if t.GoalID == nil || *t.GoalID != g.ID {
					continue
				}
				stageC := tuiStageColor(t.Stage)
				phaseLabel := ""
				if t.Phase != nil {
					phaseLabel = fmt.Sprintf("%-12s", *t.Phase)
				}
				taskRow := fmt.Sprintf("      %s  %s  %s",
					lipgloss.NewStyle().Foreground(stageC).Render(shortStage(t.Stage)),
					dimStyle.Render(phaseLabel),
					truncStr(t.Title, m.w-30),
				)
				sb.WriteString(dimStyle.Width(m.w).Render(taskRow) + "\n")
			}
		}
	}

	sb.WriteString(dimStyle.Render(strings.Repeat("─", m.w)) + "\n")
	return sb.String()
}

// ─── Entry Point ─────────────────────────────────────────────────────────────

func RunSwarmTUI() {
	p := tea.NewProgram(newTUIModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "swarm TUI error: %v\n", err)
		os.Exit(1)
	}
}
