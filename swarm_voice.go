package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

type voiceConfig struct {
	sttURL    string
	ttsURL    string
	ttsVoice  string
	chatModel string
}

func getVoiceConfig() voiceConfig {
	return voiceConfig{
		sttURL:    envOrDefault("SWARM_STT_URL", "http://10.0.1.226:8300"),
		ttsURL:    envOrDefault("SWARM_TTS_URL", "http://10.0.1.226:8880"),
		ttsVoice:  envOrDefault("SWARM_TTS_VOICE", "bf_emma"),
		chatModel: envOrDefault("SWARM_VOICE_MODEL", "claude-haiku-4-5-20251001"),
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return strings.TrimRight(v, "/")
	}
	return def
}

// ---------------------------------------------------------------------------
// Circuit breakers — fast-fail when a service is repeatedly unreachable
// ---------------------------------------------------------------------------

type serviceCircuit struct {
	mu          sync.Mutex
	failures    int
	lastFailure time.Time
	open        bool
}

const circuitOpenDuration  = 30 * time.Second
const circuitFailThreshold = 3

func (c *serviceCircuit) allow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.open {
		return true
	}
	if time.Since(c.lastFailure) > circuitOpenDuration {
		c.open = false
		c.failures = 0
		return true
	}
	return false
}

func (c *serviceCircuit) fail() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures++
	c.lastFailure = time.Now()
	if c.failures >= circuitFailThreshold {
		c.open = true
	}
}

func (c *serviceCircuit) succeed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures = 0
	c.open = false
}

var (
	sttCircuit = &serviceCircuit{}
	ttsCircuit = &serviceCircuit{}
)

// ---------------------------------------------------------------------------
// STT — POST /api/swarm/transcribe
// ---------------------------------------------------------------------------

// handleSwarmTranscribeAPI accepts multipart audio and proxies to speaches STT.
func handleSwarmTranscribeAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if !sttCircuit.allow() {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "STT service temporarily unavailable"}) //nolint:errcheck
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 25<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "parse error: " + err.Error()}) //nolint:errcheck
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "file field missing"}) //nolint:errcheck
		return
	}
	defer file.Close()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		defer pw.Close()
		fw, err := mw.CreateFormFile("file", sanitizeFilename(header.Filename))
		if err != nil {
			pw.CloseWithError(fmt.Errorf("multipart build: %w", err))
			return
		}
		if _, err = io.Copy(fw, file); err != nil {
			pw.CloseWithError(fmt.Errorf("audio copy: %w", err))
			return
		}
		model := r.FormValue("model")
		if model == "" {
			model = "Systran/faster-distil-whisper-large-v3"
		}
		if err := mw.WriteField("model", model); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := mw.WriteField("response_format", "json"); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := mw.Close(); err != nil {
			pw.CloseWithError(err)
		}
	}()

	cfg := getVoiceConfig()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		cfg.sttURL+"/v1/audio/transcriptions", pr)
	if err != nil {
		pr.CloseWithError(err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "request build error"}) //nolint:errcheck
		return
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		sttCircuit.fail()
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": "STT unreachable: " + err.Error()}) //nolint:errcheck
		return
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		sttCircuit.fail()
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": "STT read error: " + readErr.Error()}) //nolint:errcheck
		return
	}

	if resp.StatusCode != http.StatusOK {
		sttCircuit.fail()
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
			"error":  "STT returned " + resp.Status,
			"detail": string(body),
		})
		return
	}

	sttCircuit.succeed()
	w.WriteHeader(http.StatusOK)
	w.Write(body) //nolint:errcheck
}

// ---------------------------------------------------------------------------
// TTS — POST /api/swarm/tts
// ---------------------------------------------------------------------------

