package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	tea "github.com/charmbracelet/bubbletea"
)

// ─── Feedback capture ─────────────────────────────────────────────────────────

const feedbackViewportCap = 4 * 1024 // 4 KB max from viewport snapshot

type tuiFeedbackCapture struct {
	viewportContent string     // ANSI-stripped viewport text (capped at feedbackViewportCap)
	sessionName     string
	agentName       string     // empty when a session (not agent) is selected
	recentEvents    []tuiEvent // last 10 events from the active session
	capturedAt      time.Time
}

type tuiFeedbackResultMsg struct {
	seqID int    // Plane sequence_id of the created issue
	err   error
}

// captureFeedbackState builds a structured snapshot of the current TUI state.
// It must NOT call m.View() — that would be slow and include rendering artefacts.
func captureFeedbackState(m *tuiModel) tuiFeedbackCapture {
	fc := tuiFeedbackCapture{capturedAt: time.Now()}

	// Session name
	sid := m.selSessionID()
	if sid != "" {
		if st, ok := m.states[sid]; ok {
			fc.sessionName = st.Session.Name
		}
	}

	// Selected agent name
	if it := m.selItem(); it != nil && it.kind == tuiItemAgent {
		if agent := m.lookupAgent(it.sid, it.eid); agent != nil {
			fc.agentName = agent.Name
		}
	}

	// Viewport content — take the last lines from the active session log,
	// strip ANSI codes, and cap at feedbackViewportCap bytes.
	if sid != "" {
		lines := m.vpLines[sid]
		raw := strings.Join(lines, "\n")
		stripped := ansi.Strip(raw)
		if len(stripped) > feedbackViewportCap {
			stripped = "…(truncated)\n" + stripped[len(stripped)-feedbackViewportCap:]
		}
		fc.viewportContent = stripped
	}

	// Last 10 raw events from the active session
	if sid != "" {
		evts := m.vpRawEvents[sid]
		start := 0
		if len(evts) > 10 {
			start = len(evts) - 10
		}
		fc.recentEvents = make([]tuiEvent, len(evts)-start)
		copy(fc.recentEvents, evts[start:])
	}

	return fc
}

// buildFeedbackDescription assembles the HTML body for the Plane issue.
func buildFeedbackDescription(fc tuiFeedbackCapture, userDetails string) string {
	var sb strings.Builder
	sb.WriteString("<p><strong>SwarmOps TUI Feedback</strong></p>")
	sb.WriteString(fmt.Sprintf("<p><em>Captured: %s</em></p>", fc.capturedAt.Format("2006-01-02 15:04:05")))
	sb.WriteString("<hr>")

	if userDetails != "" {
		sb.WriteString("<p><strong>Details:</strong></p>")
		sb.WriteString("<p>" + fbEscape(userDetails) + "</p>")
		sb.WriteString("<hr>")
	}

	// Context
	sb.WriteString("<p><strong>Context:</strong></p><ul>")
	if fc.sessionName != "" {
		sb.WriteString("<li>Session: " + fbEscape(fc.sessionName) + "</li>")
	}
	if fc.agentName != "" {
		sb.WriteString("<li>Agent: " + fbEscape(fc.agentName) + "</li>")
	}
	sb.WriteString("</ul>")

	// Recent events
	if len(fc.recentEvents) > 0 {
		sb.WriteString("<p><strong>Recent Events:</strong></p><ul>")
		for _, ev := range fc.recentEvents {
			ts := time.Unix(ev.Ts, 0).Format("15:04:05")
			sb.WriteString(fmt.Sprintf("<li>[%s] %s: %s</li>",
				ts, fbEscape(ev.Type), fbEscape(truncStr(ev.Payload, 120))))
		}
		sb.WriteString("</ul>")
	}

	// Viewport snapshot
	if fc.viewportContent != "" {
		sb.WriteString("<p><strong>Viewport Snapshot:</strong></p>")
		sb.WriteString("<pre>" + fbEscape(fc.viewportContent) + "</pre>")
	}

	return sb.String()
}

// fbEscape HTML-escapes a plain-text string for embedding in the description.
func fbEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// submitFeedbackCmd sends the feedback as a Plane issue and returns a tuiFeedbackResultMsg.
func submitFeedbackCmd(fc tuiFeedbackCapture, summary, details, issueType string) tea.Cmd {
	return func() tea.Msg {
		cfg, ok := loadPlaneConfig()
		if !ok {
			return tuiFeedbackResultMsg{err: fmt.Errorf("Plane not configured — set PLANE_API_URL/KEY/WORKSPACE/PROJECT_ID")}
		}

		// Allow a dedicated feedback project, falling back to PLANE_PROJECT_ID
		if fp := os.Getenv("SWARM_FEEDBACK_PLANE_PROJECT_ID"); fp != "" {
			cfg.projectID = fp
		}

		priority := "medium"
		if issueType == "bug" {
			priority = "high"
		}

		body := map[string]interface{}{
			"name":             summary,
			"description_html": buildFeedbackDescription(fc, details),
			"priority":         priority,
		}

		path := fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/issues/", cfg.workspace, cfg.projectID)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		data, status, err := planeReq(ctx, cfg, "POST", path, body)
		if err != nil {
			return tuiFeedbackResultMsg{err: fmt.Errorf("request failed: %w", err)}
		}
		if status != 200 && status != 201 {
			return tuiFeedbackResultMsg{err: fmt.Errorf("Plane returned %d: %s", status, truncStr(string(data), 120))}
		}

		var resp struct {
			SequenceID int `json:"sequence_id"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return tuiFeedbackResultMsg{err: fmt.Errorf("parse response: %w", err)}
		}
		return tuiFeedbackResultMsg{seqID: resp.SequenceID}
	}
}
