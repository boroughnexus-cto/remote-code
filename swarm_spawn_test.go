package main

import (
	"os"
	"path/filepath"
	"testing"
)

// ─── validateSwarmRepoPath Tests ──────────────────────────────────────────────

func TestValidateRepoPath_ValidUnderHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, "git", "project")
	if err := validateSwarmRepoPath(path); err != nil {
		t.Errorf("expected nil for path under home, got: %v", err)
	}
}

func TestValidateRepoPath_TraversalAttack(t *testing.T) {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, "git", "..", "..", "etc", "passwd")
	if err := validateSwarmRepoPath(path); err == nil {
		t.Error("expected error for path traversal attack, got nil")
	}
}

func TestValidateRepoPath_OutsideHome(t *testing.T) {
	if err := validateSwarmRepoPath("/etc/passwd"); err == nil {
		t.Error("expected error for path outside home, got nil")
	}
}

func TestValidateRepoPath_HomeItself(t *testing.T) {
	home, _ := os.UserHomeDir()
	// Exact home dir: not *under* home (requires trailing separator)
	if err := validateSwarmRepoPath(home); err == nil {
		t.Error("expected error for home dir itself, got nil")
	}
}

func TestValidateRepoPath_SymlinkEscape(t *testing.T) {
	home, _ := os.UserHomeDir()

	// Create a symlink inside home pointing to /tmp
	linkPath := filepath.Join(home, "swarmops-test-symlink-escape")
	os.Remove(linkPath) // clean up any leftover
	if err := os.Symlink("/tmp", linkPath); err != nil {
		t.Skipf("cannot create symlink (may need elevated perms): %v", err)
	}
	defer os.Remove(linkPath)

	// Path through the symlink: resolves to /tmp/foo, which is outside home
	escapePath := filepath.Join(linkPath, "foo")
	if err := validateSwarmRepoPath(escapePath); err == nil {
		t.Error("expected error for symlink escape to /tmp, got nil")
	}
}
