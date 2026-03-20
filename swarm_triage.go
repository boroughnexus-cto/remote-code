package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ─── Proactive Triage Agent ───────────────────────────────────────────────────
//
// A background goroutine that wakes on a configurable interval, scans
// configured repositories for actionable signals, and auto-enqueues swarm
// goals into an eligible session.
//
// Signals (all read-only — no local repo code execution):
//   stale_pr   — PRs open >7 days (via gh CLI)
//   vuln       — govulncheck findings (Go repos only; requires govulncheck in PATH)
//   ci_failure — failed CI runs on main branch (via gh CLI)
//
// Configuration (env vars):
//   SWARM_TRIAGE_ENABLED=true
//   SWARM_TRIAGE_REPOS=/path/a,/path/b     comma-separated repo paths (required)
//   SWARM_TRIAGE_SESSION_ID=<id>           explicit target session
//                                           (if blank, auto-detect via triage_enabled=1 on session)
//   SWARM_TRIAGE_INTERVAL=30m              poll interval (default: 30m)
//   SWARM_TRIAGE_SIGNALS=all               or: stale_pr,vuln,ci_failure
//   SWARM_TRIAGE_MAX_GOALS_PER_CYCLE=3     anti-spam cap per scan cycle (default: 3)
//   SWARM_TRIAGE_MAX_PENDING_GOALS=10      backpressure: skip goal creation if session
//                                           already has this many active goals (default: 10)

const (
	triageDefaultInterval        = 30 * time.Minute
	triageDefaultMaxGoalsCycle   = 3
	triageDefaultMaxPendingGoals = 10
	triageCmdTimeout             = 5 * time.Minute
	triageStalePRDays            = 7
	triageVulnCooldown           = 7 * 24 * time.Hour
	triageStalePRCooldown        = 7 * 24 * time.Hour
	triageCIFailCooldown         = 24 * time.Hour
)

// ─── Types ────────────────────────────────────────────────────────────────────

type triageConfig struct {
	enabled         bool
	repos           []string // validated absolute paths
	sessionID       string   // explicit target; blank = auto-detect via triage_enabled=1
	interval        time.Duration
	signals         map[string]bool
	maxGoalsCycle   int
	maxPendingGoals int
}

type triageSignal struct {
	fingerprint string
	signalType  string
	repoPath    string
	title       string
	detail      string
	cooldown    time.Duration
}

// SwarmTriageFinding mirrors the DB row for API responses.
type SwarmTriageFinding struct {
	ID                string  `json:"id"`
	SessionID         string  `json:"session_id"`
	Fingerprint       string  `json:"fingerprint"`
	SignalType        string  `json:"signal_type"`
	RepoPath          string  `json:"repo_path"`
	Title             string  `json:"title"`
	Detail            string  `json:"detail"`
	GoalID            *string `json:"goal_id,omitempty"`
	Status            string  `json:"status"`
	FirstSeenAt       int64   `json:"first_seen_at"`
	LastSeenAt        int64   `json:"last_seen_at"`
	LastGoalCreatedAt *int64  `json:"last_goal_created_at,omitempty"`
	SuppressedAt      *int64  `json:"suppressed_at,omitempty"`
}

// ─── Config loading ───────────────────────────────────────────────────────────

