package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ─── IPC types ────────────────────────────────────────────────────────────────

// IPCEvent is a single line from an agent's outbox events.jsonl
type IPCEvent struct {
	Event       string  `json:"event"`
	MessageID   string  `json:"message_id,omitempty"`
	TaskID      string  `json:"task_id,omitempty"`
	Note        string  `json:"note,omitempty"`
	Reason      string  `json:"reason,omitempty"`
	Status      string  `json:"status,omitempty"`
	ContextPct  float64 `json:"context_pct,omitempty"`
	HandoffPath string   `json:"handoff_path,omitempty"`
	Content     string   `json:"content,omitempty"`
	ModelName   string   `json:"model_name,omitempty"`
	TokensUsed  int64    `json:"tokens_used,omitempty"`
	Ts          int64    `json:"ts,omitempty"`
	ScopePaths  []string `json:"scope_paths,omitempty"` // H1: file/dir prefixes agent will modify
}

// IPCArtifact describes a file produced by an agent task.
type IPCArtifact struct {
	Type    string `json:"type"`
	Path    string `json:"path"`
	Summary string `json:"summary,omitempty"`
}

// IPCRecommendedTask is a follow-up task recommended in a handoff.
type IPCRecommendedTask struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// IPCHandoff is the structured JSON written by an agent on task completion.
type IPCHandoff struct {
	SchemaVersion        string               `json:"schema_version"`
	TaskID               string               `json:"task_id"`
	AgentID              string               `json:"agent_id"`
	MessageID            string               `json:"message_id,omitempty"`
	Status               string               `json:"status"`
	Summary              string               `json:"summary"`
	FilesChanged         []string             `json:"files_changed,omitempty"`
	ArtifactsProduced    []IPCArtifact        `json:"artifacts_produced,omitempty"`
	Confidence           float64              `json:"confidence"`
	TestsPassed          bool                 `json:"tests_passed"`
	OpenQuestions        []string             `json:"open_questions,omitempty"`
	NextRecommendedTasks []IPCRecommendedTask `json:"next_recommended_tasks,omitempty"`
	TokensUsed           int64                `json:"tokens_used,omitempty"`
	ContextPct           float64              `json:"context_pct,omitempty"`
	CompletedAt          int64                `json:"completed_at"`

	// C3 — Context handoff fields (all omitempty for backward compatibility)
	ContextSummary     string   `json:"context_summary,omitempty"`
	CurrentDiffRef     string   `json:"current_diff_ref,omitempty"` // branch name
	DecisionsLog       []string `json:"decisions_log,omitempty"`
	FailingTests       []string `json:"failing_tests,omitempty"`
	NextSteps          []string `json:"next_steps,omitempty"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
}

// InboxMessage is written by the orchestrator into an agent's inbox dir.
type InboxMessage struct {
	SchemaVersion string   `json:"schema_version"`
	MessageID     string   `json:"message_id"`
	Type          string   `json:"type"` // task_assign | context_warning | handoff_prepare | task_cancel
	TaskID        string   `json:"task_id,omitempty"`
	ParentGoal    string   `json:"parent_goal,omitempty"`
	Objective     string   `json:"objective,omitempty"`
	Constraints   []string `json:"constraints,omitempty"`
	Action        string   `json:"action,omitempty"`
	WriteTo       string   `json:"write_to,omitempty"` // path to write handoff JSON
	BudgetMinutes int      `json:"budget_minutes,omitempty"`
	SentAt        int64    `json:"sent_at"`
}

// ─── Poller ───────────────────────────────────────────────────────────────────

func startIPCPoller() {
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			pollAllAgentOutboxes()
		}
	}()
}

func pollAllAgentOutboxes() {
	ctx := context.Background()
	rows, err := database.QueryContext(ctx,
		`SELECT id, session_id, COALESCE(outbox_path,''), last_event_offset
		 FROM swarm_agents
		 WHERE outbox_path IS NOT NULL AND status NOT IN ('idle','done')`,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	type agentRow struct {
		id, sessionID, outboxPath string
		offset                    int64
	}
	var agents []agentRow
	for rows.Next() {
		var a agentRow
		if err := rows.Scan(&a.id, &a.sessionID, &a.outboxPath, &a.offset); err != nil {
			log.Printf("ipc: scan agent row: %v", err)
			continue
		}
		agents = append(agents, a)
	}
	if err := rows.Err(); err != nil {
		log.Printf("ipc: agent outbox rows error: %v", err)
		return
	}

	for _, a := range agents {
		eventsFile := filepath.Join(a.outboxPath, "events.jsonl")
		newOffset, events := readNewEvents(eventsFile, a.offset)
		if newOffset == a.offset {
			continue
		}
		// Process events before persisting offset — crash-safe: re-processing is
		// idempotent via the task state machine's isValidTransition guard.
		for _, ev := range events {
			handleIPCEvent(ctx, a.sessionID, a.id, ev)
		}
		database.ExecContext(ctx, //nolint:errcheck
			"UPDATE swarm_agents SET last_event_offset=?, last_event_ts=? WHERE id=?",
			newOffset, time.Now().Unix(), a.id,
		)
	}
}

// ipcMaxLineBytes is the maximum size of a single JSONL event line.
// The default bufio.Scanner limit is 64KB which is too small for large agent events.
const ipcMaxLineBytes = 4 << 20 // 4MB

// readNewEvents reads lines from path starting at byteOffset, returns new offset + parsed events.
func readNewEvents(path string, offset int64) (newOffset int64, events []IPCEvent) {
	f, err := os.Open(path)
	if err != nil {
		return offset, nil
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil || fi.Size() <= offset {
		return offset, nil
	}

	if _, err := f.Seek(offset, 0); err != nil {
		return offset, nil
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), ipcMaxLineBytes)
	pos := offset
	for scanner.Scan() {
		line := scanner.Text()
		pos += int64(len(line)) + 1 // +1 for newline
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev IPCEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			log.Printf("ipc: bad event line: %v — %q", err, line)
			continue
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("ipc: scanner error in %s at offset %d: %v", path, pos, err)
	}
	return pos, events
}

// ─── Event dispatch ───────────────────────────────────────────────────────────

func handleIPCEvent(ctx context.Context, sessionID, agentID string, ev IPCEvent) {
	log.Printf("ipc: event %q from agent %s task=%s", ev.Event, agentID[:8], ev.TaskID)

	switch ev.Event {
	case "task_accepted":
		if ev.TaskID != "" {
			params := AcceptTaskParams{ScopePaths: ev.ScopePaths}
			if err := AcceptTaskWithParams(ctx, ev.TaskID, ev.MessageID, params); err != nil {
				log.Printf("ipc: AcceptTask error: %v", err)
				// H1: Scope conflict — notify orchestrator so it can back off and retry.
				var scopeErr ErrScopeConflict
				if errors.As(err, &scopeErr) {
					msg := InboxMessage{
						Type:      "scope_conflict",
						MessageID: generateSwarmID(),
						Action:    fmt.Sprintf("Task %s could not acquire repo lease: %s. Back off and retry when the conflicting task completes.", ev.TaskID[:8], err.Error()),
					}
					// Find the orchestrator agent to notify.
					var orchID string
					database.QueryRowContext(ctx,
						`SELECT id FROM swarm_agents WHERE session_id=? AND role='orchestrator' AND tmux_session IS NOT NULL LIMIT 1`,
						sessionID,
					).Scan(&orchID) //nolint:errcheck
					if orchID != "" {
						writeAgentInboxMsg(ctx, sessionID, orchID, msg) //nolint:errcheck
					}
				}
			} else {
				writeSwarmEvent(ctx, sessionID, agentID, ev.TaskID, "task_accepted", ev.Note)
			}
			swarmBroadcaster.schedule(sessionID)
		}

	case "task_progress":
		if ev.TaskID != "" {
			if err := StartTask(ctx, ev.TaskID); err != nil {
				log.Printf("ipc: StartTask error: %v", err)
			}
			if ev.Note != "" {
				note := ev.Note
				if len(note) > 2000 {
					note = note[:2000]
				}
				database.ExecContext(ctx, //nolint:errcheck
					"INSERT INTO swarm_agent_notes (agent_id, session_id, content, created_by, created_at) VALUES (?,?,?,?,?)",
					agentID, sessionID, note, "agent", time.Now().Unix(),
				)
			}
			writeSwarmEvent(ctx, sessionID, agentID, ev.TaskID, "task_progress", ev.Note)
			swarmBroadcaster.schedule(sessionID)
		}

	case "task_blocked":
		if ev.TaskID != "" {
			if err := BlockTask(ctx, sessionID, agentID, ev.TaskID, ev.Reason); err != nil {
				log.Printf("ipc: BlockTask error: %v", err)
			}
			writeSwarmEvent(ctx, sessionID, agentID, ev.TaskID, "task_blocked", ev.Reason)
			swarmBroadcaster.schedule(sessionID)
		}

	case "task_complete":
		if ev.HandoffPath != "" {
			// Constrain path to the agent's own outbox directory to prevent
			// a compromised agent reading/deleting arbitrary server files.
			expectedDir := filepath.Clean(swarmOutboxDir(sessionID, agentID))
			cleanPath := filepath.Clean(ev.HandoffPath)
			if !strings.HasPrefix(cleanPath, expectedDir+string(filepath.Separator)) {
				log.Printf("ipc: rejected handoff path outside outbox: %q (agent %s)", ev.HandoffPath, agentID[:8])
				break
			}
			if err := processHandoffFile(ctx, sessionID, agentID, cleanPath); err != nil {
				log.Printf("ipc: processHandoff error: %v", err)
			}
			writeSwarmEvent(ctx, sessionID, agentID, ev.TaskID, "task_complete", "")
		}

	case "heartbeat":
		handleHeartbeat(ctx, sessionID, agentID, ev)

	case "decision_made":
		if ev.Content != "" {
			if err := appendDecision(ctx, sessionID, agentID, ev.Content); err != nil {
				log.Printf("ipc: appendDecision error: %v", err)
			}
		}

	default:
		log.Printf("ipc: unknown event type %q from %s", ev.Event, agentID[:8])
	}
}

// ─── Handoff processing ───────────────────────────────────────────────────────

func processHandoffFile(ctx context.Context, sessionID, agentID, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read handoff file: %w", err)
	}

	var h IPCHandoff
	if err := json.Unmarshal(data, &h); err != nil {
		return fmt.Errorf("parse handoff: %w", err)
	}

	if h.TaskID == "" {
		return fmt.Errorf("handoff missing task_id")
	}

	// Validate handoff completeness (non-blocking — warnings only).
	if warnings := validateHandoff(h); len(warnings) > 0 {
		log.Printf("ipc: handoff task=%s warnings: %v", h.TaskID[:8], warnings)
		// Store warnings in blocked_reason for visibility (overwritten on CompleteTask).
		warnText := strings.Join(warnings, "; ")
		database.ExecContext(ctx, //nolint:errcheck
			"UPDATE swarm_tasks SET blocked_reason=? WHERE id=?",
			"handoff warnings: "+warnText, h.TaskID,
		)
	}

	// Remove handoff file only after CompleteTask succeeds — preserves the file
	// if processing fails so the next poll cycle can retry.
	if err := CompleteTask(ctx, sessionID, agentID, h.TaskID, h); err != nil {
		return err
	}

	// Store handoff data in swarm_task_handoffs for continuation agent enrichment.
	storeTaskHandoff(ctx, h)

	os.Remove(path) //nolint:errcheck
	return nil
}

// validateHandoff returns warnings for missing required handoff fields.
// These are non-blocking — logged and stored for visibility but do not block completion.
func validateHandoff(h IPCHandoff) []string {
	var warnings []string
	if h.ContextSummary == "" {
		warnings = append(warnings, "missing context_summary")
	}
	if len(h.NextSteps) == 0 {
		warnings = append(warnings, "missing next_steps")
	}
	return warnings
}

// storeTaskHandoff persists the C3 handoff fields to swarm_task_handoffs.
// Called after CompleteTask succeeds. Uses INSERT OR REPLACE for idempotency.
func storeTaskHandoff(ctx context.Context, h IPCHandoff) {
	if h.ContextSummary == "" && len(h.NextSteps) == 0 &&
		len(h.DecisionsLog) == 0 && len(h.FailingTests) == 0 {
		return // nothing to store — old-style handoff without C3 fields
	}
	nextSteps := jsonEncodeStrings(h.NextSteps)
	decisionsLog := jsonEncodeStrings(h.DecisionsLog)
	failingTests := jsonEncodeStrings(h.FailingTests)
	acceptanceCriteria := jsonEncodeStrings(h.AcceptanceCriteria)
	_, err := database.ExecContext(ctx,
		`INSERT OR REPLACE INTO swarm_task_handoffs
		 (task_id, context_summary, current_diff_ref, next_steps, decisions_log,
		  failing_tests, acceptance_criteria, confidence, created_at)
		 VALUES (?,?,?,?,?,?,?,?,unixepoch())`,
		h.TaskID, h.ContextSummary, h.CurrentDiffRef,
		nextSteps, decisionsLog, failingTests, acceptanceCriteria, h.Confidence,
	)
	if err != nil {
		log.Printf("ipc: storeTaskHandoff failed task=%s: %v", h.TaskID[:8], err)
	}
}

// jsonEncodeStrings encodes a string slice as a JSON array, or returns "[]" on error.
func jsonEncodeStrings(ss []string) string {
	if ss == nil {
		return "[]"
	}
	b, err := json.Marshal(ss)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// ─── Heartbeat / context management ──────────────────────────────────────────

const (
	ctxPctWarning      = 0.70
	ctxPctRotate       = 0.85
	ctxPctEmergency    = 0.95
	emergencyKillDelay = 30 * time.Second
)

func handleHeartbeat(ctx context.Context, sessionID, agentID string, ev IPCEvent) {
	pct := ev.ContextPct
	log.Printf("ipc: heartbeat agent=%s context_pct=%.2f", agentID[:8], pct)

	// Persist context_pct on agent
	database.ExecContext(ctx, //nolint:errcheck
		"UPDATE swarm_agents SET context_pct=? WHERE id=?",
		pct, agentID,
	)

	// Persist model_name and tokens_used when present
	if ev.ModelName != "" {
		database.ExecContext(ctx, //nolint:errcheck
			"UPDATE swarm_agents SET model_name=? WHERE id=?",
			ev.ModelName, agentID)
	}
	if ev.TokensUsed > 0 {
		database.ExecContext(ctx, //nolint:errcheck
			"UPDATE swarm_agents SET tokens_used=? WHERE id=?",
			ev.TokensUsed, agentID)
	}

	// Update last_heartbeat_at on the active task (for watchdog liveness tracking)
	if ev.TaskID != "" {
		database.ExecContext(ctx, //nolint:errcheck
			"UPDATE swarm_tasks SET last_heartbeat_at=? WHERE id=? AND stage IN ('running','accepted')",
			time.Now().Unix(), ev.TaskID,
		)
	}

	switch {
	case pct >= ctxPctEmergency:
		// Guard against duplicate emergency goroutines: check if already in emergency.
		var state string
		database.QueryRowContext(ctx, "SELECT COALESCE(context_state,'normal') FROM swarm_agents WHERE id=?", agentID).Scan(&state) //nolint:errcheck
		if state == "emergency" {
			return // goroutine already scheduled
		}
		database.ExecContext(ctx, //nolint:errcheck
			"UPDATE swarm_agents SET context_state='emergency' WHERE id=?", agentID)
		// Capture the current tmux session so the kill goroutine can verify
		// the agent hasn't already rotated by the time the timer fires.
		var capturedTmux string
		database.QueryRowContext(ctx, "SELECT COALESCE(tmux_session,'') FROM swarm_agents WHERE id=?", agentID).Scan(&capturedTmux) //nolint:errcheck
		log.Printf("ipc: EMERGENCY context %s — scheduling hard kill in %s", agentID[:8], emergencyKillDelay)
		go emergencyRotateAgent(ctx, sessionID, agentID, capturedTmux)

	case pct >= ctxPctRotate:
		// Graceful rotation: ask agent to produce handoff then we'll restart
		var state string
		database.QueryRowContext(ctx, "SELECT COALESCE(context_state,'normal') FROM swarm_agents WHERE id=?", agentID).Scan(&state)
		if state == "handoff_pending" || state == "emergency" {
			return // already in rotation
		}
		database.ExecContext(ctx, //nolint:errcheck
			"UPDATE swarm_agents SET context_state='handoff_pending' WHERE id=?", agentID)

		// Find the agent's current task to give the handoff path
		var taskID string
		database.QueryRowContext(ctx, "SELECT COALESCE(current_task_id,'') FROM swarm_agents WHERE id=?", agentID).Scan(&taskID)

		handoffPath := filepath.Join(swarmOutboxDir(sessionID, agentID), "handoff.json")
		msg := InboxMessage{
			SchemaVersion: "1",
			MessageID:     generateSwarmID(),
			Type:          "handoff_prepare",
			TaskID:        taskID,
			Action: fmt.Sprintf(`Context at %.0f%%. Write a complete handoff JSON to %s NOW, then emit task_complete.

Required fields (a continuation agent WILL read these):
  "context_summary"     — What has been done, what state things are in (required)
  "next_steps"          — Ordered list of what to do next (required, JSON array)
  "current_diff_ref"    — Your branch name so the continuation agent can git diff
  "decisions_log"       — Key decisions made and why (JSON array)
  "failing_tests"       — Tests currently failing and why (JSON array)
  "acceptance_criteria" — Criteria from spec that still need verification (JSON array)
  "confidence"          — How complete the work is (0.0–1.0)
  "tests_passed"        — true if all tests pass, false otherwise
  "summary"             — Brief summary of overall progress
  "task_id"             — "%s" (required)
  "schema_version"      — "1"

Write this file, then emit: {"event":"task_complete","task_id":"%s","handoff_path":"%s","ts":<unix>}`,
				pct*100, handoffPath, taskID, taskID, handoffPath),
			WriteTo: handoffPath,
			SentAt:  time.Now().Unix(),
		}
		writeAgentInboxMsg(ctx, sessionID, agentID, msg) //nolint:errcheck
		writeSwarmEvent(ctx, sessionID, agentID, taskID, "context_rotation_requested", fmt.Sprintf("%.0f%%", pct*100))
		swarmBroadcaster.schedule(sessionID)

	case pct >= ctxPctWarning:
		// Warning: ask agent to compress context
		var state string
		database.QueryRowContext(ctx, "SELECT COALESCE(context_state,'normal') FROM swarm_agents WHERE id=?", agentID).Scan(&state)
		if state != "normal" {
			return // don't spam
		}
		database.ExecContext(ctx, //nolint:errcheck
			"UPDATE swarm_agents SET context_state='compressing' WHERE id=?", agentID)
		msg := InboxMessage{
			SchemaVersion: "1",
			MessageID:     generateSwarmID(),
			Type:          "context_warning",
			Action:        fmt.Sprintf("Context usage is at %.0f%%. Please compress your working memory: summarise what you've done so far, drop verbose intermediate reasoning, and continue more concisely.", pct*100),
			SentAt:        time.Now().Unix(),
		}
		writeAgentInboxMsg(ctx, sessionID, agentID, msg) //nolint:errcheck

	default:
		// Healthy — reset state if it was compressing (agent successfully compressed)
		var state string
		database.QueryRowContext(ctx, "SELECT COALESCE(context_state,'normal') FROM swarm_agents WHERE id=?", agentID).Scan(&state)
		if state == "compressing" && pct < ctxPctWarning {
			database.ExecContext(ctx, //nolint:errcheck
				"UPDATE swarm_agents SET context_state='normal' WHERE id=?", agentID)
		}
	}
}

func emergencyRotateAgent(ctx context.Context, sessionID, agentID, capturedTmux string) {
	time.Sleep(emergencyKillDelay)

	// Verify the agent is still the same tmux session that triggered the emergency.
	// If the session changed, the agent already rotated — do not kill a new one.
	var currentTmux string
	database.QueryRowContext(ctx, "SELECT COALESCE(tmux_session,'') FROM swarm_agents WHERE id=?", agentID).Scan(&currentTmux) //nolint:errcheck
	if capturedTmux != "" && currentTmux != capturedTmux {
		log.Printf("ipc: emergency kill aborted — agent %s already rotated", agentID[:8])
		return
	}

	log.Printf("ipc: emergency kill agent %s", agentID[:8])

	// Get agent info before kill
	var tmuxSession, taskID string
	database.QueryRowContext(ctx,
		"SELECT COALESCE(tmux_session,''), COALESCE(current_task_id,'') FROM swarm_agents WHERE id=?",
		agentID,
	).Scan(&tmuxSession, &taskID) //nolint:errcheck

	// Kill tmux session
	if tmuxSession != "" {
		exec.Command("tmux", "kill-session", "-t", tmuxSession).Run() //nolint:errcheck
	}

	// Mark agent as idle
	database.ExecContext(ctx, //nolint:errcheck
		"UPDATE swarm_agents SET tmux_session=NULL, status='idle', context_state='normal', rotated_at=? WHERE id=?",
		time.Now().Unix(), agentID,
	)

	// Mark any running task as needs_review first, then idempotently re-queue it.
	// Two-step: transition to needs_review (valid from 'running'), then needs_review → queued.
	if taskID != "" {
		now := time.Now().Unix()
		// H1: Release repo lease before re-queuing so the next agent can acquire it.
		releaseRepoLease(ctx, database, taskID)
		database.ExecContext(ctx, //nolint:errcheck
			"UPDATE swarm_tasks SET stage='needs_review', blocked_reason='emergency context rotation', updated_at=?, stage_changed_at=? WHERE id=? AND stage='running'",
			now, now, taskID,
		)
		// Idempotent re-queue: only if still in needs_review (not already re-queued).
		// requeued_at guard prevents duplicate dispatch on crash/retry.
		result, err := database.ExecContext(ctx,
			`UPDATE swarm_tasks SET stage='queued', agent_id=NULL, requeued_at=?, updated_at=?, stage_changed_at=?
			 WHERE id=? AND stage='needs_review' AND requeued_at IS NULL`,
			now, now, now, taskID,
		)
		if err == nil {
			if n, _ := result.RowsAffected(); n > 0 {
				log.Printf("ipc: emergency rotate re-queued task=%s", taskID[:8])
				go autoDispatchQueuedTasks(context.Background(), sessionID)
			}
		}
	}

	writeSwarmEvent(ctx, sessionID, agentID, taskID, "agent_emergency_rotated", "context limit exceeded")
	swarmBroadcaster.schedule(sessionID)
}

// ─── Inbox writer ─────────────────────────────────────────────────────────────

func writeAgentInboxMsg(ctx context.Context, sessionID, agentID string, msg InboxMessage) error {
	inboxDir := swarmInboxDir(sessionID, agentID)
	if err := os.MkdirAll(inboxDir, 0755); err != nil {
		return err
	}
	filename := fmt.Sprintf("msg_%d_%s.json", time.Now().UnixNano(), msg.MessageID[:8])
	path := filepath.Join(inboxDir, filename)
	data, err := json.MarshalIndent(msg, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data)
}
