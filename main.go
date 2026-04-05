package main

import (
	"bufio"
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func init() {
	f, err := os.Open(".env")
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			if os.Getenv(k) == "" {
				os.Setenv(k, v)
			}
		}
	}
}

var database *sql.DB

func main() {
	// TUI subcommand: ./swarmops tui
	if len(os.Args) > 1 && os.Args[1] == "tui" {
		RunSwarmTUI()
		return
	}

	// Initialize database
	database = initDatabase()
	defer database.Close()

	// Initialize config service
	globalConfigService = newConfigService(database)

	// Server context for background workers
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	// Start warm session pool if enabled
	initPool(serverCtx)

	// Periodic session status sync
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				syncTmuxSessions()
			case <-serverCtx.Done():
				return
			}
		}
	}()

	// HTTP routes
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","version":"2.0"}`))
	})

	http.HandleFunc("/api/", handleAPI)

	// OpenAI-compatible pool API
	http.HandleFunc("/v1/chat/completions", handlePoolChatCompletions)
	http.HandleFunc("/v1/models", handlePoolListModels)
	http.HandleFunc("/api/swarm/pool", handlePoolStatusAPI)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("SwarmOps starting on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
