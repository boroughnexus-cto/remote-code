package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

// configScope indicates where a config value comes from / should be stored.
type configScope int

const (
	scopeEnv     configScope = iota // read-only env var baseline
	scopeDB                         // persisted in system_config table
	scopeRuntime                    // in-memory override, resets on restart
	scopeDefault                    // hardcoded registry default (no env var, no DB row)
)

// configEntry holds a resolved config value with provenance.
type configEntry struct {
	Key         string      `json:"key"`
	Value       string      `json:"value"`
	Source      configScope `json:"source"`
	DangerLevel int         `json:"danger_level"`
	Restartable bool        `json:"restartable"`
}

// configMeta describes a known config key.
type configMeta struct {
	Default     string
	Description string
	DangerLevel int
	Restartable bool
	EnvVar      string            // e.g. "SWARM_MAX_AGENTS" — read as baseline if set
	Validate    func(string) error // optional value validator; nil = accept anything
}

// ─── Validator helpers ────────────────────────────────────────────────────────

func validatePositiveInt(min, max int) func(string) error {
	return func(v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("must be an integer")
		}
		if n < min {
			return fmt.Errorf("must be >= %d", min)
		}
		if max > 0 && n > max {
			return fmt.Errorf("must be <= %d", max)
		}
		return nil
	}
}

func validatePositiveFloat(min float64) func(string) error {
	return func(v string) error {
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return fmt.Errorf("must be a number")
		}
		if f < min {
			return fmt.Errorf("must be >= %g", min)
		}
		return nil
	}
}

func validateBoolValue() func(string) error {
	return func(v string) error {
		if _, err := strconv.ParseBool(strings.TrimSpace(v)); err != nil {
			return fmt.Errorf("must be true or false")
		}
		return nil
	}
}

func validateEnum(vals ...string) func(string) error {
	return func(v string) error {
		t := strings.TrimSpace(v)
		for _, valid := range vals {
			if t == valid {
				return nil
			}
		}
		return fmt.Errorf("must be one of: %s", strings.Join(vals, ", "))
	}
}

// configChange is one record from system_config_history.
type configChange struct {
	ID        int64  `json:"id"`
	Key       string `json:"key"`
	OldValue  string `json:"old_value"`
	NewValue  string `json:"new_value"`
	ChangedAt int64  `json:"changed_at"`
	ChangedBy string `json:"changed_by"`
}

