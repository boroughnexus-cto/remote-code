package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ─── CLI subcommands ───────────────────────────────────────────────────────────
//
// Usage:
//
//	swarmops status [<session-name>]
//	swarmops task add <session-name-or-id> <title> [description]
//	swarmops inject <session-name-or-id> <agent-name-or-id> <message>
//
// All commands hit the local HTTP server (localhost:PORT). Localhost traffic
// bypasses auth, so no credentials are needed.

func runCLI(args []string) {
	if len(args) == 0 {
		cliUsage()
		os.Exit(1)
	}
	switch args[0] {
	case "status":
		cliStatus(args[1:])
	case "task":
		if len(args) < 2 || args[1] != "add" {
			fmt.Fprintln(os.Stderr, "usage: swarmops task add <session> <title> [description]")
			os.Exit(1)
		}
		cliTaskAdd(args[2:])
	case "inject":
		cliInject(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", args[0])
		cliUsage()
		os.Exit(1)
	}
}

func cliUsage() {
	fmt.Fprintln(os.Stderr, `swarmops subcommands:
  status [<session>]                    list sessions / agents / tasks
  task add <session> <title> [desc]     add a task to a session
  inject <session> <agent> <message>    inject a message into a live agent`)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func cliBase() string {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	return "http://localhost:" + port
}

func cliGet(path string) ([]byte, error) {
	resp, err := http.Get(cliBase() + path)
	if err != nil {
		return nil, fmt.Errorf("server unreachable: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return b, nil
}

func cliPost(path string, body interface{}) ([]byte, int, error) {
	payload, _ := json.Marshal(body)
	resp, err := http.Post(cliBase()+path, "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, 0, fmt.Errorf("server unreachable: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b, resp.StatusCode, nil
}

// resolveSession returns the session ID for a name-or-ID prefix.
// Exact ID match takes priority, then name match.
func resolveSession(nameOrID string) (id, name string, err error) {
	b, err := cliGet("/api/swarm/dashboard")
	if err != nil {
		return "", "", err
	}
	var dash struct {
		Sessions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"sessions"`
	}
	if json.Unmarshal(b, &dash) != nil {
		return "", "", fmt.Errorf("parse error from dashboard")
	}
	// Exact ID match
	for _, s := range dash.Sessions {
		if s.ID == nameOrID {
			return s.ID, s.Name, nil
		}
	}
	// Prefix/name match (case-insensitive)
	lower := strings.ToLower(nameOrID)
	var matched []struct{ id, name string }
	for _, s := range dash.Sessions {
		if strings.HasPrefix(strings.ToLower(s.ID), lower) || strings.Contains(strings.ToLower(s.Name), lower) {
			matched = append(matched, struct{ id, name string }{s.ID, s.Name})
		}
	}
	if len(matched) == 1 {
		return matched[0].id, matched[0].name, nil
	}
	if len(matched) > 1 {
		names := make([]string, len(matched))
		for i, m := range matched {
			names[i] = m.name + " (" + m.id[:8] + ")"
		}
		return "", "", fmt.Errorf("ambiguous session %q: %s", nameOrID, strings.Join(names, ", "))
	}
	return "", "", fmt.Errorf("no session matching %q", nameOrID)
}

// resolveAgent returns the agent ID for a name-or-ID prefix within a session.
func resolveAgent(sessionID, nameOrID string) (agentID, agentName string, err error) {
	b, err := cliGet("/api/swarm/sessions/" + sessionID)
	if err != nil {
		return "", "", err
	}
	var state struct {
		Agents []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"agents"`
	}
	if json.Unmarshal(b, &state) != nil {
		return "", "", fmt.Errorf("parse error from session")
	}
	lower := strings.ToLower(nameOrID)
	var matched []struct{ id, name string }
	for _, a := range state.Agents {
		if a.ID == nameOrID || strings.HasPrefix(strings.ToLower(a.ID), lower) || strings.Contains(strings.ToLower(a.Name), lower) {
			matched = append(matched, struct{ id, name string }{a.ID, a.Name})
		}
	}
	if len(matched) == 1 {
		return matched[0].id, matched[0].name, nil
	}
	if len(matched) > 1 {
		names := make([]string, len(matched))
		for i, m := range matched {
			names[i] = m.name
		}
		return "", "", fmt.Errorf("ambiguous agent %q: %s", nameOrID, strings.Join(names, ", "))
	}
	return "", "", fmt.Errorf("no agent matching %q in session", nameOrID)
}

// ─── status ───────────────────────────────────────────────────────────────────

func cliStatus(args []string) {
	b, err := cliGet("/api/swarm/dashboard")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var dash struct {
		Sessions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(b, &dash); err != nil {
		fmt.Fprintln(os.Stderr, "parse error:", err)
		os.Exit(1)
	}

	// Filter by session name/id if argument given
	filter := ""
	if len(args) > 0 {
		filter = strings.ToLower(args[0])
	}

	for _, sess := range dash.Sessions {
		if filter != "" && !strings.Contains(strings.ToLower(sess.Name), filter) &&
			!strings.HasPrefix(strings.ToLower(sess.ID), filter) {
			continue
		}
		b2, err := cliGet("/api/swarm/sessions/" + sess.ID)
		if err != nil {
			fmt.Printf("  %-30s  %s\n", sess.Name, "(unreachable)")
			continue
		}
		var state struct {
			Agents []struct {
				ID          string  `json:"id"`
				Name        string  `json:"name"`
				Role        string  `json:"role"`
				Status      string  `json:"status"`
				TmuxSession *string `json:"tmux_session"`
			} `json:"agents"`
			Tasks []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				Stage string `json:"stage"`
			} `json:"tasks"`
		}
		json.Unmarshal(b2, &state) //nolint:errcheck

		live := 0
		for _, a := range state.Agents {
			if a.TmuxSession != nil {
				live++
			}
		}
		fmt.Printf("\n⬡ %s  (%s…)\n", sess.Name, sess.ID[:8])
		fmt.Printf("  %d agent(s)  %d live  ·  %d task(s)\n", len(state.Agents), live, len(state.Tasks))

		for _, a := range state.Agents {
			onl := " "
			if a.TmuxSession != nil {
				onl = "●"
			}
			fmt.Printf("  %s %-20s  %-14s  %s\n", onl, a.Name, a.Status, a.Role)
		}

		stageCounts := make(map[string]int)
		for _, t := range state.Tasks {
			stageCounts[t.Stage]++
		}
		if len(stageCounts) > 0 {
			var parts []string
			for stage, n := range stageCounts {
				parts = append(parts, fmt.Sprintf("%s:%d", stage, n))
			}
			fmt.Printf("  tasks: %s\n", strings.Join(parts, "  "))
		}
	}
}

// ─── task add ─────────────────────────────────────────────────────────────────

func cliTaskAdd(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: swarmops task add <session> <title> [description]")
		os.Exit(1)
	}
	sessionRef, title := args[0], args[1]
	desc := ""
	if len(args) > 2 {
		desc = strings.Join(args[2:], " ")
	}

	sid, sname, err := resolveSession(sessionRef)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	payload := map[string]string{"title": title, "description": desc}
	b, status, err := cliPost("/api/swarm/sessions/"+sid+"/tasks", payload)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if status >= 400 {
		fmt.Fprintf(os.Stderr, "error (HTTP %d): %s\n", status, strings.TrimSpace(string(b)))
		os.Exit(1)
	}

	var task struct {
		ID    string `json:"id"`
		Stage string `json:"stage"`
	}
	json.Unmarshal(b, &task) //nolint:errcheck
	fmt.Printf("✓ task created  session=%s  id=%s  title=%q  stage=%s\n",
		sname, task.ID[:8], title, task.Stage)
}

// ─── inject ───────────────────────────────────────────────────────────────────

func cliInject(args []string) {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: swarmops inject <session> <agent> <message>")
		os.Exit(1)
	}
	sessionRef, agentRef := args[0], args[1]
	message := strings.Join(args[2:], " ")

	sid, sname, err := resolveSession(sessionRef)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	aid, aname, err := resolveAgent(sid, agentRef)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	// Reuse the existing inject endpoint
	payload := map[string]string{"text": message}
	b, status, err := cliPost("/api/swarm/sessions/"+sid+"/agents/"+aid+"/inject", payload)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if status >= 400 {
		fmt.Fprintf(os.Stderr, "error (HTTP %d): %s\n", status, strings.TrimSpace(string(b)))
		os.Exit(1)
	}
	_ = time.Now() // keep import
	fmt.Printf("✓ injected into %s / %s\n", sname, aname)
}
