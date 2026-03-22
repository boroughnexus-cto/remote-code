package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"swarmops/db"

	_ "modernc.org/sqlite"
)

func initDatabase() (*sql.DB, *db.Queries) {
	// Enable WAL mode and a 5s busy timeout in the DSN so all pool connections
	// inherit them. Without busy_timeout, concurrent goroutines (broadcaster,
	// goal auto-task creation) cause SQLITE_BUSY under load.
	database, queries, _ := initDatabaseWithPathAndReturn(
		"swarmops.db?_pragma=journal_mode%3DWAL&_pragma=busy_timeout%3D5000",
	)
	return database, queries
}

func initTestDatabase() (*sql.DB, *db.Queries, string) {
	// Use a unique filename for each test run to avoid conflicts.
	// Bake WAL mode + busy_timeout into the DSN so they apply to every connection
	// in the pool, not just the one that happens to run PRAGMA after open.
	// This prevents SQLITE_BUSY when background goroutines (broadcaster timers,
	// kickOffGoalSpecTask) write concurrently with test assertions.
	testDbPath := fmt.Sprintf("swarmops-test-%d.db?_pragma=journal_mode%%3DWAL&_pragma=busy_timeout%%3D5000", time.Now().UnixNano())
	db, queries, path := initDatabaseWithPathAndReturn(testDbPath)
	return db, queries, path
}

func initDatabaseWithPathAndReturn(dbPath string) (*sql.DB, *db.Queries, string) {
	
	// Ensure directory exists
	dbDir := filepath.Dir(dbPath)
	if dbDir != "." {
		if err := os.MkdirAll(dbDir, 0755); err != nil {
			log.Fatalf("Failed to create database directory: %v", err)
		}
	}

	// Open database connection.
	// _pragma=foreign_keys%3Don sets foreign_keys=ON for every connection in the pool,
	// since SQLite PRAGMA settings are per-connection and database/sql uses a pool.
	dsn := dbPath
	if !strings.Contains(dbPath, "?") {
		dsn = dbPath + "?_pragma=foreign_keys%3Don"
	} else {
		dsn = dbPath + "&_pragma=foreign_keys%3Don"
	}
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}

	// Test the connection
	if err := database.Ping(); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}

	// Enable foreign key enforcement (SQLite disables it by default)
	if _, err := database.Exec("PRAGMA foreign_keys = ON"); err != nil {
		log.Printf("database: PRAGMA foreign_keys: %v", err)
	}

	// Apply migrations
	if err := applyMigrations(database); err != nil {
		log.Fatalf("Failed to apply migrations: %v", err)
	}

	queries := db.New(database)
	return database, queries, dbPath
}

