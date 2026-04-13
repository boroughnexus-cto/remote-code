package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	xansi "github.com/charmbracelet/x/ansi"
)

const (
	maxSnapshotSize = 1 << 20 // 1MB
)

var (
	snapshotMu   sync.Mutex
	snapshotPath string // cached, set by snapshotDir()
)

// snapshotDir returns the directory for session scrollback snapshots,
// creating it if necessary. Defaults to ~/.swarmops/snapshots/.
func snapshotDir() string {
	if snapshotPath != "" {
		return snapshotPath
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	dir := filepath.Join(home, ".swarmops", "snapshots")
	os.MkdirAll(dir, 0755)
	snapshotPath = dir
	return dir
}

// snapshotFile returns the full path for a session's scrollback snapshot.
func snapshotFile(sessionID string) string {
	return filepath.Join(snapshotDir(), sessionID+".txt")
}

// saveSessionScrollback captures the full tmux scrollback for a session and
// writes it to disk atomically (write to temp file, then rename).
func saveSessionScrollback(sessionID, tmuxName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Capture full scrollback (from start)
	out, err := exec.CommandContext(ctx,
		"tmux", "capture-pane", "-p", "-S", "-", "-t", tmuxName,
	).Output()
	if err != nil {
		return fmt.Errorf("capture-pane %s: %w", tmuxName, err)
	}

	// Strip ANSI escape sequences
	content := xansi.Strip(string(out))

	// Cap size
	if len(content) > maxSnapshotSize {
		content = content[len(content)-maxSnapshotSize:]
	}

	// Atomic write: temp file + rename
	dir := snapshotDir()
	tmp, err := os.CreateTemp(dir, ".snap-")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp: %w", err)
	}
	tmp.Close()

	target := snapshotFile(sessionID)
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// saveAllScrollbacks saves scrollback for all running sessions.
// Mutex-guarded: if a save is already in progress, the call is skipped.
func saveAllScrollbacks(ctx context.Context) {
	if !snapshotMu.TryLock() {
		return // previous save still running
	}
	defer snapshotMu.Unlock()

	sessions, err := listSessions(ctx)
	if err != nil {
		log.Printf("persist: list sessions for save: %v", err)
		return
	}

	saved := 0
	for _, s := range sessions {
		if s.Status != "running" || !isTmuxAlive(s.TmuxSession) {
			continue
		}
		if err := saveSessionScrollback(s.ID, s.TmuxSession); err != nil {
			log.Printf("persist: save scrollback %s (%s): %v", s.Name, s.ID, err)
		} else {
			saved++
		}
	}
	if saved > 0 {
		log.Printf("persist: saved %d session scrollbacks", saved)
	}
}

// loadSessionScrollback reads a saved scrollback snapshot from disk.
// Returns empty string if the file doesn't exist.
// Validates UTF-8 and strips non-printable control characters.
func loadSessionScrollback(sessionID string) (string, error) {
	path := snapshotFile(sessionID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	// Cap size
	if len(data) > maxSnapshotSize {
		data = data[len(data)-maxSnapshotSize:]
	}

	// Validate UTF-8
	if !utf8.Valid(data) {
		log.Printf("persist: snapshot %s is not valid UTF-8, skipping", sessionID)
		return "", nil
	}

	// Strip non-printable control characters (keep newlines, tabs, spaces)
	content := sanitizeScrollback(string(data))
	return content, nil
}

// sanitizeScrollback removes non-printable control characters from text,
// keeping newlines (\n), carriage returns (\r), and tabs (\t).
func sanitizeScrollback(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// deleteSessionSnapshot removes the scrollback snapshot file for a session.
func deleteSessionSnapshot(sessionID string) {
	path := snapshotFile(sessionID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("persist: delete snapshot %s: %v", sessionID, err)
	}
}

// pruneOrphanedSnapshots removes snapshot files that have no matching session in the database.
// Should be called after database is initialized and migrations are complete.
func pruneOrphanedSnapshots(ctx context.Context) {
	dir := snapshotDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("persist: read snapshot dir: %v", err)
		return
	}

	// Build set of valid session IDs
	sessions, err := listSessions(ctx)
	if err != nil {
		log.Printf("persist: list sessions for prune: %v", err)
		return
	}
	validIDs := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		validIDs[s.ID] = true
	}

	pruned := 0
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".txt")
		if id == entry.Name() {
			continue // not a .txt file
		}
		if !validIDs[id] {
			os.Remove(filepath.Join(dir, entry.Name()))
			pruned++
		}
	}
	if pruned > 0 {
		log.Printf("persist: pruned %d orphaned snapshots", pruned)
	}
}