func loadTriageConfig() (triageConfig, bool) {
	if os.Getenv("SWARM_TRIAGE_ENABLED") != "true" {
		return triageConfig{}, false
	}
	rawRepos := os.Getenv("SWARM_TRIAGE_REPOS")
	if rawRepos == "" {
		log.Printf("swarm/triage: SWARM_TRIAGE_ENABLED=true but SWARM_TRIAGE_REPOS not set — triage disabled")
		return triageConfig{}, false
	}

	var repos []string
	for _, r := range strings.Split(rawRepos, ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			log.Printf("swarm/triage: invalid repo path %q: %v — skipping", r, err)
			continue
		}
		// Resolve symlinks so allowlist checking is canonical
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			log.Printf("swarm/triage: repo path %q not found: %v — skipping", r, err)
			continue
		}
		repos = append(repos, resolved)
	}
	if len(repos) == 0 {
		log.Printf("swarm/triage: no valid repo paths found — triage disabled")
		return triageConfig{}, false
	}

	interval := triageDefaultInterval
	if iv := os.Getenv("SWARM_TRIAGE_INTERVAL"); iv != "" {
		if d, err := time.ParseDuration(iv); err == nil && d > 0 {
			interval = d
		}
	}

	signals := map[string]bool{
		"stale_pr":   true,
		"vuln":       true,
		"ci_failure": true,
	}
	if sv := os.Getenv("SWARM_TRIAGE_SIGNALS"); sv != "" && sv != "all" {
		for k := range signals {
			signals[k] = false
		}
		for _, s := range strings.Split(sv, ",") {
			signals[strings.TrimSpace(s)] = true
		}
	}

	maxGoalsCycle := triageDefaultMaxGoalsCycle
	if v := os.Getenv("SWARM_TRIAGE_MAX_GOALS_PER_CYCLE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxGoalsCycle = n
		}
	}
	maxPendingGoals := triageDefaultMaxPendingGoals
	if v := os.Getenv("SWARM_TRIAGE_MAX_PENDING_GOALS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxPendingGoals = n
		}
	}

	return triageConfig{
		enabled:         true,
		repos:           repos,
		sessionID:       strings.TrimSpace(os.Getenv("SWARM_TRIAGE_SESSION_ID")),
		interval:        interval,
		signals:         signals,
		maxGoalsCycle:   maxGoalsCycle,
		maxPendingGoals: maxPendingGoals,
	}, true
}

// ─── Poller ───────────────────────────────────────────────────────────────────

func startTriagePoller(ctx context.Context) {
	cfg, ok := loadTriageConfig()
	if !ok {
		return
	}
	log.Printf("swarm/triage: poller starting (interval=%s, repos=%d, signals=%v)",
		cfg.interval, len(cfg.repos), enabledSignalNames(cfg.signals))
	go func() {
		ticker := time.NewTicker(cfg.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				triageTick(ctx, cfg)
			}
		}
	}()
}

func enabledSignalNames(signals map[string]bool) []string {
	var names []string
	for k, v := range signals {
		if v {
			names = append(names, k)
		}
	}
	return names
}

func triageTick(ctx context.Context, cfg triageConfig) {
	sessionID, err := triageTargetSession(ctx, cfg)
	if err != nil {
		log.Printf("swarm/triage: no eligible session (%v) — findings will be recorded but no goals created", err)
		sessionID = ""
	}

	// Backpressure: skip goal creation if too many active goals already
	canCreate := sessionID != "" && triageSessionActiveGoals(ctx, sessionID) < cfg.maxPendingGoals
	if sessionID != "" && !canCreate {
		log.Printf("swarm/triage: session %s has ≥%d active goals — backpressure engaged, skipping goal creation",
			sessionID[:8], cfg.maxPendingGoals)
	}

	// Collect signals from all repos
	var signals []triageSignal
	for _, repoPath := range cfg.repos {
		if cfg.signals["stale_pr"] {
			signals = append(signals, scanStalePRs(ctx, repoPath)...)
		}
		if cfg.signals["vuln"] {
			signals = append(signals, scanVulns(ctx, repoPath)...)
		}
		if cfg.signals["ci_failure"] {
			signals = append(signals, scanCIFailures(ctx, repoPath)...)
		}
	}
	log.Printf("swarm/triage: scan found %d signals across %d repos", len(signals), len(cfg.repos))

	goalsCreated := 0
	for _, sig := range signals {
		if goalsCreated >= cfg.maxGoalsCycle {
			log.Printf("swarm/triage: max goals per cycle (%d) reached — deferring remaining signals", cfg.maxGoalsCycle)
			break
		}

		// Use the session ID for finding storage, or a sentinel if no session
		storageSID := sessionID
		if storageSID == "" {
			storageSID = "no-session"
		}

		findingID, shouldCreate, err := upsertTriageFinding(ctx, storageSID, sig)
		if err != nil {
			log.Printf("swarm/triage: upsert finding %s: %v", sig.fingerprint, err)
			continue
		}

		if !shouldCreate || !canCreate {
			continue
		}

		goalID, err := createTriageGoal(ctx, sessionID, sig.title, sig.detail)
		if err != nil {
			log.Printf("swarm/triage: create goal for finding %s: %v", findingID[:8], err)
			continue
		}

		if err := markFindingGoalCreated(ctx, sig.fingerprint, goalID); err != nil {
			log.Printf("swarm/triage: mark finding %s goal created: %v", findingID[:8], err)
		}

		writeSwarmEvent(ctx, sessionID, "", goalID, "triage_goal_created",
			sig.signalType+":"+sig.title[:min(40, len(sig.title))])
		goalsCreated++
		log.Printf("swarm/triage: goal created signal=%s repo=%s goal=%s",
			sig.signalType, filepath.Base(sig.repoPath), goalID[:8])
	}
}

