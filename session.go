package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"os/exec"
	"time"

	xansi "github.com/charmbracelet/x/ansi"
)

// Session represents a managed Claude Code tmux session.
type Session struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	TmuxSession string  `json:"tmux_session"`
	Directory   string  `json:"directory"`
	ContextID   *string `json:"context_id,omitempty"`
	ContextName *string `json:"context_name,omitempty"`
	Hidden      bool    `json:"hidden"`
	Status      string  `json:"status"`
	CreatedAt   int64   `json:"created_at"`
	UpdatedAt   int64   `json:"updated_at"`
}

func generateID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func createSession(ctx context.Context, name, directory string, contextID, contextName *string, hidden bool) (*Session, error) {
	id := generateID()
	tmuxName := "sw-" + id
	now := time.Now().Unix()

	_, err := database.ExecContext(ctx,
		`INSERT INTO managed_sessions (id, name, tmux_session, directory, context_id, context_name, hidden, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'running', ?, ?)`,
		id, name, tmuxName, directory, contextID, contextName, boolToInt(hidden), now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}

	return &Session{
		ID:          id,
		Name:        name,
		TmuxSession: tmuxName,
		Directory:   directory,
		ContextID:   contextID,
		ContextName: contextName,
		Hidden:      hidden,
		Status:      "running",
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func listSessions(ctx context.Context) ([]Session, error) {
	rows, err := database.QueryContext(ctx,
		`SELECT id, name, tmux_session, directory, context_id, context_name, hidden, status, created_at, updated_at
		 FROM managed_sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var s Session
		var ctxID, ctxName sql.NullString
		var hiddenInt int
		if err := rows.Scan(&s.ID, &s.Name, &s.TmuxSession, &s.Directory, &ctxID, &ctxName, &hiddenInt, &s.Status, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		if ctxID.Valid {
			s.ContextID = &ctxID.String
		}
		if ctxName.Valid {
			s.ContextName = &ctxName.String
		}
		s.Hidden = hiddenInt != 0
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

func getSession(ctx context.Context, id string) (*Session, error) {
	var s Session
	var ctxID, ctxName sql.NullString
	var hiddenInt int
	err := database.QueryRowContext(ctx,
		`SELECT id, name, tmux_session, directory, context_id, context_name, hidden, status, created_at, updated_at
		 FROM managed_sessions WHERE id = ?`, id,
	).Scan(&s.ID, &s.Name, &s.TmuxSession, &s.Directory, &ctxID, &ctxName, &hiddenInt, &s.Status, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if ctxID.Valid {
		s.ContextID = &ctxID.String
	}
	if ctxName.Valid {
		s.ContextName = &ctxName.String
	}
	s.Hidden = hiddenInt != 0
	return &s, nil
}

func deleteSession(ctx context.Context, id string) error {
	s, err := getSession(ctx, id)
	if err != nil {
		return err
	}
	// Kill the tmux session if it exists
	exec.Command("tmux", "kill-session", "-t", s.TmuxSession).Run()
	_, err = database.ExecContext(ctx, "DELETE FROM managed_sessions WHERE id = ?", id)
	return err
}

func updateSessionStatus(ctx context.Context, id, status string) error {
	_, err := database.ExecContext(ctx,
		"UPDATE managed_sessions SET status = ?, updated_at = ? WHERE id = ?",
		status, time.Now().Unix(), id,
	)
	return err
}

// refreshSessionStatuses checks each session's tmux and updates status accordingly.
func refreshSessionStatuses(ctx context.Context) {
	sessions, err := listSessions(ctx)
	if err != nil {
		return
	}
	for _, s := range sessions {
		alive := isTmuxAlive(s.TmuxSession)
		if alive && s.Status != "running" {
			updateSessionStatus(ctx, s.ID, "running")
		} else if !alive && s.Status == "running" {
			updateSessionStatus(ctx, s.ID, "stopped")
		}
	}
}

func isTmuxAlive(name string) bool {
	return exec.Command("tmux", "has-session", "-t", name).Run() == nil
}

func captureTerminal(tmuxName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx,
		"tmux", "capture-pane", "-p", "-S", "-300", "-t", tmuxName,
	).Output()
	if err != nil {
		return "", fmt.Errorf("capture-pane: %w", err)
	}
	return xansi.Strip(string(out)), nil
}

func injectToSession(tmuxName, text string) error {
	if out, err := exec.Command("tmux", "send-keys", "-t", tmuxName, "-l", "--", text).CombinedOutput(); err != nil {
		return fmt.Errorf("tmux send-keys: %v: %s", err, out)
	}
	if out, err := exec.Command("tmux", "send-keys", "-t", tmuxName, "Enter").CombinedOutput(); err != nil {
		return fmt.Errorf("tmux enter: %v: %s", err, out)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// syncTmuxSessions detects tmux sessions that were killed externally
// and updates their status. Called periodically from main.
func syncTmuxSessions() {
	ctx := context.Background()
	sessions, err := listSessions(ctx)
	if err != nil {
		log.Printf("session: sync error: %v", err)
		return
	}
	for _, s := range sessions {
		alive := isTmuxAlive(s.TmuxSession)
		if !alive && s.Status == "running" {
			updateSessionStatus(ctx, s.ID, "stopped")
		} else if alive && s.Status == "stopped" {
			updateSessionStatus(ctx, s.ID, "running")
		}
	}
}
