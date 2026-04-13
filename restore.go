package main

import (
	"context"
	"log"
	"os"
	"os/exec"
)

var restoreComplete = make(chan struct{})

func restoreSessions(ctx context.Context) {
	defer close(restoreComplete)
	sessions, err := listSessions(ctx)
	if err != nil {
		log.Printf("restore: list sessions: %v", err)
		return
	}
	restored := 0
	for _, s := range sessions {
		if s.Hidden || isTmuxAlive(s.TmuxSession) {
			continue
		}
		dir := s.Directory
		if _, err := os.Stat(dir); err != nil {
			log.Printf("restore: %s dir %s missing", s.Name, dir)
			if h, err := os.UserHomeDir(); err == nil { dir = h } else { dir = "/tmp" }
		}
		updateSessionStatus(ctx, s.ID, "restoring")
		cArgs := buildClaudeRestoreArgs(ctx, &s)
		args := append([]string{"new-session","-d","-s",s.TmuxSession,"-c",dir,"-x","200","-y","50","--"}, cArgs...)
		if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
			log.Printf("restore: tmux for %s: %v: %s", s.Name, err, out)
			updateSessionStatus(ctx, s.ID, "stopped")
			continue
		}
		if isTmuxAlive(s.TmuxSession) {
			updateSessionStatus(ctx, s.ID, "running")
			restored++
		} else {
			updateSessionStatus(ctx, s.ID, "stopped")
		}
	}
	log.Printf("restore: restored %d/%d sessions", restored, len(sessions))
}

func buildClaudeRestoreArgs(ctx context.Context, s *Session) []string {
	if s.ClaudeSessionID != nil && isValidUUID(*s.ClaudeSessionID) {
		return []string{"claude", "--resume", *s.ClaudeSessionID, "--dangerously-skip-permissions"}
	}
	newUUID := generateUUID()
	updateClaudeSessionID(ctx, s.ID, newUUID)
	return []string{"claude", "--session-id", newUUID, "--dangerously-skip-permissions"}
}
