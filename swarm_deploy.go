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

// ─── Auto-deploy ──────────────────────────────────────────────────────────────
//
// Triggered by the CI poller when a PR's CI becomes "success" and the PR is
// merged. Calls Komodo's DeployStack API and notifies the owning agent.
//
// Required env vars:
//   SWARM_AUTO_DEPLOY_STACK  Komodo stack name to deploy
//   KOMODO_API_URL           e.g. http://komodo.internal:9120
//   KOMODO_API_KEY           Komodo API key
//   KOMODO_API_SECRET        Komodo API secret
//
// Idempotent: deploy_triggered_at is set atomically before calling Komodo;
// repeated CI polls won't fire duplicate deploys.

type deployConfig struct {
	stackName string
	apiURL    string
	apiKey    string
	apiSecret string
}

func loadDeployConfig() (*deployConfig, bool) {
	c := &deployConfig{
		stackName: os.Getenv("SWARM_AUTO_DEPLOY_STACK"),
		apiURL:    os.Getenv("KOMODO_API_URL"),
		apiKey:    os.Getenv("KOMODO_API_KEY"),
		apiSecret: os.Getenv("KOMODO_API_SECRET"),
	}
	if c.stackName == "" || c.apiURL == "" || c.apiKey == "" || c.apiSecret == "" {
		return nil, false
	}
	return c, true
}

// ghIsPRMerged checks whether a PR URL points to a merged PR.
// Uses the gh CLI (already used for CI polling) to call the GitHub API.
func ghIsPRMerged(ctx context.Context, prURL string) bool {
	// prURL format: https://github.com/owner/repo/pull/N
	trimmed := strings.TrimPrefix(prURL, "https://github.com/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return false
	}
	owner, repo, prNum := parts[0], parts[1], parts[3]
	apiPath := fmt.Sprintf("repos/%s/%s/pulls/%s", owner, repo, prNum)

	out, err := ghAPI(ctx, apiPath)
	if err != nil {
		return false
	}
	var pr struct {
		Merged bool `json:"merged"`
		State  string `json:"state"`
	}
	if json.Unmarshal(out, &pr) != nil {
		return false
	}
	return pr.Merged
}

// triggerAutoDeploy fires a Komodo DeployStack call for a merged PR task.
// Idempotent: uses deploy_triggered_at as a CAS guard.
func triggerAutoDeploy(ctx context.Context, taskID, sessionID, agentID, prURL string) {
	cfg, ok := loadDeployConfig()
	if !ok {
		return
	}

	// CAS: only proceed if deploy_triggered_at is NULL
	res, err := database.ExecContext(ctx,
		"UPDATE swarm_tasks SET deploy_triggered_at=? WHERE id=? AND deploy_triggered_at IS NULL",
		time.Now().Unix(), taskID,
	)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return // already triggered
	}

	log.Printf("swarm/deploy: triggering auto-deploy stack=%s task=%s", cfg.stackName, taskID[:8])

	execID, err := komodoDeployStack(ctx, cfg)
	if err != nil {
		log.Printf("swarm/deploy: Komodo deploy failed: %v", err)
		writeSwarmEvent(ctx, sessionID, agentID, taskID, "auto_deploy_failed", err.Error())
		if agentID != "" {
			injectToSwarmAgent(ctx, agentID, fmt.Sprintf( //nolint:errcheck
				"## Auto-deploy Failed ⚠️\n\nKomodo deploy of stack `%s` failed: %v\n\nPR: %s\n\nPlease deploy manually.",
				cfg.stackName, err, prURL))
		}
		return
	}

	writeSwarmEvent(ctx, sessionID, agentID, taskID, "auto_deployed", fmt.Sprintf("stack=%s exec=%s", cfg.stackName, execID))
	if agentID != "" {
		injectToSwarmAgent(ctx, agentID, fmt.Sprintf( //nolint:errcheck
			"## Auto-deploy Triggered ✅\n\nPR merged + CI green → Komodo deploy started for stack `%s`.\nExecution ID: %s\n\nMonitor at: %s\n\nProceed to the document phase once deploy is confirmed.",
			cfg.stackName, execID, cfg.apiURL))
	}
}

// komodoDeployStack calls the Komodo API to deploy a stack.
// Returns the execution ID on success.
func komodoDeployStack(ctx context.Context, cfg *deployConfig) (string, error) {
	body, err := json.Marshal(map[string]interface{}{
		"stack": map[string]string{"name": cfg.stackName},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(cfg.apiURL, "/")+"/api/execute/DeployStack",
		strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", cfg.apiKey)
	req.Header.Set("X-Api-Secret", cfg.apiSecret)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("Komodo request failed: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("Komodo returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &result); err != nil || result.ID == "" {
		// Some Komodo versions return the ID differently
		return "unknown", nil
	}
	return result.ID, nil
}
