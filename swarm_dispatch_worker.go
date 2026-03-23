package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ─── Warm dispatch worker ─────────────────────────────────────────────────────
//
// Keeps a single persistent `claude --output-format stream-json` process alive
// between dispatch calls.  Prompts are written to its stdin; per-query result
// channels are used so events are never shared between requests.  After each
// successful query /clear is sent and its acknowledgement drained before the
// next call may proceed.
//
// Lifecycle: call InitDispatchWarm(appCtx) at server start; the worker is killed
// cleanly when appCtx is cancelled (server shutdown).

// streamEvent is a decoded line from the claude stream-json output.
type streamEvent struct {
	Type   string `json:"type"`
	Result string `json:"result"`
	Error  string `json:"error"`
}

// warmWorker holds a single persistent claude subprocess.
type warmWorker struct {
	mu     sync.Mutex   // serialises queries (one at a time)
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan string   // raw stdout lines from background reader
	dead   chan struct{} // closed when process exits
}

var (
	workerMu   sync.Mutex
	liveWorker *warmWorker
	appCtx     context.Context // set by InitDispatchWarm; used for subprocess lifecycle
)

// InitDispatchWarm starts the warm dispatch worker at server startup and ties
// its lifecycle to the provided context.  Call as `go InitDispatchWarm(ctx)`.
func InitDispatchWarm(ctx context.Context) {
	appCtx = ctx
	if _, err := getOrStartWorker(); err != nil {
		log.Printf("dispatch: warm worker init failed (will use cold fallback): %v", err)
	}
}

func getOrStartWorker() (*warmWorker, error) {
	workerMu.Lock()
	defer workerMu.Unlock()

	if liveWorker != nil {
		select {
		case <-liveWorker.dead:
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
	baseCtx := appCtx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	cmd := exec.CommandContext(baseCtx, "claude",
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
	// Capture stderr to logs so claude errors are visible.
	cmd.Stderr = &prefixWriter{prefix: "dispatch[claude]: "}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("dispatch worker start: %w", err)
	}

	dead := make(chan struct{})
	// lines channel is unbounded via goroutine forwarding to avoid drops.
	lines := make(chan string, 64)

	w := &warmWorker{
		cmd:   cmd,
		stdin: stdinPipe,
		lines: lines,
		dead:  dead,
	}

	go func() {
		defer close(dead)
		defer close(lines)
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			// Blocking send: backpressure rather than dropping.
			// query() always drains until result so this won't deadlock.
			select {
			case lines <- line:
			case <-dead: // shouldn't happen since we own dead, but be safe
				return
			}
		}
		cmd.Wait() //nolint:errcheck
	}()

	return w, nil
}

// query sends a prompt to the warm worker and returns the result text.
// Serialised by w.mu.  After receiving the result it sends /clear and drains
// any output from that command before releasing the lock.
func (w *warmWorker) query(ctx context.Context, prompt string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := fmt.Fprintln(w.stdin, prompt); err != nil {
		return "", fmt.Errorf("dispatch: send prompt: %w", err)
	}

	result, err := w.readUntilResult(ctx, 20*time.Second)
	if err != nil {
		return "", err
	}

	// Send /clear and drain its output so the next query starts clean.
	fmt.Fprintln(w.stdin, "/clear") //nolint:errcheck
	w.drainClear(2 * time.Second)

	return result, nil
}

// readUntilResult reads stream-json lines until a "result" or "error" event.
func (w *warmWorker) readUntilResult(ctx context.Context, timeout time.Duration) (string, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
			return "", fmt.Errorf("dispatch: timeout waiting for response")
		case <-w.dead:
			return "", fmt.Errorf("dispatch: worker process exited")
		case line, ok := <-w.lines:
			if !ok {
				return "", fmt.Errorf("dispatch: worker stdout closed")
			}
			var ev streamEvent
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue // skip non-JSON lines (startup banner, etc.)
			}
			switch ev.Type {
			case "result":
				return ev.Result, nil
			case "error":
				if ev.Error == "" {
					ev.Error = "unknown LLM error"
				}
				return "", fmt.Errorf("dispatch: LLM error: %s", ev.Error)
			}
			// assistant, tool_use, etc. — keep reading.
		}
	}
}

// drainClear consumes events produced by the /clear command.
// We keep reading until we see a "result" event (clear ack) or timeout,
// so stale events cannot pollute the next query.
func (w *warmWorker) drainClear(timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return
		case <-w.dead:
			return
		case line, ok := <-w.lines:
			if !ok {
				return
			}
			var ev streamEvent
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue
			}
			// /clear emits a result event when it completes.
			if ev.Type == "result" {
				return
			}
		}
	}
}

// warmDispatchQuery routes a prompt through the warm worker, falling back to
// a cold `claude -p` subprocess if the worker is unavailable or returns an error.
func warmDispatchQuery(ctx context.Context, prompt string) (string, error) {
	w, err := getOrStartWorker()
	if err != nil {
		log.Printf("dispatch: warm worker unavailable (%v), using cold fallback", err)
		return coldDispatchQuery(ctx, prompt)
	}

	result, err := w.query(ctx, prompt)
	if err != nil {
		// Mark worker dead so the next call spawns a fresh one.
		workerMu.Lock()
		if liveWorker == w {
			liveWorker = nil
		}
		workerMu.Unlock()
		log.Printf("dispatch: warm query failed (%v), using cold fallback", err)
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

// prefixWriter writes lines prefixed with a label to the standard logger.
type prefixWriter struct{ prefix string }

func (p *prefixWriter) Write(b []byte) (int, error) {
	log.Printf("%s%s", p.prefix, strings.TrimRight(string(b), "\n"))
	return len(b), nil
}
