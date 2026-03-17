package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ─── CI poller ────────────────────────────────────────────────────────────────
//
// Polls GitHub Actions for open PRs that have been linked to swarm tasks via the
// ci_run_url column. When CI status changes, it:
//   - Updates ci_status and ci_checked_at on the task
//   - Injects a brief to the assigned agent if status changed (dedup via ci_last_notified_status)
//   - Broadcasts the session so the TUI reflects the new status

const ciPollInterval = 60 * time.Second

// startCIPoller starts the background CI polling loop if enabled.
// Requires SWARM_CI_ENABLED=true and gh CLI authenticated.
func startCIPoller(ctx context.Context) {
	if os.Getenv("SWARM_CI_ENABLED") != "true" {
		return
	}
	// Startup probe: verify gh is authenticated
	if out, err := exec.CommandContext(ctx, "gh", "auth", "status").CombinedOutput(); err != nil {
		log.Printf("swarm/ci: gh auth status failed — CI poller disabled: %s", strings.TrimSpace(string(out)))
		return
	}
	log.Printf("swarm/ci: poller starting (interval=%s)", ciPollInterval)
	go func() {
		ticker := time.NewTicker(ciPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pollAllOpenPRs(ctx)
			}
		}
	}()
}

// pollAllOpenPRs finds all tasks with a ci_run_url set and checks each.
func pollAllOpenPRs(ctx context.Context) {
	rows, err := database.QueryContext(ctx,
		`SELECT id, session_id, ci_run_url, COALESCE(ci_last_notified_status,''), COALESCE(agent_id,'')
		 FROM swarm_tasks
		 WHERE ci_run_url IS NOT NULL
		   AND stage NOT IN ('complete','failed','cancelled','timed_out')`,
	)
	if err != nil {
		log.Printf("swarm/ci: query open PR tasks: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var taskID, sessionID, runURL, lastNotified, agentID string
		if err := rows.Scan(&taskID, &sessionID, &runURL, &lastNotified, &agentID); err != nil {
			continue
		}
		checkPRCI(ctx, taskID, sessionID, runURL, lastNotified, agentID)
	}
}

// ciRunStatus represents the result of a GitHub Actions run query.
type ciRunStatus struct {
	Status     string // queued | in_progress | completed
	Conclusion string // success | failure | cancelled | timed_out | skipped | "" (not done)
}

// checkPRCI checks CI status for a single task and notifies if changed.
func checkPRCI(ctx context.Context, taskID, sessionID, runURL, lastNotified, agentID string) {
	status, err := fetchCIStatus(ctx, runURL)
	if err != nil {
		log.Printf("swarm/ci: fetchCIStatus task=%s: %v", taskID[:8], err)
		return
	}

	// Compute a canonical status string for dedup
	ciStatus := status.Status
	if status.Status == "completed" {
		ciStatus = "completed:" + status.Conclusion
	}

	now := time.Now().Unix()
	database.ExecContext(ctx, //nolint:errcheck
		"UPDATE swarm_tasks SET ci_status=?, ci_checked_at=? WHERE id=?",
		ciStatus, now, taskID,
	)
	swarmBroadcaster.schedule(sessionID)

	// Only notify the agent if the status has changed since last notification
	if ciStatus == lastNotified || agentID == "" {
		return
	}

	database.ExecContext(ctx, //nolint:errcheck
		"UPDATE swarm_tasks SET ci_last_notified_status=? WHERE id=?",
		ciStatus, taskID,
	)

	var brief string
	switch {
	case status.Status == "completed" && status.Conclusion == "success":
		brief = fmt.Sprintf("## CI Passed ✅\n\nCI for your PR is green. Run URL: %s\n\nProceed to the document phase.", runURL)
	case status.Status == "completed":
		summary := fetchCIFailureSummary(ctx, runURL)
		brief = fmt.Sprintf("## CI Failed ❌ (%s)\n\nRun URL: %s\n\n%s\n\nPlease investigate and fix the failures, then re-push.", status.Conclusion, runURL, summary)
	case status.Status == "in_progress":
		brief = fmt.Sprintf("## CI Running ⏳\n\nCI checks are now running. Run URL: %s\n\nStand by for results.", runURL)
	default:
		return // queued — not interesting enough to notify
	}

	writeSwarmEvent(ctx, sessionID, agentID, taskID, "ci_status_change", ciStatus)
	if err := injectToSwarmAgent(ctx, agentID, brief); err != nil {
		log.Printf("swarm/ci: inject to agent %s: %v", agentID[:8], err)
	}
}

// fetchCIStatus returns the CI run status for a PR or run URL.
// Accepts either a GitHub Actions run URL or a PR URL (from which it derives the latest run).
func fetchCIStatus(ctx context.Context, rawURL string) (ciRunStatus, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ciRunStatus{}, fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}

	// Normalise: if it's a PR URL, resolve to the latest check run
	// GitHub PR URL pattern: /owner/repo/pull/N
	// Run URL pattern: /owner/repo/actions/runs/N
	path := strings.TrimPrefix(u.Path, "/")
	parts := strings.Split(path, "/")

	var runID string
	if len(parts) >= 4 && parts[2] == "actions" && parts[3] == "runs" {
		// Already a run URL
		runID = parts[4]
		owner, repo := parts[0], parts[1]
		return ghGetRunStatus(ctx, owner, repo, runID)
	}
	if len(parts) >= 4 && parts[2] == "pull" {
		owner, repo, prNum := parts[0], parts[1], parts[3]
		return ghGetLatestRunForPR(ctx, owner, repo, prNum)
	}
	return ciRunStatus{}, fmt.Errorf("unrecognised GitHub URL format: %s", rawURL)
}

