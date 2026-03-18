package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const swarmClaudeCommand = "claude --dangerously-skip-permissions"

// -----------------
// ID helpers
// -----------------

func swarmShortID(id string) string {
	if len(id) >= 12 {
		return id[:12]
	}
	return id
}

func swarmTmuxName(agentID string) string {
	return "sw-" + swarmShortID(agentID)
}

func swarmWorktreePath(repoPath, agentID string) string {
	return filepath.Join(repoPath, ".worktrees", "sw-"+swarmShortID(agentID))
}

func swarmBranchName(agentID string) string {
	return "swarm/" + swarmShortID(agentID)
}

// -----------------
// Spawn / Despawn
// -----------------

func spawnSwarmAgent(ctx context.Context, sessionID, agentID string) error {
	// Fetch agent from DB
	var repoPath, tmuxSession, existingWorktreePath, agentRole string
	err := database.QueryRowContext(ctx,
		"SELECT COALESCE(repo_path,''), COALESCE(tmux_session,''), COALESCE(worktree_path,''), role FROM swarm_agents WHERE id = ? AND session_id = ?",
		agentID, sessionID,
	).Scan(&repoPath, &tmuxSession, &existingWorktreePath, &agentRole)
	if err != nil {
		return fmt.Errorf("agent not found: %v", err)
	}
	if tmuxSession != "" {
		return fmt.Errorf("agent is already spawned in session %s", tmuxSession)
	}
	// SiBot / orchestrator can spawn without a repo_path
	if repoPath == "" && agentRole == "orchestrator" {
		return spawnSiBotAgent(ctx, sessionID, agentID)
	}
	if repoPath == "" {
		return fmt.Errorf("agent has no repo_path configured — edit the agent and add a repository path")
	}

	tmuxName := swarmTmuxName(agentID)
	worktreePath := swarmWorktreePath(repoPath, agentID)
	branchName := swarmBranchName(agentID)

	log.Printf("swarm: spawning agent %s — tmux=%s worktree=%s", agentID[:8], tmuxName, worktreePath)

	// Check if worktree already exists on disk (e.g. after system reboot — reuse it)
	worktreeExists := false
	if _, err := os.Stat(worktreePath); err == nil {
		worktreeExists = true
		log.Printf("swarm: reusing existing worktree at %s", worktreePath)
	}

	if !worktreeExists {
		// Create .worktrees directory
		if out, err := exec.Command("mkdir", "-p", filepath.Join(repoPath, ".worktrees")).CombinedOutput(); err != nil {
			return fmt.Errorf("mkdir .worktrees: %v: %s", err, out)
		}
		// Add git worktree off HEAD
		if out, err := exec.Command("git", "-C", repoPath, "worktree", "add", worktreePath, "-b", branchName).CombinedOutput(); err != nil {
			return fmt.Errorf("git worktree add: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}

	// If DB had a different worktree_path recorded, use the computed path (they should match)
	_ = existingWorktreePath

	// Init blackboard + agent IPC dirs
	if err := initBlackboard(sessionID); err != nil {
		log.Printf("swarm: warning — initBlackboard: %v", err)
	}
	if _, _, err := initAgentDirs(sessionID, agentID); err != nil {
		log.Printf("swarm: warning — initAgentDirs: %v", err)
	}

	// Start tmux session in worktree directory
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", tmuxName, "-c", worktreePath).CombinedOutput(); err != nil {
		if !worktreeExists {
			cleanupWorktree(repoPath, worktreePath, branchName)
		}
		return fmt.Errorf("tmux new-session: %v: %s", err, out)
	}

	// Inject SWARM_AGENT_ID and SWARM_SESSION_ID into the tmux session environment
	// so the Stop hook script can identify which agent's outbox to write to.
	exec.Command("tmux", "setenv", "-t", tmuxName, "SWARM_AGENT_ID", agentID).Run()   //nolint:errcheck
	exec.Command("tmux", "setenv", "-t", tmuxName, "SWARM_SESSION_ID", sessionID).Run() //nolint:errcheck

	// Write .claude/settings.json to register the Stop hook
	if err := writeAgentClaudeSettings(worktreePath); err != nil {
		log.Printf("swarm: warning — could not write .claude/settings.json: %v", err)
	}

	// Add .claude/settings.json to .gitignore (alongside SWARM_CONTEXT.md)
	gitignorePath := filepath.Join(worktreePath, ".gitignore")
	gitignoreExisting, _ := os.ReadFile(gitignorePath)
	if !strings.Contains(string(gitignoreExisting), ".claude/settings.json") {
		f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			fmt.Fprintln(f, "\n# Claude Code local settings (swarm hook config)")
			fmt.Fprintln(f, ".claude/settings.json")
			f.Close()
		}
	}

	// For orchestrator role: write context file before launching claude
	if agentRole == "orchestrator" {
		if err := writeOrchestratorContext(worktreePath, sessionID); err != nil {
			log.Printf("swarm: warning — could not write orchestrator context: %v", err)
		}
	} else if worktreeExists {
		// Re-spawned worker agent: write resume context so claude can pick up where it left off
		if err := writeResumeContext(ctx, agentID, sessionID, worktreePath); err != nil {
			log.Printf("swarm: warning — could not write resume context: %v", err)
		}
	}

	// Launch claude
	if out, err := exec.Command("tmux", "send-keys", "-t", tmuxName, swarmClaudeCommand, "Enter").CombinedOutput(); err != nil {
		log.Printf("swarm: warning — could not send claude command: %v: %s", err, out)
	}

	// Inject orientation message after claude starts
	if agentRole == "orchestrator" {
		go func() {
			time.Sleep(8 * time.Second)
			injectToSwarmAgent(context.Background(), agentID,
				"You are the swarm orchestrator. Please read SWARM_CONTEXT.md in this directory to understand your role and the available API. Then confirm you are ready.")
		}()
	} else if worktreeExists {
		go func() {
			time.Sleep(8 * time.Second)
			injectToSwarmAgent(context.Background(), agentID,
				"Welcome back. Please read RESUME_CONTEXT.md for context on what you were working on, then continue where you left off.")
		}()
	} else {
		// New worker agent — after Claude starts, auto-dispatch any queued tasks.
		go func() {
			time.Sleep(12 * time.Second) // wait for Claude to be ready
			autoDispatchQueuedTasks(context.Background(), sessionID)
		}()
	}

	// Persist to DB
	_, err = database.ExecContext(ctx,
		"UPDATE swarm_agents SET worktree_path = ?, tmux_session = ?, status = 'thinking' WHERE id = ?",
		worktreePath, tmuxName, agentID,
	)
	if err == nil {
		writeSwarmEvent(ctx, sessionID, agentID, "", "agent_spawned", tmuxName)
	}
	return err
}

func despawnSwarmAgent(ctx context.Context, sessionID, agentID string) error {
	var repoPath, worktreePath, tmuxSession string
	err := database.QueryRowContext(ctx,
		"SELECT COALESCE(repo_path, ''), COALESCE(worktree_path, ''), COALESCE(tmux_session, '') FROM swarm_agents WHERE id = ? AND session_id = ?",
		agentID, sessionID,
	).Scan(&repoPath, &worktreePath, &tmuxSession)
	if err != nil {
		return fmt.Errorf("agent not found: %v", err)
	}

	log.Printf("swarm: despawning agent %s", agentID[:8])

	// Kill tmux session (ignore errors — session may already be dead)
	if tmuxSession != "" {
		exec.Command("tmux", "kill-session", "-t", tmuxSession).Run()
	}

	// Remove worktree + branch
	if worktreePath != "" && repoPath != "" {
		cleanupWorktree(repoPath, worktreePath, swarmBranchName(agentID))
	}

	// Clear DB fields
	_, err = database.ExecContext(ctx,
		"UPDATE swarm_agents SET worktree_path = NULL, tmux_session = NULL, status = 'idle' WHERE id = ?",
		agentID,
	)
	if err == nil {
		writeSwarmEvent(ctx, sessionID, agentID, "", "agent_despawned", "")
	}
	return err
}

func cleanupWorktree(repoPath, worktreePath, branchName string) {
	if out, err := exec.Command("git", "-C", repoPath, "worktree", "remove", worktreePath, "--force").CombinedOutput(); err != nil {
		log.Printf("swarm: cleanup worktree remove: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", repoPath, "branch", "-D", branchName).CombinedOutput(); err != nil {
		log.Printf("swarm: cleanup branch delete: %v: %s", err, out)
	}
}

// -----------------
// Inject text to agent tmux session
// -----------------

func injectToSwarmAgent(ctx context.Context, agentID, text string) error {
	var tmuxSession string
	err := database.QueryRowContext(ctx,
		"SELECT COALESCE(tmux_session, '') FROM swarm_agents WHERE id = ?",
		agentID,
	).Scan(&tmuxSession)
	if err != nil {
		return fmt.Errorf("agent not found: %v", err)
	}
	if tmuxSession == "" {
		return fmt.Errorf("agent is not spawned — spawn it first")
	}

	// Send text, then Enter (same pattern as existing task executions)
	if out, err := exec.Command("tmux", "send-keys", "-t", tmuxSession, text).CombinedOutput(); err != nil {
		return fmt.Errorf("tmux send-keys: %v: %s", err, out)
	}
	time.Sleep(80 * time.Millisecond)
	if out, err := exec.Command("tmux", "send-keys", "-t", tmuxSession, "Enter").CombinedOutput(); err != nil {
		return fmt.Errorf("tmux enter: %v: %s", err, out)
	}
	return nil
}

// -----------------
// Status monitor
// -----------------

// stuckTimers tracks when each agent entered a non-progressing status
// (thinking or waiting). If the agent stays in that status longer than
// swarmStuckTimeout(), it is promoted to "stuck".
//
// Env: SWARM_STUCK_TIMEOUT — duration string (default "15m"). Set to "0" to
// disable time-based stuck detection (pattern matching only).
var (
	stuckTimers   = map[string]time.Time{} // agentID → time status last changed
	stuckTimersMu sync.Mutex
)

func swarmStuckTimeout() time.Duration {
	if v := os.Getenv("SWARM_STUCK_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		if secs, err := strconv.Atoi(v); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	return 30 * time.Minute
}

func startSwarmMonitor() {
	// Immediate reconciliation on startup — clears stale tmux references from DB
	go checkSwarmAgentStatuses()

	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			checkSwarmAgentStatuses()
		}
	}()
}

func checkSwarmAgentStatuses() {
	ctx := context.Background()

	rows, err := database.QueryContext(ctx,
		"SELECT id, session_id, tmux_session FROM swarm_agents WHERE tmux_session IS NOT NULL AND status NOT IN ('done','idle')",
	)
	if err != nil {
		return
	}
	defer rows.Close()

	type agentRow struct{ id, sessionID, tmuxSession string }
	var agents []agentRow
	for rows.Next() {
		var a agentRow
		rows.Scan(&a.id, &a.sessionID, &a.tmuxSession)
		agents = append(agents, a)
	}

	// Fetch session names for notifications (lazy: map[sessionID]name)
	sessionNames := map[string]string{}
	getSessionName := func(sessionID string) string {
		if n, ok := sessionNames[sessionID]; ok {
			return n
		}
		var name string
		database.QueryRowContext(ctx, "SELECT name FROM swarm_sessions WHERE id = ?", sessionID).Scan(&name)
		sessionNames[sessionID] = name
		return name
	}

	changed := map[string]bool{}
	for _, a := range agents {
		if !isTmuxSessionAlive(a.tmuxSession) {
			// Session exited — mark idle and clear tmux_session but preserve worktree_path
			// (worktree may have uncommitted work; user can re-spawn into it)
			database.ExecContext(ctx,
				"UPDATE swarm_agents SET status = 'idle', tmux_session = NULL WHERE id = ?",
				a.id)
			writeSwarmEvent(ctx, a.sessionID, a.id, "", "agent_offline", "tmux session exited")
			changed[a.sessionID] = true
			continue
		}

		newStatus := detectSwarmAgentStatus(a.tmuxSession)

		// Time-based stuck promotion: if pattern detection returns thinking/waiting
		// and the agent has been in that state for longer than swarmStuckTimeout(),
		// escalate to stuck. Active statuses (coding/testing/done) reset the timer.
		stuckTimeout := swarmStuckTimeout()
		if stuckTimeout > 0 {
			stuckTimersMu.Lock()
			switch newStatus {
			case "coding", "testing", "done", "stuck", "idle":
				// Progress or terminal state — reset the timer
				delete(stuckTimers, a.id)
			case "thinking", "waiting":
				if t, ok := stuckTimers[a.id]; !ok {
					// First time we see this non-progressing status — start timer
					stuckTimers[a.id] = time.Now()
				} else if time.Since(t) > stuckTimeout {
					// Agent has been non-progressing long enough — promote to stuck
					newStatus = "stuck"
				}
			}
			stuckTimersMu.Unlock()
		}

		// Only update if status changed (avoids noisy broadcasts)
		var curStatus string
		database.QueryRowContext(ctx, "SELECT status FROM swarm_agents WHERE id = ?", a.id).Scan(&curStatus)
		if newStatus != curStatus {
			database.ExecContext(ctx, "UPDATE swarm_agents SET status = ? WHERE id = ?", newStatus, a.id)
			changed[a.sessionID] = true

			// HITL notifications on significant transitions
			if newStatus == "stuck" {
				var agentName string
				database.QueryRowContext(ctx, "SELECT name FROM swarm_agents WHERE id = ?", a.id).Scan(&agentName)
				writeSwarmEvent(ctx, a.sessionID, a.id, "", "agent_stuck", "")
				notifyAgentStuck(getSessionName(a.sessionID), agentName, a.id)
			} else if newStatus == "waiting" {
				var agentName string
				database.QueryRowContext(ctx, "SELECT name FROM swarm_agents WHERE id = ?", a.id).Scan(&agentName)
				writeSwarmEvent(ctx, a.sessionID, a.id, "", "agent_waiting", "")
				notifyAgentWaiting(getSessionName(a.sessionID), agentName, a.id)
			}
		}
	}

	for sessionID := range changed {
		swarmBroadcaster.schedule(sessionID)
	}
}

func isTmuxSessionAlive(session string) bool {
	return exec.Command("tmux", "has-session", "-t", session).Run() == nil
}

// detectSwarmAgentStatus inspects the last 8 lines of the agent's tmux pane
// (captured every 15 seconds) and classifies the agent's current activity via
// string pattern matching. The caller may additionally promote "thinking" or
// "waiting" to "stuck" if the agent remains in that state past swarmStuckTimeout.
//
// Status conditions (checked in priority order):
//
//	stuck   — tmux capture-pane fails (session unreachable), OR pane contains
//	          "Error:", "FAILED", "fatal:", or "panic:"
//	coding  — pane contains Claude Code tool-use names: Write(, Edit(, Bash(,
//	          Read(, Search(, Grep(, str_replace, etc.
//	thinking — pane contains Claude's active-output indicators: ⏺ ◆ ● ↳ ✓
//	waiting  — pane contains "Do you want to proceed", "Press Enter", "(y/n)",
//	           or "waiting for input"
//	thinking — fallback (no pattern matched)
//
// Time-based escalation: configure SWARM_STUCK_TIMEOUT (default "15m").
// If an agent stays in thinking/waiting longer than this threshold without
// a transition to coding/testing/done, it is promoted to stuck. Set to "0"
// to disable time-based escalation.
func detectSwarmAgentStatus(tmuxSession string) string {
	// Capture 30 lines — enough to see Claude Code indicators even after a
	// long-running tool call has produced many lines of output.
	out, err := exec.Command("tmux", "capture-pane", "-t", tmuxSession, "-p", "-S", "-30").CombinedOutput()
	if err != nil {
		return "stuck"
	}
	pane := string(out)

	// Waiting for human input — check first, highest priority for operator action.
	// Use specific Claude Code permission/confirmation phrases only; generic shell
	// phrases like "Press Enter" appear in normal command output and cause false positives.
	if containsAny(pane, "Do you want to proceed", "waiting for input",
		"Proceed?", "Allow this action?", "bypass permissions",
		"Allow tool", "Allow read", "Allow write", "Allow bash") {
		return "waiting"
	}
	// Claude Code tool-use patterns — agent is actively executing something.
	if containsAny(pane, "Write(", "Edit(", "str_replace", "create_file", "write_file") {
		return "coding"
	}
	if containsAny(pane, "Bash(", "bash(", "execute_bash", "run_command") {
		return "coding"
	}
	if containsAny(pane, "Read(", "read_file", "view_file", "Search(", "Grep(") {
		return "coding"
	}
	if containsAny(pane, "Agent(", "TodoWrite(", "TodoRead(", "Glob(", "MultiEdit(") {
		return "coding"
	}
	// Claude's active output indicators (thinking/generating text).
	if containsAny(pane, "⏺", "◆", "●", "↳", "✓") {
		return "thinking"
	}
	// Error states — only clear terminal errors, not partial matches like "Error handling".
	if containsAny(pane, "\nError:", "fatal:", "panic:", "FAILED\n", "command not found") {
		return "stuck"
	}
	// Fallback: if none of the above matched, the agent is most likely running a
	// long command whose output has scrolled past the captured window (e.g. npm
	// install, go build, a test suite). Classify as "coding" so the stuck timer
	// is not advanced. Only genuine waiting/error patterns trigger escalation.
	return "coding"
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// -----------------
// Resume context (re-spawned worker agents)
// -----------------

func writeResumeContext(ctx context.Context, agentID, sessionID, worktreePath string) error {
	var agentName, agentRole string
	database.QueryRowContext(ctx,
		"SELECT name, role FROM swarm_agents WHERE id = ?", agentID,
	).Scan(&agentName, &agentRole)

	// Latest notes (up to 5, newest first)
	noteRows, _ := database.QueryContext(ctx,
		"SELECT content, created_by, created_at FROM swarm_agent_notes WHERE agent_id = ? ORDER BY created_at DESC LIMIT 5",
		agentID)
	var notes []string
	if noteRows != nil {
		defer noteRows.Close()
		for noteRows.Next() {
			var content, createdBy string
			var ts int64
			noteRows.Scan(&content, &createdBy, &ts)
			t := time.Unix(ts, 0).Format("2006-01-02 15:04")
			notes = append(notes, fmt.Sprintf("[%s by %s] %s", t, createdBy, content))
		}
	}

	// Current assigned task (most recently updated)
	var taskTitle, taskDesc, taskStage string
	database.QueryRowContext(ctx,
		`SELECT title, COALESCE(description,''), stage FROM swarm_tasks
		 WHERE session_id = ? AND agent_id = ? ORDER BY updated_at DESC LIMIT 1`,
		sessionID, agentID,
	).Scan(&taskTitle, &taskDesc, &taskStage)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Resume Context — %s\n\n", agentName))
	sb.WriteString(fmt.Sprintf("Resumed at: %s\n\n", time.Now().Format("2006-01-02 15:04:05")))

	if taskTitle != "" {
		sb.WriteString("## Current Task\n\n")
		sb.WriteString(fmt.Sprintf("**%s** (stage: %s)\n\n", taskTitle, taskStage))
		if taskDesc != "" {
			sb.WriteString(taskDesc + "\n\n")
		}
	}

	if len(notes) > 0 {
		sb.WriteString("## Notes from Orchestrator / User\n\n")
		for _, n := range notes {
			sb.WriteString("- " + n + "\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Next Steps\n\n")
	sb.WriteString("1. Run `git log --oneline -5` to see recent commits on this branch\n")
	sb.WriteString("2. Review any uncommitted changes with `git diff`\n")
	sb.WriteString("3. Continue work on the task above\n")

	filePath := filepath.Join(worktreePath, "RESUME_CONTEXT.md")
	return os.WriteFile(filePath, []byte(sb.String()), 0644)
}

// -----------------
// SiBot (orchestrator without worktree)
// -----------------

func spawnSiBotAgent(ctx context.Context, sessionID, agentID string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not determine home dir: %v", err)
	}
	workDir := filepath.Join(homeDir, ".remote-code", "sibot", agentID)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return fmt.Errorf("mkdir sibot workdir: %v", err)
	}

	tmuxName := swarmTmuxName(agentID)
	log.Printf("swarm: spawning SiBot %s — tmux=%s workdir=%s", agentID[:8], tmuxName, workDir)

	// Write orchestrator context
	if err := writeOrchestratorContext(workDir, sessionID); err != nil {
		log.Printf("swarm: warning — could not write SiBot context: %v", err)
	}

	// Init blackboard + agent IPC dirs
	if err := initBlackboard(sessionID); err != nil {
		log.Printf("swarm: warning — initBlackboard: %v", err)
	}
	if _, _, err := initAgentDirs(sessionID, agentID); err != nil {
		log.Printf("swarm: warning — initAgentDirs: %v", err)
	}

	// Start tmux session
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", tmuxName, "-c", workDir).CombinedOutput(); err != nil {
		return fmt.Errorf("tmux new-session: %v: %s", err, out)
	}

	// Inject SWARM_AGENT_ID and SWARM_SESSION_ID into the tmux session environment
	exec.Command("tmux", "setenv", "-t", tmuxName, "SWARM_AGENT_ID", agentID).Run()   //nolint:errcheck
	exec.Command("tmux", "setenv", "-t", tmuxName, "SWARM_SESSION_ID", sessionID).Run() //nolint:errcheck

	// Write .claude/settings.json to register the Stop hook
	if err := writeAgentClaudeSettings(workDir); err != nil {
		log.Printf("swarm: warning — could not write .claude/settings.json for SiBot: %v", err)
	}

	// Launch claude
	if out, err := exec.Command("tmux", "send-keys", "-t", tmuxName, swarmClaudeCommand, "Enter").CombinedOutput(); err != nil {
		log.Printf("swarm: warning — could not send claude command: %v: %s", err, out)
	}

	// Inject orientation message after claude starts
	go func() {
		time.Sleep(8 * time.Second)
		injectToSwarmAgent(context.Background(), agentID,
			"You are SiBot, the swarm orchestrator. Please read SWARM_CONTEXT.md in this directory to understand your role and the available API. Then confirm you are ready and give a brief summary of the current swarm state.")
	}()

	// Persist to DB
	_, err = database.ExecContext(ctx,
		"UPDATE swarm_agents SET worktree_path = ?, tmux_session = ?, status = 'thinking' WHERE id = ?",
		workDir, tmuxName, agentID,
	)
	if err == nil {
		writeSwarmEvent(ctx, sessionID, agentID, "", "agent_spawned", tmuxName)
	}
	return err
}

// -----------------
// SiBot heartbeat
// -----------------

func startSiBotHeartbeat() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			runSiBotHeartbeat()
		}
	}()
}

