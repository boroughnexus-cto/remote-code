package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"swarmops/db"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

func init() {
	// Load .env file if present (ignored in production where env vars are set externally)
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
			if os.Getenv(k) == "" { // don't override real env vars
				os.Setenv(k, v)
			}
		}
	}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var database *sql.DB
var queries *db.Queries

func main() {
	// TUI subcommand: ./swarmops tui
	if len(os.Args) > 1 && os.Args[1] == "tui" {
		RunSwarmTUI()
		return
	}
	// CLI subcommands: status / task / inject
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "status", "task", "inject":
			runCLI(os.Args[1:])
			return
		}
	}

	// Initialize database
	database, queries = initDatabase()
	defer database.Close()

	// Initialize agent transport (tmux for now; channels in Phase 3)
	swarmTransport = initTransport()

	// Initialize Telegram escalation router (no-op when env vars are absent).
	telegramRouter = initTelegramRouter()

	// Ensure the Claude Code Stop hook script is written to disk
	ensureSwarmHookScript()

	// Start swarm agent status monitor
	startSwarmMonitor()
	startIPCPoller()
	startTaskWatchdog()
	startOrphanSweeper()
	startDiskUsagePoller()
	validateIntegrationConfig()
	go startCIPoller(context.Background())
	go startPlaneAdapter(context.Background())
	go startTriagePoller(context.Background())
	startAutoDispatchLoop(context.Background())

	// Setup HTTP routes
	http.HandleFunc("/", serveHome)
	http.HandleFunc("/ws", authMiddleware(handleWebSocket))
	http.HandleFunc("/ws/swarm", authMiddleware(handleSwarmWebSocket))
	http.HandleFunc("/api/", handleAPIWithAuth)

	// Channels SSE endpoint: Claude Code connects here when launched with --channels.
	// Auth is enforced inside ServeSSE via run_token; active only when a
	// ChannelsTransport is reachable through swarmTransport.
	if ct := getChannelsTransport(); ct != nil {
		http.HandleFunc("GET /mcp/channels/{agentID}/{runID}", ct.ServeSSE)
		http.HandleFunc("POST /mcp/channels/{agentID}/{runID}/messages", ct.ServeMessages)
		log.Printf("transport: channels MCP endpoints registered at /mcp/channels/{agentID}/{runID}")
	}

	// Telegram webhook: registered outside auth middleware because Telegram uses
	// its own secret-token header (TELEGRAM_WEBHOOK_SECRET) for auth.
	if telegramRouter != nil {
		http.HandleFunc("POST /api/telegram/webhook", telegramRouter.HandleWebhook)
		log.Printf("telegram: webhook registered at POST /api/telegram/webhook")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Server starting on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// handleAPIWithAuth wraps the API handler with authentication where needed
func handleAPIWithAuth(w http.ResponseWriter, r *http.Request) {
	// Auth endpoints don't require authentication
	if strings.HasPrefix(r.URL.Path, "/api/auth/") {
		handleAPI(w, r)
		return
	}

	// All other API endpoints require authentication
	authMiddleware(handleAPI)(w, r)
}

func serveHome(w http.ResponseWriter, r *http.Request) {
	// API routes should be handled by the API handler
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}

	// Check if the requested file exists in static directory
	filePath := "static" + r.URL.Path
	if r.URL.Path == "/" {
		filePath = "static/index.html"
	}

	// SvelteKit adapter-static generates <route>.html alongside <route>/ directories.
	// Serve in priority order: exact file → route.html → SPA fallback index.html.
	// Never serve a directory listing (that would expose the raw directory).
	if fi, err := os.Stat(filePath); err == nil && !fi.IsDir() {
		http.ServeFile(w, r, filePath)
		return
	}
	// Try the .html variant (e.g. /swarm → static/swarm.html)
	if r.URL.Path != "/" {
		htmlPath := "static" + strings.TrimRight(r.URL.Path, "/") + ".html"
		if fi, err := os.Stat(htmlPath); err == nil && !fi.IsDir() {
			http.ServeFile(w, r, htmlPath)
			return
		}
	}
	// SPA fallback for client-side routes (e.g. /swarm/[session])
	http.ServeFile(w, r, "static/index.html")
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// Check if a specific session is requested
	sessionName := r.URL.Query().Get("session")
	
	var cmd *exec.Cmd
	if sessionName != "" {
		// Attach to specific tmux session
		log.Printf("Attaching to tmux session: %s", sessionName)
		cmd = exec.Command("tmux", "attach-session", "-t", sessionName)
	} else {
		// Create or attach to global session for general terminal use
		log.Printf("Creating/attaching to global terminal session")
		cmd = exec.Command("tmux", "new-session", "-A", "-s", "swarmops")
	}

	// Ensure proper terminal environment for UTF-8 and colors
	cmd.Env = append(os.Environ(),
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"TERM=xterm-256color",
	)
 
 	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("Failed to start pty: %v", err)
		return
	}
	defer ptmx.Close()

	go func() {
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Printf("WebSocket read error: %v", err)
				break
			}

			// Try to parse control messages (e.g., resize)
			type resizeMsg struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			var rm resizeMsg
			if err := json.Unmarshal(message, &rm); err == nil && rm.Type == "resize" {
				if rm.Cols > 0 && rm.Rows > 0 {
					_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(rm.Cols), Rows: uint16(rm.Rows)})
				}
				continue
			}

			ptmx.Write(message)
		}
	}()

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				if err != io.EOF {
					log.Printf("PTY read error: %v", err)
				}
				break
			}
			// Send raw bytes as a binary WebSocket frame; the browser will decode UTF-8 progressively
			if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				log.Printf("WebSocket write error: %v", err)
				break
			}
		}
	}()

	cmd.Wait()
}