// ─── Session targeting ────────────────────────────────────────────────────────

// triageTargetSession resolves the target session for goal creation.
// Priority: explicit SWARM_TRIAGE_SESSION_ID > session with triage_enabled=1.
// If no eligible session is found, returns an error (caller should record findings only).
func triageTargetSession(ctx context.Context, cfg triageConfig) (string, error) {
	if cfg.sessionID != "" {
		// Validate configured session exists and has at least one live agent
		var id string
		err := database.QueryRowContext(ctx,
			`SELECT s.id FROM swarm_sessions s
			 JOIN swarm_agents a ON a.session_id = s.id
			 WHERE s.id=? AND a.tmux_session IS NOT NULL
			 LIMIT 1`,
			cfg.sessionID,
		).Scan(&id)
		if err != nil {
			return "", fmt.Errorf("configured session %s has no live agents: %w", cfg.sessionID[:min(8, len(cfg.sessionID))], err)
		}
		return id, nil
	}

	// Auto-detect: sessions explicitly opted in via triage_enabled=1 with any live agent
	var id string
	err := database.QueryRowContext(ctx,
		`SELECT s.id FROM swarm_sessions s
		 JOIN swarm_agents a ON a.session_id = s.id
		 WHERE s.triage_enabled=1 AND a.tmux_session IS NOT NULL
		 ORDER BY a.last_event_ts DESC LIMIT 1`,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("no session with triage_enabled=1 and a live agent")
	}
	return id, nil
}

func triageSessionActiveGoals(ctx context.Context, sessionID string) int {
	var count int
	database.QueryRowContext(ctx, //nolint:errcheck
		"SELECT COUNT(*) FROM swarm_goals WHERE session_id=? AND status='active'",
		sessionID,
	).Scan(&count)
	return count
}

// ─── Finding management ───────────────────────────────────────────────────────

// upsertTriageFinding records or updates a finding via UPSERT.
// Returns (findingID, shouldCreateGoal, error).
// shouldCreateGoal is true when the cooldown has elapsed since the last goal creation.
func upsertTriageFinding(ctx context.Context, sessionID string, sig triageSignal) (string, bool, error) {
	now := time.Now().Unix()
	newID := generateSwarmID()

	// UPSERT: insert on first occurrence; update last_seen_at + content on conflict.
	// This means a recurring issue always has an up-to-date description.
	_, err := database.ExecContext(ctx,
		`INSERT INTO swarm_triage_findings
		     (id, session_id, fingerprint, signal_type, repo_path, title, detail, status, first_seen_at, last_seen_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'open', ?, ?)
		 ON CONFLICT(fingerprint) DO UPDATE SET
		     last_seen_at = excluded.last_seen_at,
		     title        = excluded.title,
		     detail       = excluded.detail`,
		newID, sessionID, sig.fingerprint, sig.signalType, sig.repoPath,
		sig.title, sig.detail, now, now,
	)
	if err != nil {
		return "", false, fmt.Errorf("upsert finding: %w", err)
	}

	// Read back the persisted row (may differ from newID if UPSERT hit existing row)
	var id, status string
	var lastGoalCreatedAt sql.NullInt64
	err = database.QueryRowContext(ctx,
		"SELECT id, status, last_goal_created_at FROM swarm_triage_findings WHERE fingerprint=?",
		sig.fingerprint,
	).Scan(&id, &status, &lastGoalCreatedAt)
	if err != nil {
		return "", false, fmt.Errorf("read finding: %w", err)
	}

	if status == "suppressed" {
		return id, false, nil
	}

	// Cooldown: only create a new goal if enough time has elapsed since the last one
	if lastGoalCreatedAt.Valid {
		elapsed := time.Duration(now-lastGoalCreatedAt.Int64) * time.Second
		if elapsed < sig.cooldown {
			return id, false, nil
		}
	}

	return id, true, nil
}

