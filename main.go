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
	// Shared initialisation
	database = initDatabase()
	defer database.Close()

	globalConfigService = newConfigService(database)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initPool(ctx)

	// Periodic session status sync (both modes)
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

	term := isTerminal()

	// Redirect logs before starting HTTP server so TUI alt-screen is not corrupted
	if term {
		f, err := os.OpenFile("swarmops.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			log.SetOutput(f)
			defer f.Close()
		}
	}

	// HTTP server on a private mux
	server := newHTTPServer()

	// Start HTTP server in background
	serverErr := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	log.Printf("SwarmOps starting on %s", server.Addr)

	// Signal handler for graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	// In TUI mode, HTTP errors are non-fatal (e.g. port already in use)
	if term {
		go func() {
			if err := <-serverErr; err != nil {
				log.Printf("HTTP server error (non-fatal): %v", err)
			}
		}()
	}

	// TUI runs in a goroutine so main can select on all shutdown triggers
	tuiDone := make(chan error, 1)
	if term {
		go func() {
			tuiDone <- runTUI()
		}()
	}

	// Unified shutdown: wait for TUI exit, signal, or HTTP error
	select {
	case err := <-tuiDone:
		if err != nil {
			fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		}
	case <-sig:
		log.Printf("Received shutdown signal")
	case err := <-serverErr: // headless only (TUI mode drains above)
		log.Printf("HTTP server error: %v", err)
	}

	// Graceful shutdown
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
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
