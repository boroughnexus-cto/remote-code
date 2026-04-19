package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ─── Pool types ──────────────────────────────────────────────────────────────

type slotState int

const (
	slotIdle slotState = iota
	slotBusy
	slotStarting
	slotDead
)

func (s slotState) String() string {
	switch s {
	case slotIdle:
		return "idle"
	case slotBusy:
		return "busy"
	case slotStarting:
		return "starting"
	case slotDead:
		return "dead"
	}
	return "unknown"
}

// poolEvent is an NDJSON event from the Claude CLI stream-json output.
type poolEvent struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Message   json.RawMessage `json:"message,omitempty"`
	Result    string          `json:"result,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	CostUSD   float64         `json:"total_cost_usd,omitempty"`
	Event     json.RawMessage `json:"event,omitempty"`
	UUID      string          `json:"uuid,omitempty"`

	// Fields from result events
	DurationMS    int            `json:"duration_ms,omitempty"`
	DurationAPIMS int            `json:"duration_api_ms,omitempty"`
	NumTurns      int            `json:"num_turns,omitempty"`
	StopReason    string         `json:"stop_reason,omitempty"`
	Usage         *poolUsage     `json:"usage,omitempty"`
}

type poolUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// poolAssistantMessage is the structure inside a poolEvent.Message for assistant events.
type poolAssistantMessage struct {
	Content []poolContentBlock `json:"content"`
}

type poolContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// poolStreamDelta extracts text deltas from stream_event events.
type poolStreamEvent struct {
	Type  string          `json:"type"`
	Delta *poolDelta      `json:"delta,omitempty"`
}

type poolDelta struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ─── PoolSlot ────────────────────────────────────────────────────────────────

// PoolSlot represents a single warm Claude CLI session.
type PoolSlot struct {
	ID         string
	Model      string
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     *bufio.Reader
	stderrDone chan struct{}

	mu             sync.Mutex
	state          slotState
	startedAt      time.Time // when state last transitioned to slotStarting
	generation     int64
	lastUsed       time.Time
	errorCount     int
	totalCost      float64
	totalRequests  int64
	rateLimitUntil time.Time
}

// sendQuery writes a user message to the Claude CLI's stdin.
func (s *PoolSlot) sendQuery(content interface{}) error {
	msg := map[string]interface{}{
		"type": "user",
		"message": map[string]interface{}{
			"role":    "user",
			"content": content,
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal query: %w", err)
	}
	_, err = fmt.Fprintf(s.stdin, "%s\n", data)
	return err
}

// readEvent reads the next valid JSON event from stdout, skipping blank/non-JSON lines.
func (s *PoolSlot) readEvent() (poolEvent, error) {
	for {
		line, err := s.stdout.ReadBytes('\n')
		if err != nil {
			return poolEvent{}, err
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" || trimmed[0] != '{' {
			continue
		}
		var ev poolEvent
		if err := json.Unmarshal([]byte(trimmed), &ev); err != nil {
			log.Printf("pool: slot %s json parse error: %v", s.ID, err)
			continue
		}
		return ev, nil
	}
}

// alive checks if the underlying process is still running.
func (s *PoolSlot) alive() bool {
	if s.cmd == nil || s.cmd.Process == nil {
		return false
	}
	return s.cmd.Process.Signal(syscall.Signal(0)) == nil
}

// kill terminates the underlying process.
func (s *PoolSlot) kill() {
	if s.stdin != nil {
		s.stdin.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	}
	// Wait for stderr drain goroutine
	if s.stderrDone != nil {
		select {
		case <-s.stderrDone:
		case <-time.After(2 * time.Second):
		}
	}
}

// ─── PoolManager ─────────────────────────────────────────────────────────────

// PoolConfig holds pool configuration resolved from ConfigService.
type PoolConfig struct {
	Models         []string
	SlotsPerModel  int
	RequestTimeout time.Duration
	APIKey         string
	MaxConsecErrs  int
	IdleRecycleAge time.Duration
	BackoffBase    time.Duration
	BackoffMax     time.Duration
}

// DefaultPoolConfig returns sensible defaults.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		Models:         []string{"claude-haiku-4-5", "claude-sonnet-4-6", "claude-opus-4-6"},
		SlotsPerModel:  2,
		RequestTimeout: 5 * time.Minute,
		MaxConsecErrs:  3,
		IdleRecycleAge: 30 * time.Minute,
		BackoffBase:    1 * time.Second,
		BackoffMax:     30 * time.Second,
	}
}

// PoolManager manages a pool of warm Claude CLI sessions.
type PoolManager struct {
	slots     map[string][]*PoolSlot   // model → all slots
	available map[string]chan *PoolSlot // model → idle slot channel
	config    PoolConfig
	db        *sql.DB
	totalCost atomic.Int64 // stored as microdollars (USD * 1e6) for atomic ops

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

var globalPool *PoolManager
var poolClaudeMDOnce sync.Once

// NewPoolManager creates and starts a warm pool.
func NewPoolManager(ctx context.Context, db *sql.DB, config PoolConfig) *PoolManager {
	pctx, cancel := context.WithCancel(ctx)
	pm := &PoolManager{
		slots:     make(map[string][]*PoolSlot),
		available: make(map[string]chan *PoolSlot),
		config:    config,
		db:        db,
		ctx:       pctx,
		cancel:    cancel,
	}

	for _, model := range config.Models {
		pm.available[model] = make(chan *PoolSlot, config.SlotsPerModel)
		pm.slots[model] = make([]*PoolSlot, 0, config.SlotsPerModel)

		for i := 0; i < config.SlotsPerModel; i++ {
			slotID := fmt.Sprintf("pool-%s-%d", modelShortName(model), i)
			slot, err := pm.spawnSlot(model, slotID)
			if err != nil {
				log.Printf("pool: failed to spawn %s: %v", slotID, err)
				continue
			}
			pm.slots[model] = append(pm.slots[model], slot)
		}
	}

	// Start health monitor
	pm.wg.Add(1)
	go pm.healthMonitor()

	log.Printf("pool: started with %d models, %d slots/model", len(config.Models), config.SlotsPerModel)
	return pm
}

// spawnSlot creates a new warm Claude CLI process.
func (pm *PoolManager) spawnSlot(model, slotID string) (*PoolSlot, error) {
	// Ensure clean working directory
	workDir := filepath.Join(os.TempDir(), "swarmops-pool", slotID)
	os.MkdirAll(workDir, 0755)

	// Write shared CLAUDE.md for all pool workers (once per process lifetime)
	poolClaudeMDOnce.Do(func() {
		poolDir := filepath.Join(os.TempDir(), "swarmops-pool")
		claudeMD := filepath.Join(poolDir, "CLAUDE.md")
		content := "# Pool Worker Rules\n\n- Always use ToolSearch to verify tool availability before claiming any MCP tool is unavailable or missing. Do not eyeball the deferred tools list.\n"
		tmp := claudeMD + ".tmp"
		if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
			log.Printf("pool: failed to write shared CLAUDE.md: %v", err)
		} else if err := os.Rename(tmp, claudeMD); err != nil {
			log.Printf("pool: failed to rename shared CLAUDE.md: %v", err)
			os.Remove(tmp)
		} else {
			log.Printf("pool: wrote shared CLAUDE.md to %s", poolDir)
		}
	})

	// Load MCP config from file for models that can handle it.
	// Haiku's context is too small for 1200+ tool definitions — skip MCP for it.
	mcpConfig := `{"mcpServers":{}}`
	if !strings.Contains(model, "haiku") {
		mcpConfigPath := filepath.Join(os.Getenv("HOME"), ".swarmops", "mcp-config.json")
		if data, err := os.ReadFile(mcpConfigPath); err == nil {
			mcpConfig = string(data)
			log.Printf("pool: loaded MCP config from %s (%d bytes)", mcpConfigPath, len(data))
		}
	}

	cmd := exec.CommandContext(pm.ctx, "claude",
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--model", model,
		"--dangerously-skip-permissions",
		"--no-session-persistence",
		"--strict-mcp-config",
		"--mcp-config", mcpConfig,
	)
	cmd.Dir = workDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 64*1024), 64*1024)
		for scanner.Scan() {
			log.Printf("pool: %s [stderr] %s", slotID, scanner.Text())
		}
	}()

	slot := &PoolSlot{
		ID:         slotID,
		Model:      model,
		cmd:        cmd,
		stdin:      stdin,
		stdout:     bufio.NewReaderSize(stdout, 256*1024),
		stderrDone: stderrDone,
		state:      slotStarting,
		startedAt:  time.Now(),
		lastUsed:   time.Now(),
	}

	// Self-test: send a trivial query and wait for result
	if err := pm.selfTest(slot); err != nil {
		slot.kill()
		return nil, fmt.Errorf("self-test: %w", err)
	}

	slot.mu.Lock()
	slot.state = slotIdle
	slot.mu.Unlock()

	// Push to available channel
	pm.available[model] <- slot

	log.Printf("pool: spawned %s (PID %d, model %s)", slotID, cmd.Process.Pid, model)
	return slot, nil
}

// selfTest sends a minimal query to validate the slot works.
func (pm *PoolManager) selfTest(slot *PoolSlot) error {
	if err := slot.sendQuery("ping"); err != nil {
		return fmt.Errorf("send: %w", err)
	}

	deadline := time.After(15 * time.Second)
	for {
		evCh := make(chan poolEvent, 1)
		errCh := make(chan error, 1)
		go func() {
			ev, err := slot.readEvent()
			if err != nil {
				errCh <- err
			} else {
				evCh <- ev
			}
		}()

		select {
		case ev := <-evCh:
			if ev.Type == "result" {
				if ev.IsError {
					return fmt.Errorf("result error: %s", ev.Result)
				}
				return nil
			}
			// Keep reading (init, assistant, etc.)
		case err := <-errCh:
			return fmt.Errorf("read: %w", err)
		case <-deadline:
			return fmt.Errorf("timeout after 15s")
		}
	}
}

// Acquire claims an available slot for the given model. Blocks until one is free or ctx is done.
func (pm *PoolManager) Acquire(ctx context.Context, model string) (*PoolSlot, error) {
	ch, ok := pm.available[model]
	if !ok {
		return nil, fmt.Errorf("unknown model: %s", model)
	}

	select {
	case slot := <-ch:
		slot.mu.Lock()
		// Skip rate-limited slots — put back and wait
		if time.Now().Before(slot.rateLimitUntil) {
			slot.mu.Unlock()
			pm.available[model] <- slot
			// Brief sleep to avoid tight loop
			select {
			case <-time.After(time.Until(slot.rateLimitUntil)):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return pm.Acquire(ctx, model)
		}
		slot.state = slotBusy
		slot.mu.Unlock()
		return slot, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Release returns a slot to the available pool. If the slot is dead, it triggers recycling.
func (pm *PoolManager) Release(slot *PoolSlot) {
	slot.mu.Lock()
	if slot.state == slotDead {
		slot.mu.Unlock()
		go pm.recycleSlot(slot)
		return
	}
	slot.state = slotIdle
	slot.lastUsed = time.Now()
	slot.mu.Unlock()

	select {
	case pm.available[slot.Model] <- slot:
	default:
		// Channel full (shouldn't happen) — kill slot
		slot.kill()
	}
}

// recycleSlot kills a dead slot and spawns a replacement.
func (pm *PoolManager) recycleSlot(slot *PoolSlot) {
	model := slot.Model
	slotID := slot.ID
	slot.kill()

	log.Printf("pool: recycling %s", slotID)
	newSlot, err := pm.spawnSlot(model, slotID)
	if err != nil {
		log.Printf("pool: recycle failed for %s: %v", slotID, err)
		// Reset to slotDead so the health monitor retries on the next tick.
		slot.mu.Lock()
		slot.state = slotDead
		slot.mu.Unlock()
		return
	}

	// Replace in slots list
	pm.replaceSlot(model, slotID, newSlot)
}

func (pm *PoolManager) replaceSlot(model, slotID string, newSlot *PoolSlot) {
	for i, s := range pm.slots[model] {
		if s.ID == slotID {
			pm.slots[model][i] = newSlot
			return
		}
	}
	// Not found — append (shouldn't happen)
	pm.slots[model] = append(pm.slots[model], newSlot)
}

// healthMonitor runs every 10s checking slot health.
func (pm *PoolManager) healthMonitor() {
	defer pm.wg.Done()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-pm.ctx.Done():
			return
		case <-ticker.C:
			pm.checkHealth()
		}
	}
}

func (pm *PoolManager) checkHealth() {
	for model, slots := range pm.slots {
		for _, slot := range slots {
			slot.mu.Lock()
			state := slot.state
			idle := time.Since(slot.lastUsed)
			alive := slot.alive()
			slot.mu.Unlock()

			if state == slotIdle && !alive {
				log.Printf("pool: %s process dead, recycling", slot.ID)
				slot.mu.Lock()
				slot.state = slotDead
				slot.mu.Unlock()
				// Drain from available channel
				pm.drainSlotFromAvailable(model, slot)
				go pm.recycleSlot(slot)
			}

			if state == slotIdle && idle > pm.config.IdleRecycleAge {
				log.Printf("pool: %s idle for %v, recycling to free memory", slot.ID, idle)
				slot.mu.Lock()
				slot.state = slotDead
				slot.mu.Unlock()
				pm.drainSlotFromAvailable(model, slot)
				go pm.recycleSlot(slot)
			}

			// Retry previously-failed recycles — slot stayed dead after spawn error.
			if state == slotDead {
				slot.mu.Lock()
				if slot.state == slotDead { // re-check under lock to avoid double-spawn
					slot.state = slotStarting
					slot.startedAt = time.Now()
					slot.mu.Unlock()
					log.Printf("pool: %s dead slot detected, retrying recycle", slot.ID)
					go pm.recycleSlot(slot)
				} else {
					slot.mu.Unlock()
				}
			}

			// Unstick slots that are in slotStarting but the process is dead (SWM-57).
			// This happens when a recycle goroutine hangs or the spawned process dies
			// before self-test completes. After 2× the self-test deadline, reset to dead.
			if state == slotStarting && !alive {
				slot.mu.Lock()
				startedAt := slot.startedAt
				slot.mu.Unlock()
				if !startedAt.IsZero() && time.Since(startedAt) > 2*time.Minute {
					slot.mu.Lock()
					if slot.state == slotStarting {
						slot.state = slotDead
						log.Printf("pool: %s stuck in starting for >2m with dead process, resetting to dead", slot.ID)
					}
					slot.mu.Unlock()
				}
			}

			// Clear expired rate limits
			slot.mu.Lock()
			if !slot.rateLimitUntil.IsZero() && time.Now().After(slot.rateLimitUntil) {
				slot.rateLimitUntil = time.Time{}
				log.Printf("pool: %s rate limit cleared", slot.ID)
			}
			slot.mu.Unlock()
		}
	}
}

// drainSlotFromAvailable removes a specific slot from the available channel.
func (pm *PoolManager) drainSlotFromAvailable(model string, target *PoolSlot) {
	ch := pm.available[model]
	// Non-blocking drain: pull all slots, put back everything except target
	var keep []*PoolSlot
	for {
		select {
		case s := <-ch:
			if s.ID != target.ID {
				keep = append(keep, s)
			}
		default:
			goto done
		}
	}
done:
	for _, s := range keep {
		ch <- s
	}
}

// Shutdown gracefully shuts down all pool slots.
func (pm *PoolManager) Shutdown() {
	log.Printf("pool: shutting down")
	pm.cancel()
	for _, slots := range pm.slots {
		for _, slot := range slots {
			slot.kill()
		}
	}
	pm.wg.Wait()
	log.Printf("pool: shutdown complete")
}

// Status returns the current pool state for the status API.
func (pm *PoolManager) Status() map[string]interface{} {
	models := make(map[string]interface{})
	for model, slots := range pm.slots {
		slotInfos := make([]map[string]interface{}, 0, len(slots))
		avail := 0
		totalReqs := int64(0)
		totalCost := 0.0
		for _, slot := range slots {
			slot.mu.Lock()
			info := map[string]interface{}{
				"id":          slot.ID,
				"state":       slot.state.String(),
				"generation":  slot.generation,
				"error_count": slot.errorCount,
				"cost_usd":    slot.totalCost,
				"requests":    slot.totalRequests,
				"alive":       slot.alive(),
			}
			if slot.state == slotIdle {
				avail++
			}
			totalReqs += slot.totalRequests
			totalCost += slot.totalCost
			slot.mu.Unlock()
			slotInfos = append(slotInfos, info)
		}
		models[model] = map[string]interface{}{
			"slots":          slotInfos,
			"available":      avail,
			"total_requests": totalReqs,
			"total_cost_usd": totalCost,
		}
	}

	return map[string]interface{}{
		"enabled":        true,
		"models":         models,
		"total_cost_usd": float64(pm.totalCost.Load()) / 1e6,
	}
}

// handleRateLimit processes a rate_limit_event or api_retry event.
func (pm *PoolManager) handleRateLimit(slot *PoolSlot, ev poolEvent) {
	slot.mu.Lock()
	defer slot.mu.Unlock()

	slot.errorCount++

	// Try to extract retry delay from the event
	delay := pm.config.BackoffBase * time.Duration(1<<min(slot.errorCount, 5))
	if delay > pm.config.BackoffMax {
		delay = pm.config.BackoffMax
	}

	slot.rateLimitUntil = time.Now().Add(delay)
	log.Printf("pool: %s rate limited for %v (errors: %d)", slot.ID, delay, slot.errorCount)
}

// classifyResultError determines what to do after an error result.
func classifyResultError(ev poolEvent) string {
	if !ev.IsError {
		return ""
	}
	r := strings.ToLower(ev.Result)
	switch {
	case strings.Contains(r, "not logged in"), strings.Contains(r, "authentication"):
		return "disable" // systemic — don't respawn
	case strings.Contains(r, "billing"), strings.Contains(r, "quota"):
		return "disable"
	case strings.Contains(r, "rate limit"):
		return "retry"
	case strings.Contains(r, "prompt is too long"):
		return "ignore" // user's fault
	default:
		return "recycle"
	}
}

// logRequest records a pool request in the database.
func (pm *PoolManager) logRequest(reqID, model, slotID, promptPreview, status string, tokensIn, tokensOut, latencyMS, ttftMS int, costUSD float64, errType, errDetail string) {
	if pm.db == nil {
		return
	}
	var completedAt *string
	if status == "complete" || status == "error" || status == "cancelled" {
		now := time.Now().Format(time.RFC3339)
		completedAt = &now
	}
	_, err := pm.db.Exec(`INSERT INTO pool_requests
		(request_id, model, slot_id, prompt_preview, tokens_in, tokens_out, cost_usd, latency_ms, ttft_ms, status, error_type, error_detail, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		reqID, model, slotID, truncateStr(promptPreview, 200), tokensIn, tokensOut, costUSD, latencyMS, ttftMS, status, nilIfEmpty(errType), nilIfEmpty(errDetail), completedAt)
	if err != nil {
		log.Printf("pool: log request error: %v", err)
	}
}

