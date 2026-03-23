package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ─── Plane adapter ─────────────────────────────────────────────────────────────
//
// Polls Plane for "started" issues in a configured project and creates swarm
// goals for any unseen issue. When a swarm goal completes, closes the linked
// Plane issue.
//
// Required env vars (adapter is disabled if any are absent):
//   PLANE_API_URL          e.g. http://100.74.34.7:8300
//   PLANE_API_KEY          Plane API token
//   PLANE_WORKSPACE        workspace slug, e.g. thomkernet
//   PLANE_PROJECT_ID       UUID of the target Plane project
//   PLANE_TARGET_SESSION_ID  swarm session to create goals in
//
// Optional:
//   PLANE_DONE_STATE_ID    UUID of the "Done" state; auto-detected if absent

const planePollInterval = 60 * time.Second

// ─── Work queue cache ──────────────────────────────────────────────────────────
//
// A background goroutine refreshes Plane work queue items for all configured
// projects every planePollInterval. The TUI work queue panel reads from this
// cache, so the W key is instant and never blocks on the Plane API.

type planeWorkQueueCache struct {
	mu    sync.RWMutex
	items map[string][]WorkQueueItem // key: "projectID:stateGroup,stateGroup2,..."
}

var globalPlaneCache = &planeWorkQueueCache{
	items: make(map[string][]WorkQueueItem),
}

func (c *planeWorkQueueCache) set(key string, items []WorkQueueItem) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = items
}

func (c *planeWorkQueueCache) get(key string) ([]WorkQueueItem, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.items[key]
	return v, ok
}

// cacheKey returns the cache map key for a project + state groups combination.
func cacheKey(projectID string, groups []string) string {
	return projectID + ":" + strings.Join(groups, ",")
}

type planeConfig struct {
	apiURL      string
	apiKey      string
	workspace   string
	projectID   string
	sessionID   string
	doneStateID string // resolved at startup
	// LabelFilter is an optional Plane label ID. When set, only issues with
	// this label are synced. NOTE: Plane API label filter param is "label_ids"
	// (comma-separated UUIDs). Verify against your Plane instance if issues
	// are not being filtered as expected.
	LabelFilter string
}

func loadPlaneConfig() (*planeConfig, bool) {
	c := &planeConfig{
		apiURL:      os.Getenv("PLANE_API_URL"),
		apiKey:      os.Getenv("PLANE_API_KEY"),
		workspace:   os.Getenv("PLANE_WORKSPACE"),
		projectID:   os.Getenv("PLANE_PROJECT_ID"),
		sessionID:   os.Getenv("PLANE_TARGET_SESSION_ID"),
		doneStateID: os.Getenv("PLANE_DONE_STATE_ID"),
	}
	if c.apiURL == "" || c.apiKey == "" || c.workspace == "" || c.projectID == "" || c.sessionID == "" {
		return nil, false
	}
	return c, true
}

// planeReq performs an authenticated request against the Plane API.
func planeReq(ctx context.Context, cfg *planeConfig, method, path string, body interface{}) ([]byte, int, error) {
	var rb io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rb = strings.NewReader(string(b))
	}
	url := strings.TrimRight(cfg.apiURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, url, rb)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("x-api-key", cfg.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return data, resp.StatusCode, nil
}