// handleSwarmTTSAPI converts text to speech via Kokoro and returns MP3.
func handleSwarmTTSAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if !ttsCircuit.allow() {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "TTS service temporarily unavailable"}) //nolint:errcheck
		return
	}

	var req struct {
		Text  string `json:"text"`
		Voice string `json:"voice"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "text required"}) //nolint:errcheck
		return
	}
	if len(req.Text) > 500 {
		req.Text = req.Text[:500]
	}

	cfg := getVoiceConfig()
	if req.Voice == "" {
		req.Voice = cfg.ttsVoice
	}

	body, _ := json.Marshal(map[string]string{
		"model":           "kokoro",
		"input":           req.Text,
		"voice":           req.Voice,
		"response_format": "mp3",
	})

	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		cfg.ttsURL+"/v1/audio/speech", bytes.NewReader(body))
	if err != nil {
		ttsCircuit.fail()
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "request build error"}) //nolint:errcheck
		return
	}
	upstream.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(upstream)
	if err != nil {
		ttsCircuit.fail()
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": "TTS unreachable: " + err.Error()}) //nolint:errcheck
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		ttsCircuit.fail()
		errBody, _ := io.ReadAll(resp.Body)
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
			"error":  "TTS returned " + resp.Status,
			"detail": string(errBody),
		})
		return
	}

	// Buffer full MP3 (Kokoro synthesises fast ~150-300ms)
	audio, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		ttsCircuit.fail()
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": "TTS read error: " + err.Error()}) //nolint:errcheck
		return
	}

	ttsCircuit.succeed()
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(audio) //nolint:errcheck
}

// ---------------------------------------------------------------------------
// Voice chat — POST /api/swarm/voice/chat
// ---------------------------------------------------------------------------

type voiceChatRequest struct {
	Message   string `json:"message"`
	SessionID string `json:"session_id"`
	// Only user turns accepted from client — server never trusts assistant content
	History []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"history"`
}

// claudeBinary is the path to the claude CLI, overridable via env var.
func claudeBinary() string {
	if v := os.Getenv("CLAUDE_BIN"); v != "" {
		return v
	}
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	return "/usr/local/bin/claude"
}

// handleSwarmVoiceChatAPI runs the voice chat pipeline via the claude CLI
// (subscription auth — no API key required).
func handleSwarmVoiceChatAPI(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req voiceChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Message) == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "message required"}) //nolint:errcheck
		return
	}

	swarmCtx := buildVoiceSwarmContext(ctx, req.SessionID)

	systemPrompt := "You are a voice assistant for SwarmOps, an AI agent orchestration platform. " +
		"You help the operator understand and control their AI coding agents. " +
		swarmCtx + " " +
		"Rules: Respond in 1-3 sentences suitable for spoken audio. " +
		"No markdown, bullet points, headers, or code blocks. " +
		"Use natural conversational language. " +
		"Be direct and specific — reference agent names, task names, and counts. " +
		"If asked to do something you cannot do yet, say so in one sentence."

	// Build conversation history as plain text — only trust user turns.
	// Keep last 6 user turns to bound prompt size.
	var histLines []string
	for _, h := range req.History {
		if h.Role != "user" || strings.TrimSpace(h.Content) == "" {
			continue
		}
		histLines = append(histLines, strings.TrimSpace(h.Content))
	}
	if len(histLines) > 6 {
		histLines = histLines[len(histLines)-6:]
	}

	var prompt strings.Builder
	for _, line := range histLines {
		prompt.WriteString("Previous message: ")
		prompt.WriteString(line)
		prompt.WriteString("\n")
	}
	prompt.WriteString(req.Message)

	cfg := getVoiceConfig()
	bin := claudeBinary()

	// 20s timeout — claude CLI startup + inference for haiku is typically 1-3s
	cmdCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, bin,
		"-p", prompt.String(),
		"--system-prompt", systemPrompt,
		"--model", cfg.chatModel,
	)

	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		detail := ""
		if errors.As(err, &exitErr) {
			detail = string(exitErr.Stderr)
		}
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
			"error":  "claude CLI error: " + err.Error(),
			"detail": detail,
		})
		return
	}

	text := strings.TrimSpace(string(out))
	if text == "" {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": "empty response from claude"}) //nolint:errcheck
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"text": text}) //nolint:errcheck
}