func markFindingGoalCreated(ctx context.Context, fingerprint, goalID string) error {
	_, err := database.ExecContext(ctx,
		"UPDATE swarm_triage_findings SET goal_id=?, last_goal_created_at=? WHERE fingerprint=?",
		goalID, time.Now().Unix(), fingerprint,
	)
	return err
}

// ─── Goal creation ────────────────────────────────────────────────────────────

// createTriageGoal creates a swarm goal via the same internal path as POST /goals.
// The description is prefixed with "[triage]" for audit trail visibility.
func createTriageGoal(ctx context.Context, sessionID, title, detail string) (string, error) {
	id := generateSwarmID()
	now := time.Now().Unix()
	desc := fmt.Sprintf("[triage] %s\n\n%s", title, detail)

	if _, err := database.ExecContext(ctx,
		"INSERT INTO swarm_goals (id,session_id,description,status,created_at,updated_at) VALUES (?,?,?,?,?,?)",
		id, sessionID, desc, "active", now, now,
	); err != nil {
		return "", fmt.Errorf("insert goal: %w", err)
	}

	goal := SwarmGoal{
		ID: id, SessionID: sessionID, Description: desc,
		Status: "active", CreatedAt: now, UpdatedAt: now,
	}
	writeSwarmEvent(ctx, sessionID, "", "", "goal_created", truncate(title, 80))
	go kickOffGoalSpecTask(context.Background(), sessionID, goal)
	swarmBroadcaster.schedule(sessionID)
	return id, nil
}

// ─── Command execution ────────────────────────────────────────────────────────

