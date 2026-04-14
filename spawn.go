package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"os/exec"
	"regexp"
)

// uuidV4Pattern validates a canonical UUID v4 string.
var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// generateUUID produces a random UUID v4 string using crypto/rand (no external dependency).
func generateUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// isValidUUID checks whether s is a valid UUID v4 in canonical lowercase hex form.
func isValidUUID(s string) bool {
	return uuidV4Pattern.MatchString(s)
}

// spawnSession creates a new tmux session and launches claude inside it.
// The session is registered in the database and the tmux session is created
// with the given working directory. Claude Code is started inside the session
// with a controlled --session-id so it can be resumed after restarts.
// An optional model string selects a specific Claude model (e.g. "claude-sonnet-4-6").
func spawnSession(ctx context.Context, name, directory string, contextID, contextName, mission *string, model string) (*Session, error) {
	s, err := createSession(ctx, name, directory, contextID, contextName, mission, false)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// Generate a UUID for Claude's session so we can resume it later.
	claudeUUID := generateUUID()

	// Create tmux session with claude as the session command (no send-keys needed).
	// Using "--" ensures claude flags aren't interpreted as tmux flags.
	claudeArgs := []string{"claude", "--session-id", claudeUUID, "--dangerously-skip-permissions"}
	if model != "" {
		claudeArgs = append(claudeArgs, "--model", model)
	}
	tmuxArgs := append([]string{"new-session", "-d",
		"-s", s.TmuxSession,
		"-c", directory,
		"-x", "200", "-y", "50",
		"--",
	}, claudeArgs...)
	cmd := exec.Command("tmux", tmuxArgs...)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Clean up DB entry on failure
		deleteSession(ctx, s.ID)
		return nil, fmt.Errorf("tmux new-session: %v: %s", err, out)
	}

	// Store the Claude session ID for future resume
	if err := updateClaudeSessionID(ctx, s.ID, claudeUUID); err != nil {
		log.Printf("spawn: failed to store claude session ID for %s: %v", s.ID, err)
	}
	s.ClaudeSessionID = &claudeUUID

	log.Printf("spawn: created session %q (tmux=%s, dir=%s, claude=%s, model=%s)", name, s.TmuxSession, directory, claudeUUID, model)
	return s, nil
}
