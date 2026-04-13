package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeScrollback(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text", "hello world", "hello world"},
		{"keeps newlines", "line1\nline2\n", "line1\nline2\n"},
		{"keeps tabs", "col1\tcol2", "col1\tcol2"},
		{"strips null bytes", "hello\x00world", "helloworld"},
		{"strips bell", "hello\x07world", "helloworld"},
		{"strips escape", "hello\x1bworld", "helloworld"},
		{"keeps carriage return", "hello\rworld", "hello\rworld"},
		{"mixed control chars", "a\x00b\x07c\nd\te\x1bf", "abc\nd\tef"},
		{"empty string", "", ""},
		{"unicode preserved", "héllo wörld 🌍", "héllo wörld 🌍"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeScrollback(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeScrollback(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSnapshotSaveLoadRoundTrip(t *testing.T) {
	// Use a temp directory for snapshots
	tmpDir := t.TempDir()
	snapshotPath = tmpDir

	content := "$ claude --dangerously-skip-permissions\nHello! How can I help?\n> do something\nDone.\n"

	// Write snapshot file directly (simulating what saveSessionScrollback does post-capture)
	sessionID := "test123abc"
	path := filepath.Join(tmpDir, sessionID+".txt")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	// Load it back
	loaded, err := loadSessionScrollback(sessionID)
	if err != nil {
		t.Fatalf("loadSessionScrollback: %v", err)
	}
	if loaded != content {
		t.Errorf("round-trip mismatch:\ngot:  %q\nwant: %q", loaded, content)
	}

	// Clean up cached path
	snapshotPath = ""
}

func TestLoadSessionScrollback_MissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	snapshotPath = tmpDir

	content, err := loadSessionScrollback("nonexistent")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty string for missing file, got: %q", content)
	}

	snapshotPath = ""
}

func TestLoadSessionScrollback_SizeLimit(t *testing.T) {
	tmpDir := t.TempDir()
	snapshotPath = tmpDir

	// Create a file larger than maxSnapshotSize
	sessionID := "bigfile123"
	bigContent := strings.Repeat("x", maxSnapshotSize+1000)
	path := filepath.Join(tmpDir, sessionID+".txt")
	if err := os.WriteFile(path, []byte(bigContent), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	loaded, err := loadSessionScrollback(sessionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) > maxSnapshotSize {
		t.Errorf("loaded size %d exceeds max %d", len(loaded), maxSnapshotSize)
	}

	snapshotPath = ""
}

func TestLoadSessionScrollback_InvalidUTF8(t *testing.T) {
	tmpDir := t.TempDir()
	snapshotPath = tmpDir

	sessionID := "badutf8"
	path := filepath.Join(tmpDir, sessionID+".txt")
	// Invalid UTF-8 bytes
	if err := os.WriteFile(path, []byte{0xff, 0xfe, 0x80, 0x81}, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	content, err := loadSessionScrollback(sessionID)
	if err != nil {
		t.Fatalf("expected nil error for invalid UTF-8, got: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty string for invalid UTF-8, got: %q", content)
	}

	snapshotPath = ""
}

func TestDeleteSessionSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	snapshotPath = tmpDir

	sessionID := "deltest"
	path := filepath.Join(tmpDir, sessionID+".txt")
	os.WriteFile(path, []byte("test"), 0644)

	deleteSessionSnapshot(sessionID)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("snapshot file should have been deleted")
	}

	// Deleting again should not error
	deleteSessionSnapshot(sessionID)

	snapshotPath = ""
}

func TestPruneOrphanedSnapshots(t *testing.T) {
	tmpDir := t.TempDir()
	snapshotPath = tmpDir

	// Set up test database
	db, dbPath := initTestDatabase()
	database = db
	defer func() {
		database.Close()
		database = nil
		// Clean up test DB files
		basePath := strings.Split(dbPath, "?")[0]
		os.Remove(basePath)
		os.Remove(basePath + "-wal")
		os.Remove(basePath + "-shm")
	}()

	ctx := context.Background()

	// Create a session in the database
	_, err := createSession(ctx, "keeper", "/tmp", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessions, _ := listSessions(ctx)
	keeperID := sessions[0].ID

	// Create snapshot files: one valid, one orphan
	os.WriteFile(filepath.Join(tmpDir, keeperID+".txt"), []byte("keep"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "orphan123.txt"), []byte("delete"), 0644)
	os.WriteFile(filepath.Join(tmpDir, ".hidden"), []byte("skip"), 0644)

	pruneOrphanedSnapshots(ctx)

	// Valid snapshot should remain
	if _, err := os.Stat(filepath.Join(tmpDir, keeperID+".txt")); err != nil {
		t.Error("valid snapshot should not have been pruned")
	}
	// Orphan should be deleted
	if _, err := os.Stat(filepath.Join(tmpDir, "orphan123.txt")); !os.IsNotExist(err) {
		t.Error("orphan snapshot should have been pruned")
	}
	// Hidden file should remain
	if _, err := os.Stat(filepath.Join(tmpDir, ".hidden")); err != nil {
		t.Error("hidden file should not have been pruned")
	}

	snapshotPath = ""
}

func TestGenerateUUID(t *testing.T) {
	uuid := generateUUID()
	if !isValidUUID(uuid) {
		t.Errorf("generateUUID() = %q, not a valid UUID v4", uuid)
	}

	// Should be unique
	uuid2 := generateUUID()
	if uuid == uuid2 {
		t.Error("two generated UUIDs should not be equal")
	}
}

func TestIsValidUUID(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"550e8400-e29b-41d4-a716-446655440000", true},
		{"6b8f5e1a-b82f-4844-a7b6-aee5c1a19f2c", true},
		{"not-a-uuid", false},
		{"550e8400-e29b-51d4-a716-446655440000", false}, // version 5, not 4
		{"", false},
		{"550e8400-e29b-41d4-c716-446655440000", false}, // bad variant
		{"XXXXXXXX-XXXX-4XXX-8XXX-XXXXXXXXXXXX", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isValidUUID(tt.input)
			if got != tt.valid {
				t.Errorf("isValidUUID(%q) = %v, want %v", tt.input, got, tt.valid)
			}
		})
	}
}
