package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gorilla/websocket"
)

// ─── Client interface ─────────────────────────────────────────────────────────

// TUIClient is the interface implemented by *swarmClient.
// Tests inject a fakeClient through this interface to avoid network I/O.
type TUIClient interface {
	fetchAll() tea.Cmd
	fetchTerminal(sid, agentID string) tea.Cmd
	fetchGitStatus(sid, agentID string) tea.Cmd
	fetchNotes(sid, agentID string) tea.Cmd
	post(op, path string, body interface{}) tea.Cmd
	patch(op, path string, body interface{}) tea.Cmd
	get(op, path string) tea.Cmd
	deleteItem(op, path string) tea.Cmd
	putSync(path string, body []byte) error
	getSync(path string) ([]byte, error)
	postSync(path string, body []byte) ([]byte, error)
	deleteSync(path string) error
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

// getSync performs a synchronous GET and returns the body bytes.
func (c *swarmClient) getSync(path string) ([]byte, error) {
	b, _, err := c.do("GET", path, nil)
	return b, err
}

// postSync performs a synchronous POST and returns the response body.
func (c *swarmClient) postSync(path string, body []byte) ([]byte, error) {
	b, status, err := c.do("POST", path, json.RawMessage(body))
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("POST %s: %d %s", path, status, string(b))
	}
	return b, nil
}

// deleteSync performs a synchronous DELETE.
func (c *swarmClient) deleteSync(path string) error {
	b, status, err := c.do("DELETE", path, nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("DELETE %s: %d %s", path, status, string(b))
	}
	return nil
}

// putSync performs a synchronous PUT with a raw JSON body.
func (c *swarmClient) putSync(path string, body []byte) error {
	req, err := http.NewRequest("PUT", c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PUT %s: %d %s", path, resp.StatusCode, string(b))
	}
	return nil
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
		// Use bulk endpoint to fetch all session states in one call instead of 1+N round-trips
		states := make(map[string]tuiState, len(dash.Sessions))
		if len(dash.Sessions) > 0 {
			ids := make([]string, 0, len(dash.Sessions))
			for _, s := range dash.Sessions {
				ids = append(ids, s.ID)
			}
			b2, st2, err2 := c.do("GET", "/api/swarm/sessions/bulk?ids="+strings.Join(ids, ","), nil)
			if err2 == nil && st2 == 200 {
				var bulk map[string]tuiState
				if json.Unmarshal(b2, &bulk) == nil {
					states = bulk
				}
			} else {
				// Fallback: per-session requests if bulk unavailable
				for _, s := range dash.Sessions {
					b3, st3, err3 := c.do("GET", "/api/swarm/sessions/"+s.ID, nil)
					if err3 != nil || st3 != 200 {
						continue
					}
					var st tuiState
					if json.Unmarshal(b3, &st) == nil {
						states[s.ID] = st
					}
				}
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

func (c *swarmClient) deleteItem(op, path string) tea.Cmd {
	return func() tea.Msg {
		_, status, err := c.do("DELETE", path, nil)
		if err != nil {
			return tuiErrMsg{op: op, text: err.Error()}
		}
		if status >= 400 {
			return tuiErrMsg{op: op, text: fmt.Sprintf("HTTP %d", status)}
		}
		return tuiDoneMsg{op: op}
	}
}

func (c *swarmClient) fetchNotes(sid, agentID string) tea.Cmd {
	return func() tea.Msg {
		b, status, err := c.do("GET", "/api/swarm/sessions/"+sid+"/agents/"+agentID+"/note", nil)
		if err != nil || status != 200 {
			return tuiErrMsg{op: "fetch-notes", text: "failed to load notes"}
		}
		var notes []agentNote
		if json.Unmarshal(b, &notes) != nil {
			return tuiErrMsg{op: "fetch-notes", text: "parse error"}
		}
		return tuiNotesMsg{agentID: agentID, items: notes}
	}
}

func (c *swarmClient) fetchGitStatus(sid, agentID string) tea.Cmd {
	return func() tea.Msg {
		b, status, err := c.do("GET", "/api/swarm/sessions/"+sid+"/agents/"+agentID+"/git", nil)
		if err != nil || status != 200 {
			return tuiDoneMsg{op: "git-status"} // silent — git unavailable is normal
		}
		var gs tuiGitStatus
		if json.Unmarshal(b, &gs) != nil {
			return tuiDoneMsg{op: "git-status"}
		}
		return tuiGitStatusMsg{agentID: agentID, status: gs}
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
