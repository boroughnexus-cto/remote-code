package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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
	// Subcommand routing
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "tui":
			runTUIClient()
			return
		case "redeploy":
			runRedeploy()
			return
		}
	}

	// TTY with no subcommand → TUI client
	if isTerminal() {
		runTUIClient()
		return
	}

	// Server mode: database, config, HTTP API, then pool (pool spawns are slow)
	database = initDatabase()
	defer database.Close()

	globalConfigService = newConfigService(database)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start HTTP server BEFORE pool so health checks pass during pool startup
	server := newHTTPServer()

	serverErr := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	log.Printf("SwarmOps server starting on %s", server.Addr)

	// Pool init is slow (spawns 6 Claude CLI sessions) — runs after HTTP is up
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

// runRedeploy pulls latest from git, rebuilds, restarts the backend service, then launches the TUI.
func runRedeploy() {
	dir, _ := os.Getwd()
	// Find the swarmops source directory (where main.go lives)
	srcDir := dir
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
		// Try the binary's directory
		exe, _ := os.Executable()
		srcDir = filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(srcDir, "main.go")); err != nil {
			fmt.Fprintf(os.Stderr, "Cannot find swarmops source directory\n")
			os.Exit(1)
		}
	}

	steps := []struct {
		name string
		cmd  string
		args []string
	}{
		{"Pulling latest", "git", []string{"pull", "--ff-only", "origin", "main"}},
		{"Building", "go", []string{"build", "-o", "swarmops", "."}},
		{"Running tests", "go", []string{"test", "./...", "-count=1", "-timeout=60s"}},
		{"Restarting service", "systemctl", []string{"--user", "restart", "swarmops"}},
	}

	for _, step := range steps {
		fmt.Printf("  %s...", step.name)
		cmd := exec.Command(step.cmd, step.args...)
		cmd.Dir = srcDir
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Printf(" FAILED\n")
			fmt.Fprintf(os.Stderr, "%s\n", string(out))
			if step.name == "Running tests" {
				fmt.Printf("  (continuing despite test failure)\n")
				continue
			}
			os.Exit(1)
		}
		fmt.Printf(" done\n")
	}

	// Wait for service to come up (pool spawns 6 Claude sessions — can take 60s+)
	fmt.Printf("  Waiting for backend...")
	ready := false
	for i := 0; i < 120; i++ {
		time.Sleep(500 * time.Millisecond)
		resp, err := http.Get("http://localhost:8080/")
		if err == nil {
			resp.Body.Close()
			fmt.Printf(" ready (%ds)\n\n", (i+1)/2)
			ready = true
			break
		}
		if i%10 == 9 {
			fmt.Printf(".")
		}
	}
	if !ready {
		fmt.Printf(" timeout after 60s\n")
		fmt.Fprintf(os.Stderr, "Check: journalctl --user -u swarmops -n 20\n")
		os.Exit(1)
	}

	// Launch TUI
	runTUIClient()
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
