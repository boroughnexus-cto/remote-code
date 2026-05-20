package main

// Probe valid Anthropic model identifiers for 1M-context Opus 4.x.
//
// Run with: SWARMOPS_RUN_OPUS_1M_PROBE=1 go test -run TestOpus1MModelIDs -v -timeout 5m
//
// Tries common naming patterns and inspects the result.modelUsage.contextWindow
// to confirm whether a 1M variant is supported on this account.

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestOpus1MModelIDs(t *testing.T) {
	if os.Getenv("SWARMOPS_RUN_OPUS_1M_PROBE") != "1" {
		t.Skip("set SWARMOPS_RUN_OPUS_1M_PROBE=1 to run opus 1M probe")
	}

	// Candidates to try, ordered most→least likely based on Anthropic naming conventions.
	candidates := []string{
		"claude-opus-4-6-1m",
		"claude-opus-4-6[1m]",
		"claude-opus-4-1m",
		"claude-opus-1m",
		"claude-opus-4-6-20251101-1m",
		"opus-1m",
		// Sentinel — known-good baseline to ensure the probe itself is working
		"claude-opus-4-6",
	}

	for _, model := range candidates {
		t.Run(model, func(t *testing.T) {
			result, errMsg, contextWindow, ok := probeModel(model)
			t.Logf("model=%q ok=%v contextWindow=%d", model, ok, contextWindow)
			if !ok {
				t.Logf("  error: %s", errMsg)
			} else {
				t.Logf("  result (first 100): %s", truncate(result, 100))
			}
		})
		time.Sleep(2 * time.Second) // be gentle on the API
	}
}

func probeModel(model string) (result, errMsg string, contextWindow int, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude",
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--model", model,
		"--dangerously-skip-permissions",
		"--no-session-persistence",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err.Error(), 0, false
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err.Error(), 0, false
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return "", err.Error(), 0, false
	}

	payload := `{"type":"user","message":{"role":"user","content":"Reply with OK"}}` + "\n"
	go func() {
		_, _ = io.WriteString(stdin, payload)
		stdin.Close()
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	var lastResult string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, `{"type":"result"`) {
			continue
		}
		lastResult = line
	}
	_ = cmd.Wait()

	if lastResult == "" {
		return "", "no result event", 0, false
	}

	var ev struct {
		IsError    bool   `json:"is_error"`
		Result     string `json:"result"`
		ModelUsage map[string]struct {
			ContextWindow int `json:"contextWindow"`
		} `json:"modelUsage"`
	}
	if err := json.Unmarshal([]byte(lastResult), &ev); err != nil {
		return "", "parse: " + err.Error(), 0, false
	}

	for _, u := range ev.ModelUsage {
		if u.ContextWindow > contextWindow {
			contextWindow = u.ContextWindow
		}
	}

	if ev.IsError {
		return ev.Result, ev.Result, contextWindow, false
	}
	return ev.Result, "", contextWindow, true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