// configRegistry is the hardcoded map of known keys with metadata.
var configRegistry = map[string]configMeta{
	// Swarm limits
	"swarm.max_agents":     {Default: "10", EnvVar: "SWARM_MAX_AGENTS", DangerLevel: 1, Description: "Maximum concurrent agents", Validate: validatePositiveInt(1, 1000)},
	"swarm.max_tasks":      {Default: "50", EnvVar: "SWARM_MAX_TASKS", DangerLevel: 0, Description: "Maximum tracked tasks", Validate: validatePositiveInt(1, 10000)},
	"swarm.max_disk_mb":    {Default: "5000", EnvVar: "SWARM_MAX_DISK_MB", DangerLevel: 1, Description: "Max worktree disk usage (MB)", Validate: validatePositiveInt(100, 0)},
	"swarm.cost_limit_usd": {Default: "5.0", EnvVar: "SWARM_COST_LIMIT_USD", DangerLevel: 1, Description: "Per-session cost limit (USD)", Validate: validatePositiveFloat(0)},
	"swarm.stuck_timeout":  {Default: "120", EnvVar: "SWARM_STUCK_TIMEOUT", DangerLevel: 0, Description: "Seconds before agent marked stuck", Validate: validatePositiveInt(10, 0)},
	// Budget
	"swarm.budget_max_total":     {Default: "100.0", EnvVar: "SWARM_BUDGET_MAX_TOTAL", DangerLevel: 1, Description: "Total budget ceiling (USD)", Validate: validatePositiveFloat(0)},
	"swarm.budget_autoraise_pct": {Default: "10", EnvVar: "SWARM_BUDGET_AUTORAISE_PCT", DangerLevel: 1, Description: "Auto-raise budget by this % when limit hit", Validate: validatePositiveInt(0, 100)},
	// Spawn rate
	"swarm.spawn_rate_interval": {Default: "60", EnvVar: "SWARM_SPAWN_RATE_INTERVAL", DangerLevel: 0, Description: "Spawn rate interval (seconds)", Validate: validatePositiveInt(1, 0)},
	"swarm.spawn_rate_burst":    {Default: "3", EnvVar: "SWARM_SPAWN_RATE_BURST", DangerLevel: 0, Description: "Spawn burst limit", Validate: validatePositiveInt(1, 100)},
	// Triage
	"swarm.triage_enabled":  {Default: "false", EnvVar: "SWARM_TRIAGE_ENABLED", DangerLevel: 0, Description: "Enable background triage agent", Validate: validateBoolValue()},
	"swarm.triage_interval": {Default: "3600", EnvVar: "SWARM_TRIAGE_INTERVAL", DangerLevel: 0, Description: "Triage check interval (seconds)", Validate: validatePositiveInt(60, 0)},
	// Fleet
	"fleet.mode":   {Default: "normal", DangerLevel: 2, Restartable: false, Description: "Fleet operating mode: normal/contain/stabilize", Validate: validateEnum("normal", "contain", "stabilize")},
	"fleet.paused": {Default: "false", DangerLevel: 2, Restartable: false, Description: "Pause all new agent spawning", Validate: validateBoolValue()},
	// Display
	"display.log_verbosity": {Default: "info", DangerLevel: 0, Description: "TUI log verbosity: info/debug/trace", Validate: validateEnum("info", "debug", "trace")},
	"display.timestamps":    {Default: "relative", DangerLevel: 0, Description: "Timestamp format: relative/absolute/none", Validate: validateEnum("relative", "absolute", "none")},
}

// configService is the settings service backed by SQLite.
type configService struct {
	db      *sql.DB
	mu      sync.RWMutex
	runtime map[string]string // in-memory overrides (scope=runtime), highest precedence
	dbCache map[string]string // read-through cache for DB layer; invalidated on Set()
}

var globalConfigService *configService

// newConfigService creates a new configService backed by the given DB.
func newConfigService(db *sql.DB) *configService {
	return &configService{
		db:      db,
		runtime: make(map[string]string),
		dbCache: make(map[string]string),
	}
}

// Get resolves a config key with precedence: runtime > DB > env var > default.
func (cs *configService) Get(key string) configEntry {
	meta, known := configRegistry[key]
	dangerLevel := 0
	restartable := false
	if known {
		dangerLevel = meta.DangerLevel
		restartable = meta.Restartable
	}

	// 1. Runtime override (in-memory, highest precedence)
	cs.mu.RLock()
	rv, hasRuntime := cs.runtime[key]
	cv, hasCache := cs.dbCache[key]
	cs.mu.RUnlock()

	if hasRuntime {
		return configEntry{Key: key, Value: rv, Source: scopeRuntime, DangerLevel: dangerLevel, Restartable: restartable}
	}

	// 2. DB value — served from cache when available
	if hasCache {
		return configEntry{Key: key, Value: cv, Source: scopeDB, DangerLevel: dangerLevel, Restartable: restartable}
	}

	// Cache miss — query DB
	var dbValue string
	err := cs.db.QueryRow("SELECT value FROM system_config WHERE key = ?", key).Scan(&dbValue)
	if err == nil {
		cs.mu.Lock()
		cs.dbCache[key] = dbValue
		cs.mu.Unlock()
		return configEntry{Key: key, Value: dbValue, Source: scopeDB, DangerLevel: dangerLevel, Restartable: restartable}
	}
	if err != sql.ErrNoRows {
		log.Printf("config: DB query for key %q: %v", key, err)
	}

	// 3. Env var baseline (if registered)
	if known && meta.EnvVar != "" {
		if envVal := os.Getenv(meta.EnvVar); envVal != "" {
			return configEntry{Key: key, Value: envVal, Source: scopeEnv, DangerLevel: dangerLevel, Restartable: restartable}
		}
	}

	// 4. Hardcoded default
	defaultVal := ""
	if known {
		defaultVal = meta.Default
	}
	return configEntry{Key: key, Value: defaultVal, Source: scopeDefault, DangerLevel: dangerLevel, Restartable: restartable}
}

