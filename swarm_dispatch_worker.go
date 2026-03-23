package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ─── Warm dispatch worker ─────────────────────────────────────────────────────
//
// Keeps a single persistent `claude --output-format stream-json` process alive
// between dispatch calls.  Prompts are written to its stdin; JSON-streamed
// events are read from stdout.  After each successful query we send /clear so
// the conversation context is reset and doesn't accumulate across calls.
//
// If the worker process dies (context exhaustion, crash, etc.) it is restarted
// automatically.  If the worker is unavailable a fresh `claude -p` subprocess
// is used as a cold fallback.

type warmWorker struct {
	mu     sync.Mutex      // serialises queries (one at a time)
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	events chan workerEvent // produced by the background reader goroutine
	dead   chan struct{}    // closed when the process exits
}

type workerEvent struct {
	raw map[string]interface{}
	err error
}

var (
	workerMu sync.Mutex
	liveWorker *warmWorker
)

// InitDispatchWarm starts the warm dispatch worker at server startup.
// Call with `go`.
func InitDispatchWarm() {
	if _, err := getOrStartWorker(); err != nil {
		// Non-fatal: cold fallback will be used until worker is available.
		_ = err
	}
}

func getOrStartWorker() (*warmWorker, error) {
	workerMu.Lock()
	defer workerMu.Unlock()

	if liveWorker != nil {
		select {
		case <-liveWorker.dead:
			// Process has exited — fall through to restart.
			liveWorker = nil
		default:
			return liveWorker, nil
		}
	}

	w, err := spawnWorker()
	if err != nil {
		return nil, err
	}
	liveWorker = w
	return w, nil
}

func spawnWorker() (*warmWorker, error) {
	cmd := exec.Command("claude",
		"--model", "claude-haiku-4-5-20251001",
		"--output-format", "stream-json",
		"--dangerously-skip-permissions",
	)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("dispatch worker stdin: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("dispatch worker stdout: %w", err)
	}
	// Discard stderr so it doesn't block the process.
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("dispatch worker start: %w", err)
	}

	dead := make(chan struct{})
	events := make(chan workerEvent, 32)

	w := &warmWorker{
		cmd:    cmd,
		stdin:  stdinPipe,
		events: events,
		dead:   dead,
	}

	// Background reader: parses stream-json lines and forwards to events channel.
	go func() {
		defer close(dead)
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var ev map[string]interface{}
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue // skip non-JSON lines (e.g. startup banner)
			}
			select {
			case events <- workerEvent{raw: ev}:
			default:
				// Channel full — discard (shouldn't happen with buffer of 32).
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case events <- workerEvent{err: err}:
			default:
			}
		}
		cmd.Wait() //nolint:errcheck
	}()

	return w, nil
}

// query sends a prompt to the warm worker and returns the result text.
// It holds w.mu for the duration, ensuring queries are serialised.
// After receiving the result it sends /clear to reset conversation context.
func (w *warmWorker) query(ctx context.Context, prompt string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Write prompt; claude reads one logical turn per newline.
	if _, err := fmt.Fprintln(w.stdin, prompt); err != nil {
		return "", fmt.Errorf("dispatch: send prompt: %w", err)
	}

	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timeout.C:
			return "", fmt.Errorf("dispatch: timeout waiting for response")
		case <-w.dead:
			return "", fmt.Errorf("dispatch: worker process exited")
		case ev := <-w.events:
			if ev.err != nil {
				return "", fmt.Errorf("dispatch: reader error: %w", ev.err)
			}
			switch ev.raw["type"] {
			case "result":
				result, _ := ev.raw["result"].(string)
				// Reset context so the next call starts clean.
				fmt.Fprintln(w.stdin, "/clear") //nolint:errcheck
				return result, nil
			case "error":
				msg, _ := ev.raw["error"].(string)
				if msg == "" {
					msg = fmt.Sprintf("%v", ev.raw["error"])
				}
				return "", fmt.Errorf("dispatch: LLM error: %s", msg)
			}
			// Other event types (assistant, tool_use, etc.) — keep reading.
		}
	}
}

// warmDispatchQuery returns the LLM's text response for prompt.
// Uses the warm worker when available; falls back to a cold `claude -p` call.
func warmDispatchQuery(ctx context.Context, prompt string) (string, error) {
	w, err := getOrStartWorker()
	if err != nil {
		return coldDispatchQuery(ctx, prompt)
	}

	result, err := w.query(ctx, prompt)
	if err != nil {
		// Mark worker as dead so the next call restarts it.
		workerMu.Lock()
		if liveWorker == w {
			liveWorker = nil
		}
		workerMu.Unlock()
		// Still try cold path rather than failing.
		return coldDispatchQuery(ctx, prompt)
	}
	return result, nil
}

// coldDispatchQuery spawns a fresh `claude -p` subprocess as a fallback.
func coldDispatchQuery(ctx context.Context, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt,
		"--model", "claude-haiku-4-5-20251001",
		"--dangerously-skip-permissions",
	)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("dispatch cold: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
