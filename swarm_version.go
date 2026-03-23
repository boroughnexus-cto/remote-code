package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// BuildCommit is injected at compile time:
//
//	go build -ldflags="-X main.BuildCommit=$(git rev-parse --short HEAD)"
//
// Falls back to "dev" when built without ldflags (e.g. `go run .`).
var BuildCommit = "dev"

// ─── Server-side state ────────────────────────────────────────────────────────

type versionState struct {
	mu          sync.RWMutex
	remoteHead  string
	updateAvail bool
	checkedAt   time.Time
}

var globalVersionState = &versionState{}

// startVersionCheck launches the background goroutine. Call once from main().
func startVersionCheck() {
	go func() {
		// Wait for the server to finish starting before hitting the network.
		time.Sleep(10 * time.Second)
		checkRemoteVersion()

		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			checkRemoteVersion()
		}
	}()
}

// checkRemoteVersion resolves the remote HEAD commit and updates globalVersionState.
// Strategy: try fast local cache first, then query remote.
func checkRemoteVersion() {
	repoDir := repoDirectory()

	// Fast path: local reflog already knows fork/main
	out, _, err := runGit(repoDir, "rev-parse", "--short", "fork/main")
	if err == nil && len(strings.TrimSpace(out)) > 0 {
		setRemoteHead(strings.TrimSpace(out))
		return
	}

	// Slow path: ask the fork remote (network call, ~1–2 s)
	out, _, err = runGit(repoDir, "ls-remote", "fork", "HEAD")
	if err == nil {
		// format: "<sha>\tHEAD"
		if sha, _, ok := strings.Cut(out, "\t"); ok && len(sha) >= 7 {
			setRemoteHead(sha[:7])
			return
		}
	}
}

func setRemoteHead(shortSHA string) {
	shortSHA = strings.TrimSpace(shortSHA)
	avail := BuildCommit != "dev" && shortSHA != "" && shortSHA != BuildCommit

	globalVersionState.mu.Lock()
	globalVersionState.remoteHead = shortSHA
	globalVersionState.updateAvail = avail
	globalVersionState.checkedAt = time.Now()
	globalVersionState.mu.Unlock()

	if avail {
		log.Printf("version: update available — running %s, remote is %s", BuildCommit, shortSHA)
	}
}

func repoDirectory() string {
	if d := os.Getenv("SWARMOPS_REPO_DIR"); d != "" {
		return d
	}
	if exe, err := os.Executable(); err == nil {
		// Prefer the source repo dir when running from a build in the repo
		return dirOf(exe)
	}
	wd, _ := os.Getwd()
	return wd
}

func dirOf(p string) string {
	idx := strings.LastIndex(p, "/")
	if idx < 0 {
		return "."
	}
	return p[:idx]
}

// ─── HTTP endpoint ────────────────────────────────────────────────────────────

// handleSwarmVersionAPI serves GET /api/swarm/version
func handleSwarmVersionAPI(w http.ResponseWriter, _ *http.Request) {
	globalVersionState.mu.RLock()
	resp := map[string]interface{}{
		"current":          BuildCommit,
		"remote":           globalVersionState.remoteHead,
		"update_available": globalVersionState.updateAvail,
	}
	globalVersionState.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}