// GetString returns the string value for a key, or fallback if not found/empty.
func (cs *configService) GetString(key, fallback string) string {
	e := cs.Get(key)
	if e.Value == "" {
		return fallback
	}
	return e.Value
}

// GetInt returns the int value for a key, or fallback if not parseable.
func (cs *configService) GetInt(key string, fallback int) int {
	e := cs.Get(key)
	if v, err := strconv.Atoi(strings.TrimSpace(e.Value)); err == nil {
		return v
	}
	return fallback
}

// GetBool returns the bool value for a key, or fallback if not parseable.
func (cs *configService) GetBool(key string, fallback bool) bool {
	e := cs.Get(key)
	if v, err := strconv.ParseBool(strings.TrimSpace(e.Value)); err == nil {
		return v
	}
	return fallback
}

// Set writes a value to the DB and appends an audit history row.
// Returns an error if the key is not in the registry or the value fails validation.
func (cs *configService) Set(key, value, changedBy string) error {
	meta, ok := configRegistry[key]
	if !ok {
		return fmt.Errorf("config: unknown key %q", key)
	}
	if meta.Validate != nil {
		if err := meta.Validate(value); err != nil {
			return fmt.Errorf("config: invalid value for %q: %w", key, err)
		}
	}

	// Read current value for history (may not exist yet).
	var oldValue sql.NullString
	cs.db.QueryRow("SELECT value FROM system_config WHERE key = ?", key).Scan(&oldValue) //nolint:errcheck
	// Convert to a driver-friendly parameter: nil for NULL, string otherwise.
	var oldParam interface{}
	if oldValue.Valid {
		oldParam = oldValue.String
	}

	tx, err := cs.db.Begin()
	if err != nil {
		return fmt.Errorf("config: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.Exec(
		`INSERT INTO system_config (key, value, changed_at, changed_by)
		 VALUES (?, ?, unixepoch(), ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, changed_at=excluded.changed_at, changed_by=excluded.changed_by`,
		key, value, changedBy,
	)
	if err != nil {
		return fmt.Errorf("config: upsert key %q: %w", key, err)
	}

	_, err = tx.Exec(
		`INSERT INTO system_config_history (key, old_value, new_value, changed_at, changed_by)
		 VALUES (?, ?, ?, unixepoch(), ?)`,
		key, oldParam, value, changedBy,
	)
	if err != nil {
		return fmt.Errorf("config: insert history for key %q: %w", key, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("config: commit tx: %w", err)
	}

	// Update read-through cache so subsequent Get() calls don't hit the DB.
	cs.mu.Lock()
	cs.dbCache[key] = value
	cs.mu.Unlock()
	return nil
}

// SetRuntime writes a value to the in-memory runtime map only (no DB, no history).
// The override is lost on restart. Logs and ignores unknown keys.
func (cs *configService) SetRuntime(key, value string) {
	if _, ok := configRegistry[key]; !ok {
		log.Printf("config: SetRuntime called with unknown key %q (ignored)", key)
		return
	}
	cs.mu.Lock()
	cs.runtime[key] = value
	cs.mu.Unlock()
}

// GetAll returns all known registry keys matching the given prefix (or all if prefix is ""),
// resolved with provenance. Warms the DB cache with a single query before resolution.
func (cs *configService) GetAll(prefix string) []configEntry {
	// Warm cache with a single SELECT rather than N individual queries.
	rows, err := cs.db.Query("SELECT key, value FROM system_config")
	if err != nil {
		log.Printf("config: GetAll DB scan: %v", err)
	} else {
		defer rows.Close()
		cs.mu.Lock()
		for rows.Next() {
			var k, v string
			if rows.Scan(&k, &v) == nil {
				cs.dbCache[k] = v
			}
		}
		cs.mu.Unlock()
	}

	entries := make([]configEntry, 0, len(configRegistry))
	for key := range configRegistry {
		if prefix == "" || strings.HasPrefix(key, prefix) {
			entries = append(entries, cs.Get(key))
		}
	}
	return entries
}

// History returns up to limit rows from system_config_history for the given key.
func (cs *configService) History(key string, limit int) ([]configChange, error) {
	rows, err := cs.db.Query(
		`SELECT id, key, COALESCE(old_value, ''), new_value, changed_at, changed_by
		 FROM system_config_history
		 WHERE key = ?
		 ORDER BY id DESC
		 LIMIT ?`,
		key, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("config: query history for key %q: %w", key, err)
	}
	defer rows.Close()

	var changes []configChange
	for rows.Next() {
		var c configChange
		if err := rows.Scan(&c.ID, &c.Key, &c.OldValue, &c.NewValue, &c.ChangedAt, &c.ChangedBy); err != nil {
			return nil, fmt.Errorf("config: scan history row: %w", err)
		}
		changes = append(changes, c)
	}
	return changes, rows.Err()
}

// Rollback finds the history record by ID and calls Set with its old_value.
func (cs *configService) Rollback(key string, historyID int64, changedBy string) error {
	var oldValue sql.NullString
	err := cs.db.QueryRow(
		`SELECT old_value FROM system_config_history WHERE id = ? AND key = ?`,
		historyID, key,
	).Scan(&oldValue)
	if err != nil {
		return fmt.Errorf("config: rollback — history record %d not found for key %q: %w", historyID, key, err)
	}
	if !oldValue.Valid {
		return fmt.Errorf("config: rollback — history record %d for key %q has no old value to restore", historyID, key)
	}
	return cs.Set(key, oldValue.String, changedBy)
}

// ─── HTTP handler ─────────────────────────────────────────────────────────────

// handleConfigAPI handles:
//
//	GET  /api/swarm/config           — list all config entries
//	GET  /api/swarm/config/{key}     — get single entry
//	PUT  /api/swarm/config/{key}     — set value (body: {"value":"...","changed_by":"tui"})
//	GET  /api/swarm/config/{key}/history — get change history
func handleConfigAPI(w http.ResponseWriter, r *http.Request) {
	if globalConfigService == nil {
		http.Error(w, "config service not initialised", http.StatusServiceUnavailable)
		return
	}

	// Strip the /api/swarm/config prefix.
	path := strings.TrimPrefix(r.URL.Path, "/api/swarm/config")
	path = strings.Trim(path, "/")

	w.Header().Set("Content-Type", "application/json")

	// GET /api/swarm/config  — list all
	if path == "" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		entries := globalConfigService.GetAll("")
		json.NewEncoder(w).Encode(entries) //nolint:errcheck
		return
	}

	// /api/swarm/config/{key}/history
	if strings.HasSuffix(path, "/history") {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		key := strings.TrimSuffix(path, "/history")
		limitStr := r.URL.Query().Get("limit")
		limit := 50
		if limitStr != "" {
			if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
				limit = n
			}
		}
		changes, err := globalConfigService.History(key, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(changes) //nolint:errcheck
		return
	}

	// /api/swarm/config/{key}
	key := path
	switch r.Method {
	case http.MethodGet:
		entry := globalConfigService.Get(key)
		json.NewEncoder(w).Encode(entry) //nolint:errcheck

	case http.MethodPut:
		var body struct {
			Value     string `json:"value"`
			ChangedBy string `json:"changed_by"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if body.ChangedBy == "" {
			body.ChangedBy = "api"
		}
		if err := globalConfigService.Set(key, body.Value, body.ChangedBy); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		entry := globalConfigService.Get(key)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(entry) //nolint:errcheck

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
