package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// ─── Global task creation ─────────────────────────────────────────────────────

// handleGlobalTasksAPI handles:
//
//	POST /api/swarm/tasks — create a new task
func handleGlobalTasksAPI(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req CreateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if req.SenderID == "" {
		req.SenderID = "api"
	}
	if req.Type == "" {
		http.Error(w, `{"error":"type required"}`, http.StatusBadRequest)
		return
	}
	if req.Payload == "" {
		req.Payload = "{}"
	}

	task, err := createTask(ctx, req)
	if errors.Is(err, ErrTaskDuplicate) {
		http.Error(w, `{"error":"task with this external_id already exists"}`, http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(task) //nolint:errcheck
}

// ─── Session-scoped task endpoints ───────────────────────────────────────────

// handleSessionTasksAPI routes /api/swarm/sessions/:id/tasks/...
//
//	GET    /tasks/inbox          — pending tasks for this session
//	GET    /tasks/:tid           — single task (must belong to session)
//	GET    /tasks/:tid/events    — audit trail for task
//	POST   /tasks/:tid/accept
//	POST   /tasks/:tid/reject    body: {"reason":"..."}
//	POST   /tasks/:tid/defer     body: {"defer_until":"RFC3339"}
//	POST   /tasks/:tid/complete  body: {"result":"..."}
//	POST   /tasks/:tid/fail      body: {"reason":"..."}
func handleSessionTasksAPI(w http.ResponseWriter, r *http.Request, ctx context.Context, sessionID string, subPath []string) {
	w.Header().Set("Content-Type", "application/json")

	// GET /tasks/inbox
	if len(subPath) == 0 || (len(subPath) == 1 && subPath[0] == "inbox") {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		tasks, err := getTaskInbox(ctx, sessionID, limit)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		if tasks == nil {
			tasks = []Task{}
		}
		json.NewEncoder(w).Encode(tasks) //nolint:errcheck
		return
	}

	taskID := subPath[0]
	action := ""
	if len(subPath) >= 2 {
		action = subPath[1]
	}

	// GET /tasks/:tid or GET /tasks/:tid/events
	if r.Method == http.MethodGet {
		if action == "events" {
			events, err := listTaskEvents(ctx, taskID)
			if errors.Is(err, ErrTaskNotFound) {
				http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
				return
			}
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
				return
			}
			if events == nil {
				events = []TaskEvent{}
			}
			json.NewEncoder(w).Encode(events) //nolint:errcheck
			return
		}
		// GET /tasks/:tid
		task, err := getTask(ctx, taskID)
		if errors.Is(err, ErrTaskNotFound) {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		// Enforce session boundary
		if task.SessionID == nil || *task.SessionID != sessionID {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(task) //nolint:errcheck
		return
	}

	// POST mutations require an action suffix
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Extract optional caller identity from X-Agent-ID header (fallback to "api")
	agentID := r.Header.Get("X-Agent-ID")
	if agentID == "" {
		agentID = "api"
	}

	var body struct {
		Reason     string `json:"reason"`
		Result     string `json:"result"`
		DeferUntil string `json:"defer_until"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	var err error
	switch action {
	case "accept":
		err = acceptTask(ctx, taskID, sessionID, agentID)
	case "reject":
		err = rejectTask(ctx, taskID, sessionID, agentID, body.Reason)
	case "defer":
		if body.DeferUntil == "" {
			http.Error(w, `{"error":"defer_until required"}`, http.StatusBadRequest)
			return
		}
		until, parseErr := time.Parse(time.RFC3339, body.DeferUntil)
		if parseErr != nil {
			http.Error(w, `{"error":"defer_until must be RFC3339"}`, http.StatusBadRequest)
			return
		}
		err = deferTask(ctx, taskID, sessionID, agentID, until)
	case "complete":
		result := body.Result
		if result == "" {
			result = body.Reason
		}
		err = completeTask(ctx, taskID, sessionID, agentID, result)
	case "fail":
		err = failTask(ctx, taskID, sessionID, agentID, body.Reason)
	default:
		http.Error(w, `{"error":"unknown task action"}`, http.StatusNotFound)
		return
	}

	if errors.Is(err, ErrTaskNotFound) {
		http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
		return
	}
	if errors.Is(err, ErrTaskConflict) {
		http.Error(w, `{"error":"task state conflict"}`, http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}

	// Return updated task
	task, err := getTask(ctx, taskID)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	json.NewEncoder(w).Encode(task) //nolint:errcheck
}
