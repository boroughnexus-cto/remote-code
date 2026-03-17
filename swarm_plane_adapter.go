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

type planeConfig struct {
	apiURL      string
	apiKey      string
	workspace   string
	projectID   string
	sessionID   string
	doneStateID string // resolved at startup
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
	req.Header.Set("X-Api-Token", cfg.apiKey)
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
func planeListStartedIssues(ctx context.Context, cfg *planeConfig) ([]struct {
	ID          string `json:"id"`
	Title       string `json:"name"`
	Description string `json:"description_stripped"`
}, error) {
	path := fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/issues/?state_group=started&per_page=50",
		cfg.workspace, cfg.projectID)
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
		go injectGoalToSiBot(context.Background(), cfg.sessionID, goal)

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

// startPlaneAdapter is the background poller goroutine.
func startPlaneAdapter(ctx context.Context) {
	cfg, ok := loadPlaneConfig()
	if !ok {
		log.Printf("swarm/plane: adapter disabled (set PLANE_API_URL, PLANE_API_KEY, PLANE_WORKSPACE, PLANE_PROJECT_ID, PLANE_TARGET_SESSION_ID)")
		return
	}

	// Resolve done state at startup
	if cfg.doneStateID == "" {
		cfg.doneStateID = planeFetchDoneStateID(ctx, cfg)
	}
	log.Printf("swarm/plane: adapter started (project=%s, session=%s, doneState=%s)",
		cfg.projectID[:8], cfg.sessionID[:8], cfg.doneStateID)

	ticker := time.NewTicker(planePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			planeSyncStartedIssues(ctx, cfg)
		}
	}
}
