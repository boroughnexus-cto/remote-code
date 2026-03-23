package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// ─── Dispatch worker ──────────────────────────────────────────────────────────
//
// warmDispatchQuery sends a prompt to a fresh `claude -p` subprocess.
// The name "warm" is retained for API compatibility; the persistent-session
// approach was abandoned because the claude CLI enters --print mode when stdin
// is a pipe (not a TTY) and exits after the first response — a PTY shim would
// be needed to keep it alive across multiple queries, which is out of scope.
//
// --strict-mcp-config with an empty server list prevents claude from loading
// hundreds of MCP tool definitions, reducing cold-start time to ~3s.
//
// Lifecycle: call InitDispatchWarm(appCtx) at server start to register the
// app context; actual subprocess is spawned per call.

var appCtx context.Context

// InitDispatchWarm registers the server context for dispatch subprocesses.
func InitDispatchWarm(ctx context.Context) {
	appCtx = ctx
	log.Printf("dispatch: query worker enabled (--strict-mcp-config, haiku)")
}

// warmDispatchQuery routes a prompt through a claude subprocess.
func warmDispatchQuery(ctx context.Context, prompt string) (string, error) {
	return coldDispatchQuery(ctx, prompt)
}

// coldDispatchQuery spawns a fresh `claude -p` subprocess.
func coldDispatchQuery(ctx context.Context, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt,
		"--model", "claude-haiku-4-5-20251001",
		"--dangerously-skip-permissions",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
	)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("dispatch cold: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// prefixWriter writes lines prefixed with a label to the standard logger.
type prefixWriter struct{ prefix string }

func (p *prefixWriter) Write(b []byte) (int, error) {
	log.Printf("%s%s", p.prefix, strings.TrimRight(string(b), "\n"))
	return len(b), nil
}