// triageExec runs a command in dir with a timeout. Sets a new process group
// (Setpgid) so the entire process tree is killed if the context times out.
func triageExec(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	tctx, cancel := context.WithTimeout(ctx, triageCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(tctx, name, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	out, err := cmd.Output()
	if tctx.Err() == context.DeadlineExceeded {
		if cmd.Process != nil {
			// Kill the entire process group to prevent orphans
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) //nolint:errcheck
		}
		return nil, fmt.Errorf("triage command %s timed out after %s", name, triageCmdTimeout)
	}
	return out, err
}

// repoOwnerRepo infers "owner/repo" from the git remote URL of a local repo.
func repoOwnerRepo(ctx context.Context, repoPath string) (string, error) {
	out, err := triageExec(ctx, repoPath, "git", "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("git remote get-url: %w", err)
	}
	raw := strings.TrimSpace(string(out))

	// SSH: git@github.com:owner/repo.git
	if strings.HasPrefix(raw, "git@github.com:") {
		ownerRepo := strings.TrimPrefix(raw, "git@github.com:")
		return strings.TrimSuffix(ownerRepo, ".git"), nil
	}
	// HTTPS: https://github.com/owner/repo[.git]
	if idx := strings.Index(raw, "github.com/"); idx >= 0 {
		ownerRepo := raw[idx+len("github.com/"):]
		return strings.TrimSuffix(ownerRepo, ".git"), nil
	}
	return "", fmt.Errorf("unsupported remote URL: %s", raw)
}

// ─── Signal: stale_pr ─────────────────────────────────────────────────────────

func scanStalePRs(ctx context.Context, repoPath string) []triageSignal {
	ownerRepo, err := repoOwnerRepo(ctx, repoPath)
	if err != nil {
		log.Printf("swarm/triage: stale_pr: get remote for %s: %v", filepath.Base(repoPath), err)
		return nil
	}

	out, err := triageExec(ctx, repoPath, "gh", "pr", "list",
		"--repo", ownerRepo, "--state", "open",
		"--json", "number,title,createdAt,isDraft",
		"--limit", "20",
	)
	if err != nil {
		log.Printf("swarm/triage: stale_pr: gh pr list %s: %v", ownerRepo, err)
		return nil
	}

	var prs []struct {
		Number    int    `json:"number"`
		Title     string `json:"title"`
		CreatedAt string `json:"createdAt"`
		IsDraft   bool   `json:"isDraft"`
	}
	if err := json.Unmarshal(out, &prs); err != nil {
		log.Printf("swarm/triage: stale_pr: parse response: %v", err)
		return nil
	}

	repoName := filepath.Base(repoPath)
	threshold := time.Now().AddDate(0, 0, -triageStalePRDays)
	var signals []triageSignal
	for _, pr := range prs {
		if pr.IsDraft {
			continue
		}
		t, err := time.Parse(time.RFC3339, pr.CreatedAt)
		if err != nil || t.After(threshold) {
			continue
		}
		signals = append(signals, triageSignal{
			fingerprint: fmt.Sprintf("stale_pr:%s:%d", ownerRepo, pr.Number),
			signalType:  "stale_pr",
			repoPath:    repoPath,
			title:       fmt.Sprintf("Review stale PR #%d in %s: %s", pr.Number, repoName, pr.Title),
			detail: fmt.Sprintf("PR #%d (%s) in %s has been open since %s (>%d days). "+
				"Review, update, or close it. Run: gh pr view %d --repo %s",
				pr.Number, pr.Title, ownerRepo, t.Format("2006-01-02"),
				triageStalePRDays, pr.Number, ownerRepo),
			cooldown: triageStalePRCooldown,
		})
	}
	return signals
}

// ─── Signal: vuln ─────────────────────────────────────────────────────────────

// vulncheckLine is one JSON object in the line-delimited output of govulncheck -json.
type vulncheckLine struct {
	Finding *struct {
		OSVID string `json:"osv_id"`
	} `json:"finding"`
}

func scanVulns(ctx context.Context, repoPath string) []triageSignal {
	// Only scan Go modules
	if _, err := os.Stat(filepath.Join(repoPath, "go.mod")); err != nil {
		return nil
	}
	// Skip gracefully if govulncheck is not installed
	if _, err := exec.LookPath("govulncheck"); err != nil {
		return nil
	}

	out, err := triageExec(ctx, repoPath, "govulncheck", "-json", "./...")
	// govulncheck exits non-zero when vulnerabilities are found — that's expected
	if err != nil && len(out) == 0 {
		log.Printf("swarm/triage: vuln: govulncheck %s: %v", filepath.Base(repoPath), err)
		return nil
	}

	// Parse line-delimited JSON; collect unique OSV IDs
	seen := map[string]bool{}
	var osvIDs []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var v vulncheckLine
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			continue
		}
		if v.Finding != nil && v.Finding.OSVID != "" && !seen[v.Finding.OSVID] {
			seen[v.Finding.OSVID] = true
			osvIDs = append(osvIDs, v.Finding.OSVID)
		}
	}
	if len(osvIDs) == 0 {
		return nil
	}

	repoName := filepath.Base(repoPath)
	idList := strings.Join(osvIDs, ", ")
	return []triageSignal{{
		fingerprint: fmt.Sprintf("vuln:%s:%s", repoPath, idList),
		signalType:  "vuln",
		repoPath:    repoPath,
		title:       fmt.Sprintf("Fix %d Go vulnerability/vulnerabilities in %s", len(osvIDs), repoName),
		detail: fmt.Sprintf("govulncheck found %d vulnerability/vulnerabilities in %s: %s. "+
			"Run `govulncheck ./...` in %s for full details and remediation advice.",
			len(osvIDs), repoName, idList, repoPath),
		cooldown: triageVulnCooldown,
	}}
}

// ─── Signal: ci_failure ───────────────────────────────────────────────────────

func scanCIFailures(ctx context.Context, repoPath string) []triageSignal {
	ownerRepo, err := repoOwnerRepo(ctx, repoPath)
	if err != nil {
		return nil
	}

	out, err := triageExec(ctx, repoPath, "gh", "run", "list",
		"--repo", ownerRepo, "--branch", "main",
		"--limit", "5",
		"--json", "databaseId,status,conclusion,workflowName,headSha",
	)
	if err != nil {
		log.Printf("swarm/triage: ci_failure: gh run list %s: %v", ownerRepo, err)
		return nil
	}

	var runs []struct {
		DatabaseID   int64  `json:"databaseId"`
		Status       string `json:"status"`
		Conclusion   string `json:"conclusion"`
		WorkflowName string `json:"workflowName"`
		HeadSha      string `json:"headSha"`
	}
	if err := json.Unmarshal(out, &runs); err != nil {
		log.Printf("swarm/triage: ci_failure: parse response: %v", err)
		return nil
	}

	repoName := filepath.Base(repoPath)
	var signals []triageSignal
	for _, run := range runs {
		if run.Status != "completed" || run.Conclusion != "failure" {
			continue
		}
		headShort := run.HeadSha
		if len(headShort) > 8 {
			headShort = headShort[:8]
		}
		signals = append(signals, triageSignal{
			fingerprint: fmt.Sprintf("ci_failure:%s:%d", ownerRepo, run.DatabaseID),
			signalType:  "ci_failure",
			repoPath:    repoPath,
			title:       fmt.Sprintf("Fix failing CI on %s main: %s", repoName, run.WorkflowName),
			detail: fmt.Sprintf("CI workflow '%s' failed on main branch in %s (run #%d, commit %s). "+
				"Investigate and fix the failure. Check: gh run view %d --repo %s --log-failed",
				run.WorkflowName, repoName, run.DatabaseID, headShort,
				run.DatabaseID, ownerRepo),
			cooldown: triageCIFailCooldown,
		})
	}
	return signals
}

