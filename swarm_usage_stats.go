package main

// swarm_usage_stats.go — polls the tkn-usage MCP server for Claude quota and
// GitHub Copilot usage, caches the result in memory, and serves it at
// GET /api/swarm/usage.  Refresh interval: 5 minutes.
//
// MCP server URL is read from USAGE_MCP_URL env var (default: Tailscale URL).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ─── Types ────────────────────────────────────────────────────────────────────

type usageQuotaEntry struct {
	PercentUsed int    `json:"percent_used"`
	ResetsAt    string `json:"resets_at"` // RFC3339
}

// SwarmUsageStats is the payload served at /api/swarm/usage and consumed by
// the TUI.
type SwarmUsageStats struct {
	Claude struct {
		Session usageQuotaEntry `json:"session"`
		Weekly  usageQuotaEntry `json:"weekly"`
	} `json:"claude"`
	Copilot struct {
		PremiumPct float64 `json:"premium_pct"`
		ResetsAt   string  `json:"resets_at"` // first day of next month
		Plan       string  `json:"plan"`
	} `json:"copilot"`
	FetchedAt time.Time `json:"fetched_at"`
	Error     string    `json:"error,omitempty"`
}

var (
	globalUsageStats SwarmUsageStats
	globalUsageMu    sync.RWMutex
	usageMCPURL      = envOrDefaultUsage("USAGE_MCP_URL", "https://mcp-usage.gate-hexatonic.ts.net/mcp")
)

func envOrDefaultUsage(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ─── Background poller ────────────────────────────────────────────────────────

func startUsagePoller() {
	go func() {
		refreshUsageStats()
		tick := time.NewTicker(5 * time.Minute)
		for range tick.C {
			refreshUsageStats()
		}
	}()
}

func refreshUsageStats() {
	var stats SwarmUsageStats
	stats.FetchedAt = time.Now()

	// ── Claude quota ──────────────────────────────────────────────────────────
	type claudeQuotaResp struct {
		CurrentSession  usageQuotaEntry `json:"current_session"`
		WeeklyAllModels usageQuotaEntry `json:"weekly_all_models"`
		Status          string          `json:"status"`
	}
	claudeRaw, err := callUsageMCPTool("scrape_claude_quota", nil)
	if err != nil {
		stats.Error = "claude: " + err.Error()
		log.Printf("swarm/usage: scrape_claude_quota: %v", err)
	} else {
		var q claudeQuotaResp
		if err := json.Unmarshal(claudeRaw, &q); err != nil {
			stats.Error = "claude parse: " + err.Error()
			log.Printf("swarm/usage: parse claude quota: %v", err)
		} else {
			stats.Claude.Session = q.CurrentSession
			stats.Claude.Weekly = q.WeeklyAllModels
		}
	}

	// ── Copilot usage ─────────────────────────────────────────────────────────
	type copilotResp struct {
		PremiumRequestsPercentage float64 `json:"premium_requests_percentage"`
		Plan                      string  `json:"plan"`
		Status                    string  `json:"status"`
	}
	copilotRaw, err := callUsageMCPTool("scrape_github_copilot_usage", nil)
	if err != nil {
		if stats.Error != "" {
			stats.Error += "; "
		}
		stats.Error += "copilot: " + err.Error()
		log.Printf("swarm/usage: scrape_github_copilot_usage: %v", err)
	} else {
		var c copilotResp
		if err := json.Unmarshal(copilotRaw, &c); err != nil {
			log.Printf("swarm/usage: parse copilot: %v", err)
		} else {
			stats.Copilot.PremiumPct = c.PremiumRequestsPercentage
			stats.Copilot.Plan = c.Plan
			// Copilot resets at the start of the next calendar month.
			now := time.Now().UTC()
			nextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			stats.Copilot.ResetsAt = nextMonth.Format(time.RFC3339)
		}
	}

	globalUsageMu.Lock()
	globalUsageStats = stats
	globalUsageMu.Unlock()
}

// ─── MCP streamable-HTTP client ───────────────────────────────────────────────

var usageHTTPClient = &http.Client{Timeout: 30 * time.Second}

// callUsageMCPTool calls a tool on the usage MCP server and returns the raw
// JSON text from the first content item.  It handles both direct JSON and
// text/event-stream SSE responses.
var usageMCPCallID atomic.Int64

func callUsageMCPTool(toolName string, args map[string]interface{}) (json.RawMessage, error) {
	if args == nil {
		args = map[string]interface{}{}
	}
	body, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      usageMCPCallID.Add(1),
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      toolName,
			"arguments": args,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("callUsageMCPTool marshal: %w", err)
	}

	req, err := http.NewRequest("POST", usageMCPURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := usageHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	rawBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	rawBody := string(rawBytes)

	// If the server responded with SSE, extract the first data: line.
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		for _, line := range strings.Split(rawBody, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data: ") {
				rawBody = strings.TrimPrefix(line, "data: ")
				break
			}
		}
	}

	var rpcResp struct {
		Result *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(rawBody), &rpcResp); err != nil {
		return nil, err
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("%s", rpcResp.Error.Message)
	}
	if rpcResp.Result == nil || len(rpcResp.Result.Content) == 0 {
		return nil, fmt.Errorf("empty result")
	}
	return json.RawMessage(rpcResp.Result.Content[0].Text), nil
}

// ─── HTTP handler ─────────────────────────────────────────────────────────────

func handleUsageStatsAPI(w http.ResponseWriter, r *http.Request) {
	globalUsageMu.RLock()
	stats := globalUsageStats
	globalUsageMu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats) //nolint:errcheck
}