// planeFetchDoneStateID fetches the project states and returns the UUID of the
// first state whose group is "completed".
func planeFetchDoneStateID(ctx context.Context, cfg *planeConfig) string {
	path := fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/states/", cfg.workspace, cfg.projectID)
	data, status, err := planeReq(ctx, cfg, "GET", path, nil)
	if err != nil || status != 200 {
		log.Printf("swarm/plane: failed to fetch states (status=%d): %v", status, err)
		return ""
	}
	var resp struct {
		Results []struct {
			ID    string `json:"id"`
			Group string `json:"group"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return ""
	}
	for _, s := range resp.Results {
		if s.Group == "completed" {
			return s.ID
		}
	}
	return ""
}

// planeListStartedIssues returns issues in the "started" state group.
// When cfg.LabelFilter is set it appends &label_ids=<id> to narrow results to
// issues carrying that label. (Plane API param name verified as "label_ids".)
func planeListStartedIssues(ctx context.Context, cfg *planeConfig) ([]struct {
	ID          string `json:"id"`
	Title       string `json:"name"`
	Description string `json:"description_stripped"`
}, error) {
	path := fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/issues/?state_group=started&per_page=50",
		cfg.workspace, cfg.projectID)
	if cfg.LabelFilter != "" {
		path += "&label_ids=" + cfg.LabelFilter
	}
	data, status, err := planeReq(ctx, cfg, "GET", path, nil)
	if err != nil || status != 200 {
		return nil, fmt.Errorf("plane list issues: status=%d err=%v", status, err)
	}
	var resp struct {
		Results []struct {
			ID          string `json:"id"`
			Title       string `json:"name"`
			Description string `json:"description_stripped"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// planeSyncStartedIssues creates swarm goals for any Plane "started" issues
// that don't yet have a matching goal in this session.
func planeSyncStartedIssues(ctx context.Context, cfg *planeConfig) {
	issues, err := planeListStartedIssues(ctx, cfg)
	if err != nil {
		log.Printf("swarm/plane: list issues: %v", err)
		return
	}

	for _, issue := range issues {
		// Check if we already have a goal for this Plane issue
		var count int
		database.QueryRowContext(ctx, //nolint:errcheck
			"SELECT COUNT(*) FROM swarm_goals WHERE plane_issue_id=? AND session_id=?",
			issue.ID, cfg.sessionID,
		).Scan(&count)
		if count > 0 {
			continue
		}

		// Build description
		desc := issue.Title
		if issue.Description != "" {
			desc += "\n\n" + issue.Description
		}

		// Create swarm goal
		id := generateSwarmID()
		now := time.Now().Unix()
		_, err := database.ExecContext(ctx,
			`INSERT INTO swarm_goals
			 (id, session_id, description, status, plane_issue_id, plane_synced_at, created_at, updated_at)
			 VALUES (?,?,?,?,?,?,?,?)`,
			id, cfg.sessionID, desc, "active", issue.ID, now, now, now,
		)
		if err != nil {
			log.Printf("swarm/plane: insert goal for issue %s: %v", issue.ID[:8], err)
			continue
		}
		writeSwarmEvent(ctx, cfg.sessionID, "", "", "goal_created", truncate(desc, 80))
		swarmBroadcaster.schedule(cfg.sessionID)

		// Trigger Talos phases + orchestrator injection
		goal := SwarmGoal{ID: id, SessionID: cfg.sessionID, Description: desc, Status: "active"}
		go kickOffGoalSpecTask(context.Background(), cfg.sessionID, goal)

		log.Printf("swarm/plane: created goal %s for Plane issue %s", id[:8], issue.ID[:8])
	}
}

// planeAutoCloseGoal closes the linked Plane issue when a goal completes.
// Called from reconcileGoal (as a goroutine) when all tasks reach terminal state.
// Idempotent: plane_synced_at is updated so re-runs are harmless.
func planeAutoCloseGoal(ctx context.Context, goalID string) {
	cfg, ok := loadPlaneConfig()
	if !ok {
		return
	}

	var planeIssueID string
	var planeSyncedAt int64
	database.QueryRowContext(ctx, //nolint:errcheck
		"SELECT COALESCE(plane_issue_id,''), COALESCE(plane_synced_at,0) FROM swarm_goals WHERE id=?",
		goalID,
	).Scan(&planeIssueID, &planeSyncedAt)

	if planeIssueID == "" {
		return
	}

	// Determine done state
	doneStateID := cfg.doneStateID
	if doneStateID == "" {
		doneStateID = planeFetchDoneStateID(ctx, cfg)
		if doneStateID == "" {
			log.Printf("swarm/plane: cannot auto-close goal %s — no done state found", goalID[:8])
			return
		}
	}

	path := fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/issues/%s/",
		cfg.workspace, cfg.projectID, planeIssueID)
	_, status, err := planeReq(ctx, cfg, "PATCH", path, map[string]string{"state": doneStateID})
	if err != nil || (status != 200 && status != 204) {
		log.Printf("swarm/plane: close issue %s: status=%d err=%v", planeIssueID[:8], status, err)
		return
	}

	now := time.Now().Unix()
	database.ExecContext(ctx, //nolint:errcheck
		"UPDATE swarm_goals SET plane_synced_at=? WHERE id=?", now, goalID)

	log.Printf("swarm/plane: closed Plane issue %s for goal %s", planeIssueID[:8], goalID[:8])
}

// planeAutoCloseTask closes a Plane issue linked to a completed task.
// Accepts the Plane issue UUID directly — DB idempotency and plane_synced_at
// updates are handled by the caller (swarm.go done-stage handler).
// Returns nil on success, non-nil on failure (caller may retry or log).
func planeAutoCloseTask(ctx context.Context, planeIssueID string) error {
	cfg, ok := loadPlaneConfig()
	if !ok {
		return fmt.Errorf("plane config not set")
	}

	doneStateID := cfg.doneStateID
	if doneStateID == "" {
		doneStateID = planeFetchDoneStateID(ctx, cfg)
		if doneStateID == "" {
			return fmt.Errorf("swarm/plane: no done state found for task issue %s", safePrefix(planeIssueID, 8))
		}
	}

	path := fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/issues/%s/",
		cfg.workspace, cfg.projectID, planeIssueID)
	_, status, err := planeReq(ctx, cfg, "PATCH", path, map[string]string{"state": doneStateID})
	if err != nil {
		return fmt.Errorf("swarm/plane: close task issue %s: %w", safePrefix(planeIssueID, 8), err)
	}
	if status != 200 && status != 204 {
		return fmt.Errorf("swarm/plane: close task issue %s: unexpected status %d", safePrefix(planeIssueID, 8), status)
	}

	log.Printf("swarm/plane: closed Plane issue %s for task", safePrefix(planeIssueID, 8))
	return nil
}

// safePrefix returns the first n characters of s, or the full string if shorter.
func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// planeSyncSession syncs a single autopilot session: creates goals for any
// Plane "started" issues in the given project that don't yet have a goal.
// Uses the shared planeConfig (API URL/key/workspace) with a session-specific project.
func planeSyncSession(ctx context.Context, projectID, sessionID string) {
	baseCfg, ok := loadPlaneConfig()
	if !ok {
		// No base Plane config; can't sync
		return
	}
	cfg := &planeConfig{
		apiURL:      baseCfg.apiURL,
		apiKey:      baseCfg.apiKey,
		workspace:   baseCfg.workspace,
		projectID:   projectID,
		sessionID:   sessionID,
		doneStateID: baseCfg.doneStateID,
	}
	if cfg.doneStateID == "" {
		cfg.doneStateID = planeFetchDoneStateID(ctx, cfg)
	}
	planeSyncStartedIssues(ctx, cfg)
}

// startPlaneAdapter is the background poller goroutine.
// It syncs the env-var-configured session AND any DB autopilot sessions.
// Runs immediately at startup, then every planePollInterval.
func startPlaneAdapter(ctx context.Context) {
	baseCfg, envOK := loadPlaneConfig()
	if envOK {
		// Resolve done state once
		if baseCfg.doneStateID == "" {
			baseCfg.doneStateID = planeFetchDoneStateID(ctx, baseCfg)
		}
		log.Printf("swarm/plane: adapter started (env project=%s, session=%s)",
			baseCfg.projectID[:8], baseCfg.sessionID[:8])
	} else {
		log.Printf("swarm/plane: env-var session not configured; will poll DB autopilot sessions only")
	}

	poll := func() {
		if envOK {
			planeSyncStartedIssues(ctx, baseCfg)
		}
		syncAllAutopilotSessions(ctx, baseCfg)
		refreshWorkQueueCache(ctx, baseCfg)
	}

	// Run immediately at startup so issues are available without waiting for first tick.
	poll()

	ticker := time.NewTicker(planePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
}

// refreshWorkQueueCache pre-populates the in-memory work queue cache for all
// configured projects (env-var PLANE_PROJECT_ID + all DB autopilot sessions).
// Requires only PLANE_API_URL, PLANE_API_KEY, PLANE_WORKSPACE — does not need
// the full adapter config, so it works even when PLANE_TARGET_SESSION_ID is unset.
func refreshWorkQueueCache(ctx context.Context, baseCfg *planeConfig) {
	// Need at minimum the three base vars (checked inside planeFetchWorkQueueItems)
	if os.Getenv("PLANE_API_URL") == "" || os.Getenv("PLANE_API_KEY") == "" || os.Getenv("PLANE_WORKSPACE") == "" {
		return
	}
	defaultGroups := []string{"backlog", "unstarted"}

	// Collect all project IDs to cache
	projectIDs := make(map[string]struct{})
	if baseCfg != nil && baseCfg.projectID != "" {
		projectIDs[baseCfg.projectID] = struct{}{}
	}
	// Also pick up PLANE_PROJECT_ID even when full adapter config is incomplete
	if pid := os.Getenv("PLANE_PROJECT_ID"); pid != "" {
		projectIDs[pid] = struct{}{}
	}

	rows, err := database.QueryContext(ctx,
		`SELECT COALESCE(autopilot_plane_project_id,'') FROM swarm_sessions
		 WHERE autopilot_enabled=1 AND autopilot_plane_project_id IS NOT NULL AND autopilot_plane_project_id != ''`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var pid string
			rows.Scan(&pid) //nolint:errcheck
			if pid != "" {
				projectIDs[pid] = struct{}{}
			}
		}
	}

	if len(projectIDs) == 0 {
		return
	}
	for pid := range projectIDs {
		items, err := planeFetchWorkQueueItems(ctx, pid, defaultGroups)
		if err != nil {
			log.Printf("swarm/plane: cache refresh for project %s: %v", shortID(pid), err)
			continue
		}
		globalPlaneCache.set(cacheKey(pid, defaultGroups), items)
		log.Printf("swarm/plane: cached %d work queue items for project %s", len(items), shortID(pid))
	}
}

// syncAllAutopilotSessions queries for sessions with autopilot_enabled=1
// and syncs each with its configured Plane project.
func syncAllAutopilotSessions(ctx context.Context, baseCfg *planeConfig) {
	rows, err := database.QueryContext(ctx,
		`SELECT id, COALESCE(autopilot_plane_project_id,''), COALESCE(autopilot_label_filter,'')
		 FROM swarm_sessions
		 WHERE autopilot_enabled=1 AND autopilot_plane_project_id IS NOT NULL AND autopilot_plane_project_id != ''`,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var sessionID, projectID, labelFilter string
		if err := rows.Scan(&sessionID, &projectID, &labelFilter); err != nil {
			continue
		}
		// Skip if this is the same as the env-var session (already synced above)
		if baseCfg != nil && sessionID == baseCfg.sessionID && projectID == baseCfg.projectID {
			continue
		}
		if baseCfg == nil {
			// No base config; autopilot sessions without base Plane config won't work
			log.Printf("swarm/plane: autopilot session %s skipped — no PLANE_API_URL/KEY/WORKSPACE configured", sessionID[:8])
			continue
		}
		cfg := &planeConfig{
			apiURL:      baseCfg.apiURL,
			apiKey:      baseCfg.apiKey,
			workspace:   baseCfg.workspace,
			projectID:   projectID,
			sessionID:   sessionID,
			doneStateID: baseCfg.doneStateID,
			LabelFilter: labelFilter,
		}
		planeSyncStartedIssues(ctx, cfg)
	}
}

// planeFetchWorkQueueItems returns Plane issues for a given project and state groups.
// Used by the TUI work queue panel API endpoint.
// Only requires PLANE_API_URL, PLANE_API_KEY, PLANE_WORKSPACE — not the adapter-specific vars.
func planeFetchWorkQueueItems(ctx context.Context, projectID string, stateGroups []string) ([]WorkQueueItem, error) {
	apiURL := os.Getenv("PLANE_API_URL")
	apiKey := os.Getenv("PLANE_API_KEY")
	workspace := os.Getenv("PLANE_WORKSPACE")
	if apiURL == "" || apiKey == "" || workspace == "" {
		return nil, fmt.Errorf("Plane not configured (need PLANE_API_URL, PLANE_API_KEY, PLANE_WORKSPACE)")
	}
	cfg := &planeConfig{
		apiURL:    apiURL,
		apiKey:    apiKey,
		workspace: workspace,
		projectID: projectID,
	}

	var items []WorkQueueItem
	for _, group := range stateGroups {
		path := fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/issues/?state_group=%s&per_page=50",
			cfg.workspace, cfg.projectID, group)
		data, status, err := planeReq(ctx, cfg, "GET", path, nil)
		if err != nil || status != 200 {
			continue
		}
		var resp struct {
			Results []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Priority string `json:"priority"`
				SequenceID int  `json:"sequence_id"`
			} `json:"results"`
		}
		if json.Unmarshal(data, &resp) != nil {
			continue
		}
		for _, r := range resp.Results {
			items = append(items, WorkQueueItem{
				PlaneIssueID: r.ID,
				Title:        r.Name,
				Priority:     r.Priority,
				SequenceID:   r.SequenceID,
				StateGroup:   group,
			})
		}
	}
	return items, nil
}