// ─── API handlers ─────────────────────────────────────────────────────────────

// handleSwarmTriageAPI dispatches triage sub-routes for a session.
//
//	GET  .../triage/findings             — list findings (last 50)
//	POST .../triage/findings/:id/suppress — mark suppressed
//	POST .../triage/run                  — trigger immediate scan
func handleSwarmTriageAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, sessionID string, pathParts []string) {
	w.Header().Set("Content-Type", "application/json")

	if len(pathParts) == 0 {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown triage endpoint"}) //nolint:errcheck
		return
	}

	switch pathParts[0] {
	case "run":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		cfg, ok := loadTriageConfig()
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "triage not enabled"}) //nolint:errcheck
			return
		}
		// Override session ID so the scan targets this specific session
		cfg.sessionID = sessionID
		go triageTick(context.Background(), cfg)
		json.NewEncoder(w).Encode(map[string]string{"status": "scan triggered"}) //nolint:errcheck

	case "findings":
		if len(pathParts) == 1 {
			listTriageFindings(w, r, ctx, sessionID)
			return
		}
		// POST .../triage/findings/:id/suppress
		if len(pathParts) == 3 && pathParts[2] == "suppress" && r.Method == http.MethodPost {
			suppressTriageFinding(w, r, ctx, pathParts[1])
			return
		}
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown findings endpoint"}) //nolint:errcheck

	default:
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown triage endpoint"}) //nolint:errcheck
	}
}

func listTriageFindings(w http.ResponseWriter, r *http.Request, ctx context.Context, sessionID string) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rows, err := database.QueryContext(ctx,
		`SELECT id, session_id, fingerprint, signal_type, repo_path, title, detail,
		        goal_id, status, first_seen_at, last_seen_at, last_goal_created_at, suppressed_at
		 FROM swarm_triage_findings
		 WHERE session_id=?
		 ORDER BY last_seen_at DESC LIMIT 50`,
		sessionID,
	)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
		return
	}
	defer rows.Close()

	var findings []SwarmTriageFinding
	for rows.Next() {
		var f SwarmTriageFinding
		var goalID sql.NullString
		var lastGoalCreatedAt, suppressedAt sql.NullInt64
		rows.Scan( //nolint:errcheck
			&f.ID, &f.SessionID, &f.Fingerprint, &f.SignalType, &f.RepoPath,
			&f.Title, &f.Detail, &goalID, &f.Status,
			&f.FirstSeenAt, &f.LastSeenAt, &lastGoalCreatedAt, &suppressedAt,
		)
		if goalID.Valid {
			f.GoalID = &goalID.String
		}
		if lastGoalCreatedAt.Valid {
			f.LastGoalCreatedAt = &lastGoalCreatedAt.Int64
		}
		if suppressedAt.Valid {
			f.SuppressedAt = &suppressedAt.Int64
		}
		findings = append(findings, f)
	}
	if findings == nil {
		findings = []SwarmTriageFinding{}
	}
	json.NewEncoder(w).Encode(findings) //nolint:errcheck
}

func suppressTriageFinding(w http.ResponseWriter, r *http.Request, ctx context.Context, findingID string) {
	res, err := database.ExecContext(ctx,
		"UPDATE swarm_triage_findings SET status='suppressed', suppressed_at=? WHERE id=?",
		time.Now().Unix(), findingID,
	)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "finding not found"}) //nolint:errcheck
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "suppressed"}) //nolint:errcheck
}