func applyMigrations(database *sql.DB) error {
	// Create migrations tracking table if it doesn't exist
	_, err := database.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %v", err)
	}

	// Check if this is an existing database that needs migration seeding
	// by checking if the migrations table is empty but schema exists
	var migrationCount int
	err = database.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount)
	if err != nil {
		return fmt.Errorf("failed to count migrations: %v", err)
	}

	if migrationCount == 0 {
		// Check if 001_initial was already applied (projects table exists)
		var tableExists int
		err = database.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='projects'").Scan(&tableExists)
		if err != nil {
			return fmt.Errorf("failed to check for projects table: %v", err)
		}
		if tableExists > 0 {
			// Seed 001 as already applied
			database.Exec("INSERT OR IGNORE INTO schema_migrations (version) VALUES (?)", "db/migrations/001_initial.sql")
		}

		// Check if 002_elo_tracking was already applied (elo_rating column exists on agents)
		var eloColumnExists int
		err = database.QueryRow("SELECT COUNT(*) FROM pragma_table_info('agents') WHERE name='elo_rating'").Scan(&eloColumnExists)
		if err != nil {
			return fmt.Errorf("failed to check for elo_rating column: %v", err)
		}
		if eloColumnExists > 0 {
			// Seed 002 as already applied
			database.Exec("INSERT OR IGNORE INTO schema_migrations (version) VALUES (?)", "db/migrations/002_elo_tracking.sql")
		}

		// Check if 003_remove_worktrees was already applied (worktrees table doesn't exist)
		var worktreesExists int
		err = database.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='worktrees'").Scan(&worktreesExists)
		if err != nil {
			return fmt.Errorf("failed to check for worktrees table: %v", err)
		}
		if worktreesExists == 0 && tableExists > 0 {
			// Worktrees table doesn't exist but other tables do, 003 was already applied
			database.Exec("INSERT OR IGNORE INTO schema_migrations (version) VALUES (?)", "db/migrations/003_remove_worktrees.sql")
		}
	}

	migrations := []string{
		"db/migrations/001_initial.sql",
		"db/migrations/002_elo_tracking.sql",
		"db/migrations/003_remove_worktrees.sql",
		"db/migrations/004_remote_ports.sql",
		"db/migrations/005_webauthn.sql",
		"db/migrations/006_webauthn_add_rp_id.sql",
		"db/migrations/007_directory_dev_servers.sql",
		"db/migrations/008_webauthn_backup_flags.sql",
		"db/migrations/009_execution_milestones.sql",
		"db/migrations/010_swarm.sql",
		"db/migrations/011_swarm_v2.sql",
		"db/migrations/012_swarm_v3.sql",
		"db/migrations/013_agent_memory.sql",
		"db/migrations/014_task_pr.sql",
		"db/migrations/015_drop_swarm_api_token.sql",
		"db/migrations/016_agent_mission.sql",
		"db/migrations/017_ipc_fields.sql",
		"db/migrations/018_blackboard.sql",
		"db/migrations/019_task_lifecycle.sql",
		"db/migrations/020_context_mgmt.sql",
		"db/migrations/021_goals.sql",
		"db/migrations/022_task_phase.sql",
		"db/migrations/023_ci_status.sql",
		"db/migrations/024_goal_plane_id.sql",
		"db/migrations/025_autopilot.sql",
		"db/migrations/026_reliability.sql",
		"db/migrations/027_triage.sql",
		"db/migrations/028_agent_metrics.sql",
		"db/migrations/029_event_log.sql",
		"db/migrations/030_role_prompts.sql",
		"db/migrations/031_fix_tasks_fk.sql",
		"db/migrations/032_status_timestamps.sql",
		"db/migrations/033_agent_runs.sql",
		"db/migrations/034_agent_escalations.sql",
		"db/migrations/035_tool_restrictions.sql",
		"db/migrations/036_batch3.sql",
		"db/migrations/037_batch4.sql",
		"db/migrations/039_h2_merge_policy.sql",
		"db/migrations/040_c3_handoff.sql",
		"db/migrations/041_c1_ralph.sql",
		"db/migrations/042_h1_leases.sql",
		"db/migrations/043_system_config.sql",
		"db/migrations/044_session_contexts.sql",
		"db/migrations/045_fleet_state.sql",
		"db/migrations/046_session_context_link.sql",
	}

	for _, migrationPath := range migrations {
		// Check if migration has already been applied
		var count int
		err := database.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", migrationPath).Scan(&count)
		if err != nil {
			return fmt.Errorf("failed to check migration status for %s: %v", migrationPath, err)
		}
		if count > 0 {
			// Migration already applied, skip
			continue
		}

		migrationSQL, err := os.ReadFile(migrationPath)
		if err != nil {
			return fmt.Errorf("failed to read migration %s: %v", migrationPath, err)
		}

		_, err = database.Exec(string(migrationSQL))
		if err != nil {
			// Handle ALTER TABLE errors for columns that already exist
			// This can happen when migration 006 runs on a fresh DB where 005 already created the column
			if strings.Contains(err.Error(), "duplicate column name") {
				log.Printf("Migration %s: column already exists, skipping", migrationPath)
			} else {
				return fmt.Errorf("failed to execute migration %s: %v", migrationPath, err)
			}
		}

		// Record that migration was applied
		_, err = database.Exec("INSERT INTO schema_migrations (version) VALUES (?)", migrationPath)
		if err != nil {
			return fmt.Errorf("failed to record migration %s: %v", migrationPath, err)
		}
	}

	return nil
}