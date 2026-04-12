package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
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
	// Client mode: "tui" subcommand or TTY detected — connect to backend via HTTP
	if len(os.Args) > 1 && os.Args[1] == "tui" || isTerminal() {
		runTUIClient()
		return
	}

	// Server mode: database, config, pool, HTTP API
	database = initDatabase()
	defer database.Close()

	globalConfigService = newConfigService(database)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initPool(ctx)

	// Periodic session status sync
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				syncTmuxSessions()
			case <-ctx.Done():
				return
			}
		}
	}()

	// Headless server mode
	server := newHTTPServer()

	serverErr := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	log.Printf("SwarmOps server starting on %s", server.Addr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sig:
		log.Printf("Received shutdown signal")
	case err := <-serverErr:
		log.Printf("HTTP server error: %v", err)
	}

	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}
}

// runTUIClient starts the TUI as an HTTP client against the backend.
func runTUIClient() {
	baseURL := os.Getenv("SWARM_API_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}

	api := newAPIClient(baseURL)

	// Health check before launching TUI
	if err := api.healthCheck(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintf(os.Stderr, "Start the backend with: systemctl --user start swarmops\n")
		os.Exit(1)
	}

	// Redirect logs so TUI alt-screen is clean
	f, err := os.OpenFile("swarmops.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		log.SetOutput(f)
		defer f.Close()
	}

	if err := runTUI(api); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}
func newHTTPServer() *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","version":"2.0"}`))
	})

	mux.HandleFunc("/api/", handleAPI)

	// OpenAI-compatible pool API
	mux.HandleFunc("/v1/chat/completions", handlePoolChatCompletions)
	mux.HandleFunc("/v1/models", handlePoolListModels)
	mux.HandleFunc("/api/swarm/pool", handlePoolStatusAPI)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	return &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}
}

func isTerminal() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout} {
		fi, err := f.Stat()
		if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}