// ---------------------------------------------------------------------------
// SwarmOps context builder
// ---------------------------------------------------------------------------

// buildVoiceSwarmContext assembles a plain-text summary of SwarmOps state for
// injection into the voice assistant system prompt. Uses %q formatting on all
// user-controlled strings to prevent prompt injection.
func buildVoiceSwarmContext(ctx context.Context, sessionID string) string {
	var parts []string

	if sessionID != "" {
		var name string
		_ = database.QueryRowContext(ctx,
			"SELECT name FROM swarm_sessions WHERE id = ?", sessionID).Scan(&name)
		if name != "" {
			parts = append(parts, fmt.Sprintf("Current session: %q", name))
		}

		// Agents
		rows, err := database.QueryContext(ctx,
			"SELECT name, role, status, mission FROM swarm_agents WHERE session_id = ? ORDER BY role, name",
			sessionID)
		if err == nil {
			var lines []string
			for rows.Next() {
				var agName, role, status string
				var mission *string
				_ = rows.Scan(&agName, &role, &status, &mission)
				line := fmt.Sprintf("%q (%s, %s)", agName, role, status)
				if mission != nil && *mission != "" {
					line += ": " + truncate(*mission, 50)
				}
				lines = append(lines, line)
			}
			rows.Close()
			if len(lines) > 0 {
				parts = append(parts, "Agents: "+strings.Join(lines, "; "))
			}
		}

		// Tasks (most recent 8)
		rows2, err := database.QueryContext(ctx,
			"SELECT title, stage FROM swarm_tasks WHERE session_id = ? ORDER BY created_at DESC LIMIT 8",
			sessionID)
		if err == nil {
			var lines []string
			for rows2.Next() {
				var title, stage string
				_ = rows2.Scan(&title, &stage)
				lines = append(lines, fmt.Sprintf("%q (%s)", truncate(title, 40), stage))
			}
			rows2.Close()
			if len(lines) > 0 {
				parts = append(parts, "Tasks: "+strings.Join(lines, "; "))
			}
		}
	} else {
		// Dashboard summary
		rows, err := database.QueryContext(ctx, `
			SELECT s.name,
			       COUNT(DISTINCT a.id),
			       COUNT(DISTINCT CASE WHEN a.status='stuck' THEN a.id END)
			FROM swarm_sessions s
			LEFT JOIN swarm_agents a ON a.session_id = s.id
			GROUP BY s.id ORDER BY s.updated_at DESC LIMIT 5`)
		if err == nil {
			var lines []string
			for rows.Next() {
				var sName string
				var agents, stuck int
				_ = rows.Scan(&sName, &agents, &stuck)
				line := fmt.Sprintf("%q: %d agent(s)", sName, agents)
				if stuck > 0 {
					line += fmt.Sprintf(", %d stuck", stuck)
				}
				lines = append(lines, line)
			}
			rows.Close()
			if len(lines) > 0 {
				parts = append(parts, "Sessions: "+strings.Join(lines, "; "))
			}
		}
	}

	if len(parts) == 0 {
		return "No active sessions."
	}
	return strings.Join(parts, "\n")
}

// ---------------------------------------------------------------------------
// Voice sub-router — /api/swarm/voice/*
// ---------------------------------------------------------------------------

func handleSwarmVoiceAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, pathParts []string) {
	if len(pathParts) > 0 && pathParts[0] == "chat" {
		handleSwarmVoiceChatAPI(w, r, ctx)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(map[string]string{"error": "unknown voice endpoint"}) //nolint:errcheck
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	for _, sep := range []string{"/", "\\"} {
		if i := strings.LastIndex(name, sep); i >= 0 {
			name = name[i+1:]
		}
	}
	if name == "" {
		return "recording"
	}
	return name
}