func runSiBotHeartbeat() {
	ctx := context.Background()

	// Find all active SiBot / orchestrator agents
	rows, err := database.QueryContext(ctx,
		`SELECT a.id, a.session_id, a.tmux_session
		 FROM swarm_agents a
		 WHERE a.role = 'orchestrator'
		   AND a.tmux_session IS NOT NULL
		   AND a.status NOT IN ('idle','done')`,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	type sibotRow struct{ id, sessionID, tmuxSession string }
	var sibots []sibotRow
	for rows.Next() {
		var s sibotRow
		rows.Scan(&s.id, &s.sessionID, &s.tmuxSession)
		sibots = append(sibots, s)
	}

	for _, sibot := range sibots {
		if !isTmuxSessionAlive(sibot.tmuxSession) {
			continue
		}
		briefing := buildSiBotBriefing(ctx, sibot.sessionID)
		if err := injectToSwarmAgent(ctx, sibot.id, briefing); err != nil {
			log.Printf("swarm: heartbeat inject failed for SiBot %s: %v", sibot.id[:8], err)
		}
	}
}

func buildSiBotBriefing(ctx context.Context, sessionID string) string {
	now := time.Now().Format("2006-01-02 15:04:05")

	// Collect all non-orchestrator agents
	agentRows, err := database.QueryContext(ctx,
		`SELECT a.id, a.name, a.role, a.status, COALESCE(a.mission,'')
		 FROM swarm_agents a
		 WHERE a.session_id = ? AND a.role != 'orchestrator'
		 ORDER BY a.created_at ASC`,
		sessionID,
	)
	if err != nil {
		return fmt.Sprintf("## Heartbeat %s\n\nCould not read agent state: %v", now, err)
	}
	defer agentRows.Close()

	type agentInfo struct {
		id, name, role, status, mission string
	}
	var agents []agentInfo
	for agentRows.Next() {
		var a agentInfo
		agentRows.Scan(&a.id, &a.name, &a.role, &a.status, &a.mission)
		agents = append(agents, a)
	}

	// Collect recent tasks
	taskRows, err := database.QueryContext(ctx,
		`SELECT t.title, t.stage, COALESCE(a.name,'unassigned')
		 FROM swarm_tasks t
		 LEFT JOIN swarm_agents a ON a.id = t.agent_id
		 WHERE t.session_id = ? AND t.stage != 'done'
		 ORDER BY t.updated_at DESC LIMIT 10`,
		sessionID,
	)
	type taskInfo struct{ title, stage, agentName string }
	var tasks []taskInfo
	if err == nil {
		defer taskRows.Close()
		for taskRows.Next() {
			var t taskInfo
			taskRows.Scan(&t.title, &t.stage, &t.agentName)
			tasks = append(tasks, t)
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Heartbeat — %s\n\n", now))
	sb.WriteString("This is your periodic swarm status briefing. Review and take action.\n\n")

	sb.WriteString("### Agent Status\n\n")
	if len(agents) == 0 {
		sb.WriteString("No worker agents in this session.\n\n")
	} else {
		for _, a := range agents {
			statusIcon := "●"
			switch a.status {
			case "coding":
				statusIcon = "⚡"
			case "thinking":
				statusIcon = "💭"
			case "stuck":
				statusIcon = "🚨"
			case "waiting":
				statusIcon = "⏳"
			case "idle":
				statusIcon = "○"
			}
			sb.WriteString(fmt.Sprintf("- %s **%s** (%s) — %s\n", statusIcon, a.name, a.role, a.status))
			if a.mission != "" {
				sb.WriteString(fmt.Sprintf("  Mission: %s\n", a.mission))
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("### Active Tasks\n\n")
	if len(tasks) == 0 {
		sb.WriteString("No active tasks.\n\n")
	} else {
		for _, t := range tasks {
			sb.WriteString(fmt.Sprintf("- [%s] **%s** → %s\n", t.stage, t.title, t.agentName))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("### Your Action\n\n")
	sb.WriteString("1. GET /api/swarm/sessions/" + sessionID + " for full state\n")
	sb.WriteString("2. Identify any stuck/idle agents that need direction\n")
	sb.WriteString("3. Inject briefs, update task stages, create new tasks as needed\n")
	sb.WriteString("4. Reply with a brief summary of actions taken\n")

	return sb.String()
}

// -----------------
// Orchestrator context
// -----------------

func writeOrchestratorContext(worktreePath, sessionID string) error {
	apiBase := "http://localhost:8080"

	content := fmt.Sprintf(`# SiBot — Swarm Orchestrator

You are **SiBot**, the orchestrator for this AI agent swarm. You run as a Claude Code instance
with full tool access. Your peers are other Claude Code instances, each in their own tmux session,
working on tasks you assign them.

The server runs on localhost — no auth token needed for API calls.

## Session

- Session ID: %s
- API base: %s

---

## API Reference

### Read swarm state
`+"`GET %s/api/swarm/sessions/%s`"+`
Returns: agents (id, name, role, status, mission, tmux_session, current_task_id, latest_note), tasks, events.

### Create task
`+"`POST %s/api/swarm/sessions/%s/tasks`"+`
Body: `+"`{\"title\":\"...\",\"description\":\"...\",\"stage\":\"spec\",\"project\":\"...\"}`"+`
Stages: spec → implement → test → deploy → done

### Update task (stage or assignment)
`+"`PATCH %s/api/swarm/sessions/%s/tasks/{taskID}`"+`
Body: `+"`{\"stage\":\"implement\"}`"+` or `+"`{\"agent_id\":\"...\"}`"+`

### Inject instruction into agent's Claude Code terminal
`+"`POST %s/api/swarm/sessions/%s/agents/{agentID}/inject`"+`
Body: `+"`{\"text\":\"Your detailed brief here\"}`"+`
This sends the text directly into their Claude Code session — they will read and act on it.

---

## Your Operating Pattern

Each time you receive a heartbeat or user message, follow this cycle:

1. **GET** the session state — see who is online, what they're working on, what's stuck
2. **Decide** what needs doing — new tasks, reassignments, unblocking stuck agents
3. **Act** — inject briefs to agents, update task stages, create new tasks
4. **Report** — brief summary of what you've done and what to watch

## Injecting to Agents

When you inject a brief to a Claude Code agent, be specific and self-contained:
- What task they should work on
- Which files/directories to look at
- What success looks like (tests pass, endpoint returns X, etc.)
- Any constraints (don't break Y, use pattern Z)

Agents won't remember previous conversations — each inject is a fresh prompt.

## Agent Roles

- `+"`orchestrator`"+` — You (SiBot). Coordinates. Does not write code directly.
- `+"`senior-dev`"+` — Implements features, refactors, code reviews
- `+"`qa-agent`"+` — Writes tests, runs test suites, reports failures
- `+"`devops-agent`"+` — CI/CD, Docker, deployments, infrastructure
- `+"`researcher`"+` — Specs, investigation, documentation
- `+"`worker`"+` — General purpose

## HITL / Escalation

The system sends Telegram notifications when agents go stuck or waiting.
You will also receive a heartbeat every few minutes with the current state.
If an agent needs human decision-making, inject a message telling them to wait and note the blocker.

## Style

- Action-oriented: do first, explain briefly after
- When you've injected to agents, list who got what brief
- Keep task board current — move stages as work progresses
`,
		sessionID, apiBase,
		apiBase, sessionID,
		apiBase, sessionID,
		apiBase, sessionID,
		apiBase, sessionID,
	)

	filePath := filepath.Join(worktreePath, "SWARM_CONTEXT.md")
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return err
	}

	// Append SWARM_CONTEXT.md to .gitignore to prevent accidental token commit.
	// Only add if not already present to avoid duplicates on re-spawn.
	gitignorePath := filepath.Join(worktreePath, ".gitignore")
	existing, _ := os.ReadFile(gitignorePath)
	if !strings.Contains(string(existing), "SWARM_CONTEXT.md") {
		f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			fmt.Fprintln(f, "\n# Swarm orchestrator context (contains API token)")
			fmt.Fprintln(f, "SWARM_CONTEXT.md")
			f.Close()
		}
	}
	return nil
}

// -----------------
// Hook script
// -----------------

const agentStopHookScript = `#!/bin/bash
# Claude Code Stop hook — writes heartbeat IPC to swarm agent outbox
# Env vars injected by swarm spawner: SWARM_AGENT_ID, SWARM_SESSION_ID

AGENT_ID="${SWARM_AGENT_ID:-}"
SESSION_ID="${SWARM_SESSION_ID:-}"
[ -z "$AGENT_ID" ] || [ -z "$SESSION_ID" ] && exit 0

OUTBOX_DIR="${HOME}/.remote-code/swarm/${SESSION_ID}/agents/${AGENT_ID}/outbox"
[ ! -d "$OUTBOX_DIR" ] && exit 0

EVENTS_FILE="${OUTBOX_DIR}/events.jsonl"

# Parse stop hook stdin for transcript_path
_json=$(cat)
transcript_path=$(printf '%s' "$_json" | jq -r '.transcript_path // ""' 2>/dev/null)

model=""
tokens_used=0

if [ -n "$transcript_path" ] && [ -f "$transcript_path" ] && command -v jq >/dev/null 2>&1; then
    # Extract model from last assistant message
    model=$(grep -F '"type":"assistant"' "$transcript_path" 2>/dev/null | tail -1 | \
        jq -r '.message.model // ""' 2>/dev/null)
    # Sum cumulative tokens across all assistant messages
    tokens_used=$(grep -F '"type":"assistant"' "$transcript_path" 2>/dev/null | \
        jq -s '[.[].message.usage |
            ((.input_tokens // 0) + (.output_tokens // 0) +
             (.cache_creation_input_tokens // 0) + (.cache_read_input_tokens // 0))
        ] | add // 0' 2>/dev/null)
fi

# context_pct — try from stop hook payload first, fall back to 0
context_pct=$(printf '%s' "$_json" | jq -r '
    if .context_window.used_percentage != null then
        (.context_window.used_percentage / 100)
    else 0 end' 2>/dev/null)
context_pct="${context_pct:-0}"

ts=$(date +%s)
printf '{"event":"heartbeat","context_pct":%s,"model_name":"%s","tokens_used":%s,"ts":%s}\n' \
    "$context_pct" \
    "${model:-}" \
    "${tokens_used:-0}" \
    "$ts" \
    >> "$EVENTS_FILE"
`

var ensureHookOnce sync.Once

// ensureSwarmHookScript writes the Stop hook shell script to
// ~/.remote-code/swarm/hooks/agent-stop.sh and makes it executable.
// It is safe to call multiple times — uses sync.Once internally.
func ensureSwarmHookScript() {
	ensureHookOnce.Do(func() {
		hooksDir := filepath.Join(swarmBaseDir(), "hooks")
		if err := os.MkdirAll(hooksDir, 0755); err != nil {
			log.Printf("swarm hooks: mkdir %s: %v", hooksDir, err)
			return
		}
		scriptPath := filepath.Join(hooksDir, "agent-stop.sh")
		if err := os.WriteFile(scriptPath, []byte(agentStopHookScript), 0755); err != nil {
			log.Printf("swarm hooks: write %s: %v", scriptPath, err)
			return
		}
		log.Printf("swarm hooks: wrote %s", scriptPath)
	})
}

// writeAgentClaudeSettings writes a .claude/settings.json into the agent worktree
// that registers the Stop hook script so Claude Code fires it on each response.
func writeAgentClaudeSettings(worktreePath string) error {
	hookScript := filepath.Join(swarmBaseDir(), "hooks", "agent-stop.sh")
	settings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"Stop": []map[string]interface{}{
				{
					"hooks": []map[string]interface{}{
						{"type": "command", "command": hookScript},
					},
				},
			},
		},
	}
	claudeDir := filepath.Join(worktreePath, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return err
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(claudeDir, "settings.json"), b, 0644)
}
