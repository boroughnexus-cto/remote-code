package main

// Empirical probe for Anthropic prompt caching via Claude Code's stream-json.
//
// Run with: go test -run TestCacheControlPassthrough -v -timeout 5m
//
// This is a *probe*, not a regression test — it depends on a working `claude`
// binary in PATH and a valid Anthropic credential. Skipped unless
// SWARMOPS_RUN_CACHE_PROBE=1 is set.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCacheControlPassthrough(t *testing.T) {
	if os.Getenv("SWARMOPS_RUN_CACHE_PROBE") != "1" {
		t.Skip("set SWARMOPS_RUN_CACHE_PROBE=1 to run cache probe")
	}

	// ~1500-token payload (Sonnet's cache minimum is 1024 tokens)
	long := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 200)
	payload := map[string]interface{}{
		"type": "user",
		"message": map[string]interface{}{
			"role": "user",
			"content": []map[string]interface{}{
				{
					"type":          "text",
					"text":          long,
					"cache_control": map[string]string{"type": "ephemeral", "ttl": "1h"},
				},
				{
					"type": "text",
					"text": "Reply with just OK.",
				},
			},
		},
	}
	inputBytes, _ := json.Marshal(payload)

	for i := 1; i <= 2; i++ {
		t.Logf("=== run %d ===", i)
		result := runOneClaudeQuery(t, string(inputBytes)+"\n")
		t.Logf("result event raw: %s", result)

		var ev map[string]interface{}
		if err := json.Unmarshal([]byte(result), &ev); err != nil {
			t.Fatalf("parse result: %v", err)
		}
		usage, _ := ev["usage"].(map[string]interface{})
		if usage == nil {
			t.Fatalf("no usage in result")
		}

		// Anthropic API includes these fields when caching is in play.
		// If Claude Code's stream-json strips them, this probe surfaces it.
		cacheCreate, hasCreate := usage["cache_creation_input_tokens"]
		cacheRead, hasRead := usage["cache_read_input_tokens"]
		t.Logf("run %d: input=%v output=%v cache_create=%v(present=%v) cache_read=%v(present=%v)",
			i, usage["input_tokens"], usage["output_tokens"], cacheCreate, hasCreate, cacheRead, hasRead)

		if !hasCreate && !hasRead {
			t.Errorf("run %d: NO cache fields in usage — stream-json may be stripping them", i)
		}
		if i == 2 && hasRead {
			if rd, ok := cacheRead.(float64); ok && rd > 0 {
				t.Logf("✓ run 2 hit cache: %v tokens read from cache", rd)
			}
		}

		time.Sleep(3 * time.Second) // gap between runs
	}
}

func runOneClaudeQuery(t *testing.T, input string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude",
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--model", "claude-sonnet-4-6",
		"--dangerously-skip-permissions",
		"--no-session-persistence",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	go func() {
		fmt.Fprint(stdin, input)
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
		t.Fatalf("no result event in claude output")
	}
	return lastResult
}
