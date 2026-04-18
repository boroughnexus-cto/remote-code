package main

import (
	"database/sql"
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func initDatabase() *sql.DB {
	database, _ := initDatabaseWithPathAndReturn(
		"swarmops.db?_pragma=journal_mode%3DWAL&_pragma=busy_timeout%3D5000",
	)
	return database
}

func initTestDatabase() (*sql.DB, string) {
	testDbPath := fmt.Sprintf("swarmops-test-%d.db?_pragma=journal_mode%%3DWAL&_pragma=busy_timeout%%3D5000", time.Now().UnixNano())
	db, path := initDatabaseWithPathAndReturn(testDbPath)
	return db, path
}

func initDatabaseWithPathAndReturn(dbPath string) (*sql.DB, string) {
	dbDir := filepath.Dir(dbPath)
	if dbDir != "." {
		if err := os.MkdirAll(dbDir, 0755); err != nil {
			log.Fatalf("Failed to create database directory: %v", err)
		}
	}

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

	if err := database.Ping(); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}

	if _, err := database.Exec("PRAGMA foreign_keys = ON"); err != nil {
		log.Printf("database: PRAGMA foreign_keys: %v", err)
	}

	if err := applyMigrations(database); err != nil {
		log.Fatalf("Failed to apply migrations: %v", err)
	}

	return database, dbPath
}

//go:embed db/migrations/*.sql
var migrationsFS embed.FS

func applyMigrations(database *sql.DB) error {
	_, err := database.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %v", err)
	}

	var migrationCount int
	err = database.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount)
	if err != nil {
		return fmt.Errorf("failed to count migrations: %v", err)
	}

	if migrationCount == 0 {
		var tableExists int
		err = database.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='projects'").Scan(&tableExists)
		if err != nil {
			return fmt.Errorf("failed to check for projects table: %v", err)
		}
		if tableExists > 0 {
			database.Exec("INSERT OR IGNORE INTO schema_migrations (version) VALUES (?)", "db/migrations/001_initial.sql")
		}

		var eloColumnExists int
		err = database.QueryRow("SELECT COUNT(*) FROM pragma_table_info('agents') WHERE name='elo_rating'").Scan(&eloColumnExists)
		if err != nil {
			return fmt.Errorf("failed to check for elo_rating column: %v", err)
		}
		if eloColumnExists > 0 {
			database.Exec("INSERT OR IGNORE INTO schema_migrations (version) VALUES (?)", "db/migrations/002_elo_tracking.sql")
		}

		var worktreesExists int
		err = database.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='worktrees'").Scan(&worktreesExists)
		if err != nil {
			return fmt.Errorf("failed to check for worktrees table: %v", err)
		}
		if worktreesExists == 0 && tableExists > 0 {
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
		"db/migrations/047_context_dynamic.sql",
		"db/migrations/048_autopilot_label_filter.sql",
		"db/migrations/049_swarm_mode.sql",
		"db/migrations/050_pool.sql",
		"db/migrations/051_sessions.sql",
		"db/migrations/052_session_mission.sql",
		"db/migrations/053_session_persistence.sql",
		"db/migrations/054_task_bus.sql",
		"db/migrations/055_session_audit.sql",
		"db/migrations/056_session_model.sql",
	}

	for _, migrationPath := range migrations {
		var count int
		err := database.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", migrationPath).Scan(&count)
		if err != nil {
			return fmt.Errorf("failed to check migration status for %s: %v", migrationPath, err)
		}
		if count > 0 {
			continue
		}

		migrationSQL, err := migrationsFS.ReadFile(migrationPath)
		if err != nil {
			return fmt.Errorf("failed to read migration %s: %v", migrationPath, err)
		}

		_, err = database.Exec(string(migrationSQL))
		if err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				log.Printf("Migration %s: column already exists, skipping", migrationPath)
			} else {
				return fmt.Errorf("failed to execute migration %s: %v", migrationPath, err)
			}
		}

		_, err = database.Exec("INSERT INTO schema_migrations (version) VALUES (?)", migrationPath)
		if err != nil {
			return fmt.Errorf("failed to record migration %s: %v", migrationPath, err)
		}
	}

	return nil
}
