package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
)

// spawnSession creates a new tmux session and launches claude inside it.
// The session is registered in the database and the tmux session is created
// with the given working directory. Claude Code is started inside the session.
func spawnSession(ctx context.Context, name, directory string, contextID, contextName *string, model string) (*Session, error) {
	s, err := createSession(ctx, name, directory, contextID, contextName, false)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// Create tmux session in the target directory
	cmd := exec.Command("tmux", "new-session", "-d", "-s", s.TmuxSession, "-c", directory)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Clean up DB entry on failure
		deleteSession(ctx, s.ID)
		return nil, fmt.Errorf("tmux new-session: %v: %s", err, out)
	}

	// Launch claude inside the tmux session
	claudeCmd := "claude"
	if model != "" {
		claudeCmd = fmt.Sprintf("claude --model %s", model)
	}
	if err := exec.Command("tmux", "send-keys", "-t", s.TmuxSession, claudeCmd, "Enter").Run(); err != nil {
		log.Printf("spawn: failed to start claude in %s: %v", s.TmuxSession, err)
	}

	log.Printf("spawn: created session %q (tmux=%s, dir=%s, model=%s)", name, s.TmuxSession, directory, model)
	return s, nil
}