// ─── Startup ─────────────────────────────────────────────────────────────────

// initPool reads pool config and starts the pool manager if enabled.
func initPool(ctx context.Context) {
	if globalConfigService == nil {
		return
	}
	enabled := globalConfigService.Get("pool.enabled").Value
	if enabled != "true" {
		log.Printf("pool: disabled (set pool.enabled=true to enable)")
		return
	}

	config := DefaultPoolConfig()

	if v := globalConfigService.Get("pool.models").Value; v != "" {
		config.Models = strings.Split(v, ",")
		for i := range config.Models {
			config.Models[i] = strings.TrimSpace(config.Models[i])
		}
	}

	if v := globalConfigService.Get("pool.slots_per_model").Value; v != "" {
		if n, err := parseInt(v); err == nil {
			config.SlotsPerModel = n
		}
	}

	if v := globalConfigService.Get("pool.request_timeout_s").Value; v != "" {
		if n, err := parseInt(v); err == nil {
			config.RequestTimeout = time.Duration(n) * time.Second
		}
	}

	config.APIKey = globalConfigService.Get("pool.api_key").Value

	if v := globalConfigService.Get("pool.max_consec_errors").Value; v != "" {
		if n, err := parseInt(v); err == nil {
			config.MaxConsecErrs = n
		}
	}

	if v := globalConfigService.Get("pool.idle_recycle_min").Value; v != "" {
		if n, err := parseInt(v); err == nil {
			config.IdleRecycleAge = time.Duration(n) * time.Minute
		}
	}

	globalPool = NewPoolManager(ctx, database, config)
}

func parseInt(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number: %s", s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func modelShortName(model string) string {
	switch {
	case strings.Contains(model, "haiku"):
		return "haiku"
	case strings.Contains(model, "sonnet"):
		return "sonnet"
	case strings.Contains(model, "opus"):
		return "opus"
	}
	return model
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