func ghGetRunStatus(ctx context.Context, owner, repo, runID string) (ciRunStatus, error) {
	apiPath := fmt.Sprintf("repos/%s/%s/actions/runs/%s", owner, repo, runID)
	out, err := exec.CommandContext(ctx, "gh", "api", apiPath).Output()
	if err != nil {
		return ciRunStatus{}, fmt.Errorf("gh api %s: %w", apiPath, err)
	}
	var result struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return ciRunStatus{}, fmt.Errorf("unmarshal run: %w", err)
	}
	if result.Conclusion == "null" {
		result.Conclusion = ""
	}
	return ciRunStatus{Status: result.Status, Conclusion: result.Conclusion}, nil
}

func ghGetLatestRunForPR(ctx context.Context, owner, repo, prNum string) (ciRunStatus, error) {
	// Get the PR's head SHA, then find runs for that SHA
	prPath := fmt.Sprintf("repos/%s/%s/pulls/%s", owner, repo, prNum)
	out, err := exec.CommandContext(ctx, "gh", "api", prPath).Output()
	if err != nil {
		return ciRunStatus{}, fmt.Errorf("gh api %s: %w", prPath, err)
	}
	var pr struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(out, &pr); err != nil {
		return ciRunStatus{}, fmt.Errorf("unmarshal PR: %w", err)
	}
	sha := pr.Head.SHA
	if sha == "" {
		return ciRunStatus{}, fmt.Errorf("empty HEAD SHA for PR %s/%s#%s", owner, repo, prNum)
	}

	runsPath := fmt.Sprintf("repos/%s/%s/actions/runs?head_sha=%s&per_page=1", owner, repo, sha)
	out, err = exec.CommandContext(ctx, "gh", "api", runsPath).Output()
	if err != nil {
		return ciRunStatus{}, fmt.Errorf("gh api %s: %w", runsPath, err)
	}
	var runs struct {
		WorkflowRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	if err := json.Unmarshal(out, &runs); err != nil {
		return ciRunStatus{}, fmt.Errorf("unmarshal runs: %w", err)
	}
	if len(runs.WorkflowRuns) == 0 {
		return ciRunStatus{Status: "queued", Conclusion: ""}, nil
	}
	r := runs.WorkflowRuns[0]
	if r.Conclusion == "null" {
		r.Conclusion = ""
	}
	return ciRunStatus{Status: r.Status, Conclusion: r.Conclusion}, nil
}

// fetchCIFailureSummary fetches failed job names and first log lines for a run URL.
// Returns a best-effort human-readable summary; silently ignores errors.
func fetchCIFailureSummary(ctx context.Context, runURL string) string {
	u, err := url.Parse(runURL)
	if err != nil {
		return ""
	}
	path := strings.TrimPrefix(u.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 5 || parts[2] != "actions" || parts[3] != "runs" {
		return ""
	}
	owner, repo, runID := parts[0], parts[1], parts[4]

	jobsPath := fmt.Sprintf("repos/%s/%s/actions/runs/%s/jobs", owner, repo, runID)
	out, err := exec.CommandContext(ctx, "gh", "api", jobsPath).Output()
	if err != nil {
		return ""
	}
	var jobs struct {
		Jobs []struct {
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
			Steps      []struct {
				Name       string `json:"name"`
				Conclusion string `json:"conclusion"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(out, &jobs); err != nil {
		return ""
	}

	var buf bytes.Buffer
	for _, j := range jobs.Jobs {
		if j.Conclusion != "failure" {
			continue
		}
		fmt.Fprintf(&buf, "**Failed job:** %s\n", j.Name)
		for _, s := range j.Steps {
			if s.Conclusion == "failure" {
				fmt.Fprintf(&buf, "  - Failed step: %s\n", s.Name)
			}
		}
	}
	return buf.String()
}
