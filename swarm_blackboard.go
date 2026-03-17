package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ─── Directory layout ────────────────────────────────────────────────────────
//
//  ~/.remote-code/swarm/<sessionID>/
//    blackboard/
//      decisions.md      — append-only log of orchestrator decisions (regenerated)
//      context.md        — running shared context snapshot
//      goals.md          — top-level goals / acceptance criteria
//    agents/<agentID>/
//      inbox/            — per-message files: msg_<ts>_<uuid>.json
//      outbox/           — events.jsonl (append-only, offset-tailed)
//    escalations/        — escalation JSON files

func swarmBaseDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback to /tmp on failure — this path should never be hit in normal operation.
		log.Printf("swarm: os.UserHomeDir failed: %v — using /tmp", err)
		home = "/tmp"
	}
	return filepath.Join(home, ".remote-code", "swarm")
}

func swarmSessionDir(sessionID string) string {
	return filepath.Join(swarmBaseDir(), sessionID)
}

func swarmBlackboardDir(sessionID string) string {
	return filepath.Join(swarmSessionDir(sessionID), "blackboard")
}

func swarmAgentBaseDir(sessionID, agentID string) string {
	return filepath.Join(swarmSessionDir(sessionID), "agents", agentID)
}

func swarmInboxDir(sessionID, agentID string) string {
	return filepath.Join(swarmAgentBaseDir(sessionID, agentID), "inbox")
}

func swarmOutboxDir(sessionID, agentID string) string {
	return filepath.Join(swarmAgentBaseDir(sessionID, agentID), "outbox")
}

func swarmEscalationsDir(sessionID string) string {
	return filepath.Join(swarmSessionDir(sessionID), "escalations")
}

// ─── Init ────────────────────────────────────────────────────────────────────

func initBlackboard(sessionID string) error {
	bb := swarmBlackboardDir(sessionID)
	if err := os.MkdirAll(bb, 0755); err != nil {
		return fmt.Errorf("mkdir blackboard: %w", err)
	}
	if err := os.MkdirAll(swarmEscalationsDir(sessionID), 0755); err != nil {
		return fmt.Errorf("mkdir escalations: %w", err)
	}
	seedFile(filepath.Join(bb, "goals.md"), "# Goals\n\n_Set by the orchestrator._\n")
	seedFile(filepath.Join(bb, "context.md"), "# Shared Context\n\n_Updated by the orchestrator._\n")
	seedFile(filepath.Join(bb, "decisions.md"), "# Decision Log\n\n_Automatically maintained._\n")
	return nil
}

func initAgentDirs(sessionID, agentID string) (inboxDir, outboxDir string, err error) {
	inboxDir = swarmInboxDir(sessionID, agentID)
	outboxDir = swarmOutboxDir(sessionID, agentID)
	if err = os.MkdirAll(inboxDir, 0755); err != nil {
		return "", "", fmt.Errorf("mkdir inbox: %w", err)
	}
	if err = os.MkdirAll(outboxDir, 0755); err != nil {
		return "", "", fmt.Errorf("mkdir outbox: %w", err)
	}
	// Persist paths to DB
	database.Exec(
		"UPDATE swarm_agents SET inbox_path=?, outbox_path=? WHERE id=?",
		inboxDir, outboxDir, agentID,
	)
	return inboxDir, outboxDir, nil
}

// ─── Decisions ───────────────────────────────────────────────────────────────

func appendDecision(ctx context.Context, sessionID, agentID, content string) error {
	id := generateSwarmID()
	now := time.Now().Unix()
	if _, err := database.ExecContext(ctx,
		"INSERT INTO swarm_decisions (id, session_id, agent_id, content, created_at) VALUES (?,?,?,?,?)",
		id, sessionID, agentID, content, now,
	); err != nil {
		return err
	}
	return regenerateDecisionsMd(ctx, sessionID)
}

func regenerateDecisionsMd(ctx context.Context, sessionID string) error {
	rows, err := database.QueryContext(ctx,
		`SELECT d.content, a.name, d.created_at
		 FROM swarm_decisions d
		 LEFT JOIN swarm_agents a ON a.id = d.agent_id
		 WHERE d.session_id = ?
		 ORDER BY d.created_at ASC`,
		sessionID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	var sb strings.Builder
	sb.WriteString("# Decision Log\n\n")
	for rows.Next() {
		var content, name string
		var ts int64
		rows.Scan(&content, &name, &ts)
		t := time.Unix(ts, 0).Format("2006-01-02 15:04")
		sb.WriteString(fmt.Sprintf("## [%s] %s\n\n%s\n\n---\n\n", t, name, content))
	}

	path := filepath.Join(swarmBlackboardDir(sessionID), "decisions.md")
	return atomicWriteFile(path, []byte(sb.String()))
}

// ─── Artifacts ───────────────────────────────────────────────────────────────

func appendArtifact(ctx context.Context, sessionID, taskID, agentID, artifactType, path, summary string) error {
	id := generateSwarmID()
	now := time.Now().Unix()
	_, err := database.ExecContext(ctx,
		`INSERT INTO swarm_artifacts (id, session_id, task_id, agent_id, type, path, summary, created_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		id, sessionID, swarmNullStr(taskID), swarmNullStr(agentID), artifactType, path, swarmNullStr(summary), now,
	)
	return err
}

// ─── Context block for agent prompts ─────────────────────────────────────────

// getContextBlock returns a small markdown block that agents can include in their
// reasoning — contains recent decisions and blackboard file paths.
func getContextBlock(ctx context.Context, sessionID string) string {
	bb := swarmBlackboardDir(sessionID)
	var sb strings.Builder
	sb.WriteString("## Shared Blackboard\n\n")
	sb.WriteString(fmt.Sprintf("- Goals: `%s/goals.md`\n", bb))
	sb.WriteString(fmt.Sprintf("- Context: `%s/context.md`\n", bb))
	sb.WriteString(fmt.Sprintf("- Decisions: `%s/decisions.md`\n\n", bb))

	// Last 5 decisions
	rows, err := database.QueryContext(ctx,
		`SELECT d.content, a.name FROM swarm_decisions d
		 LEFT JOIN swarm_agents a ON a.id = d.agent_id
		 WHERE d.session_id = ? ORDER BY d.created_at DESC LIMIT 5`,
		sessionID,
	)
	if err == nil {
		defer rows.Close()
		sb.WriteString("### Recent Decisions\n\n")
		for rows.Next() {
			var content, name string
			rows.Scan(&content, &name)
			short := content
			if len(short) > 120 {
				short = short[:120] + "…"
			}
			sb.WriteString(fmt.Sprintf("- **%s**: %s\n", name, short))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// ─── File utilities ──────────────────────────────────────────────────────────

// atomicWriteFile writes data to path via a temp file + rename — safe under
// concurrent readers (agents reading blackboard files).
func atomicWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// seedFile creates path with content only if it does not already exist.
func seedFile(path, content string) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		os.WriteFile(path, []byte(content), 0644) //nolint:errcheck
	}
}
