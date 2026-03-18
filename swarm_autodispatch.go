package main

import (
	"context"
	"fmt"
	"log"
	"time"
)

// autoDispatchQueuedTasks pairs queued tasks with idle worker agents and
// injects a task brief directly — without waiting for the SiBot 5-minute
// heartbeat cycle. Safe to call from goroutines; takes a short-lived context.
//
// Rules:
//   - Only dispatches to non-orchestrator agents that are live (tmux_session IS NOT NULL)
//     and have no task currently in assigned/accepted/running stage.
//   - Does not touch tasks that already have an agent_id set.
//   - One task per idle agent per call; subsequent calls handle remaining tasks.
//   - Idempotent: safe to call repeatedly.
func autoDispatchQueuedTasks(ctx context.Context, sessionID string) {
	// Collect queued tasks with no assigned agent, ordered by creation time.
	rows, err := database.QueryContext(ctx,
		`SELECT id, title, COALESCE(description,''), COALESCE(goal_id,'')
		 FROM swarm_tasks
		 WHERE session_id=? AND stage='queued' AND agent_id IS NULL
		 ORDER BY created_at ASC`,
		sessionID,
	)
	if err != nil {
		log.Printf("swarm/dispatch: query queued tasks for session %s: %v", sessionID[:8], err)
		return
	}
	defer rows.Close()

	type queuedTask struct{ id, title, desc, goalID string }
	var queued []queuedTask
	for rows.Next() {
		var t queuedTask
		if err := rows.Scan(&t.id, &t.title, &t.desc, &t.goalID); err != nil {
			log.Printf("swarm/dispatch: scan task row: %v", err)
			break
		}
		queued = append(queued, t)
	}
	if err := rows.Err(); err != nil {
		log.Printf("swarm/dispatch: task rows error: %v", err)
	}

	if len(queued) == 0 {
		return
	}

	// Collect idle worker agents: live, non-orchestrator, no active task.
	agentRows, err := database.QueryContext(ctx,
		`SELECT a.id, a.name, a.role, a.tmux_session
		 FROM swarm_agents a
		 WHERE a.session_id=?
		   AND a.role != 'orchestrator'
		   AND a.tmux_session IS NOT NULL
		   AND NOT EXISTS (
		       SELECT 1 FROM swarm_tasks t
		       WHERE t.agent_id = a.id
		         AND t.stage IN ('assigned','accepted','running')
		   )
		 ORDER BY a.created_at ASC`,
		sessionID,
	)
	if err != nil {
		log.Printf("swarm/dispatch: query idle agents for session %s: %v", sessionID[:8], err)
		return
	}
	defer agentRows.Close()

	type idleAgent struct{ id, name, role, tmuxSession string }
	var idle []idleAgent
	for agentRows.Next() {
		var a idleAgent
		if err := agentRows.Scan(&a.id, &a.name, &a.role, &a.tmuxSession); err != nil {
			log.Printf("swarm/dispatch: scan agent row: %v", err)
			break
		}
		idle = append(idle, a)
	}
	if err := agentRows.Err(); err != nil {
		log.Printf("swarm/dispatch: agent rows error: %v", err)
	}

	if len(idle) == 0 {
		return
	}

	// Pair tasks to agents (one-to-one, FIFO).
	pairs := min(len(queued), len(idle))
	for i := 0; i < pairs; i++ {
		dispatchTaskToAgent(ctx, sessionID, queued[i].id, queued[i].title, queued[i].desc, idle[i].id, idle[i].name)
	}
}

func dispatchTaskToAgent(ctx context.Context, sessionID, taskID, title, desc, agentID, agentName string) {
	now := time.Now().Unix()

	// Atomic claim: only succeeds if task is still queued and unassigned.
	res, err := database.ExecContext(ctx,
		`UPDATE swarm_tasks SET agent_id=?, stage='assigned', updated_at=? WHERE id=? AND stage='queued' AND agent_id IS NULL`,
		agentID, now, taskID,
	)
	if err != nil {
		log.Printf("swarm/dispatch: claim task %s→agent %s: %v", shortDispatchID(taskID), shortDispatchID(agentID), err)
		return
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		// Another dispatcher won the race — normal, not an error.
		return
	}

	writeSwarmEvent(ctx, sessionID, agentID, taskID, "task_assigned", title)

	// Build and inject task brief.
	brief := buildTaskBrief(sessionID, taskID, title, desc)
	if err := injectToSwarmAgent(ctx, agentID, brief); err != nil {
		log.Printf("swarm/dispatch: inject to %s (%s) failed: %v — reverting to queued", agentName, shortDispatchID(agentID), err)
		// Revert so the task isn't stuck in assigned with no agent knowing about it.
		database.ExecContext(ctx, //nolint:errcheck
			`UPDATE swarm_tasks SET agent_id=NULL, stage='queued', updated_at=? WHERE id=? AND stage='assigned' AND agent_id=?`,
			time.Now().Unix(), taskID, agentID,
		)
		writeSwarmEvent(ctx, sessionID, agentID, taskID, "task_dispatch_failed", "injection failed — reverted to queued")
		return
	}

	log.Printf("swarm/dispatch: dispatched task %s (%s) → agent %s (%s)",
		shortDispatchID(taskID), title, shortDispatchID(agentID), agentName)
	swarmBroadcaster.schedule(sessionID)
}

func buildTaskBrief(sessionID, taskID, title, desc string) string {
	apiBase := swarmAPIBase()
	brief := fmt.Sprintf("## Task Assignment\n\nYou have been assigned a new task from the swarm queue.\n\n**Task:** %s\n**Task ID:** `%s`\n\n", title, taskID)
	if desc != "" {
		brief += fmt.Sprintf("**Description:**\n%s\n\n", desc)
	}
	brief += fmt.Sprintf(`**Your workflow:**

1. Accept the task:
   POST %s/api/swarm/sessions/%s/tasks/%s/accept
   Body: {"message_id": ""}

2. Start work:
   POST %s/api/swarm/sessions/%s/tasks/%s/start

3. Do the work. Update your agent status via IPC outbox or the API.

4. When complete, submit a handoff:
   POST %s/api/swarm/sessions/%s/tasks/%s/handoff
   Body: {"confidence": 0.9, "tests_passed": true, "summary": "..."}

GET %s/api/swarm/sessions/%s for full session context.
Proceed now.`,
		apiBase, sessionID, taskID,
		apiBase, sessionID, taskID,
		apiBase, sessionID, taskID,
		apiBase, sessionID,
	)
	return brief
}

// startAutoDispatchLoop runs a background ticker that dispatches any queued
// tasks that may have been missed (e.g. agent was offline when task was created).
func startAutoDispatchLoop(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepAutoDispatch(ctx)
			}
		}
	}()
}

func sweepAutoDispatch(ctx context.Context) {
	rows, err := database.QueryContext(ctx,
		"SELECT DISTINCT session_id FROM swarm_tasks WHERE stage='queued' AND agent_id IS NULL")
	if err != nil {
		log.Printf("swarm/dispatch: sweep query: %v", err)
		return
	}
	defer rows.Close()

	var sessions []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			log.Printf("swarm/dispatch: sweep scan: %v", err)
			break
		}
		sessions = append(sessions, sid)
	}

	for _, sid := range sessions {
		autoDispatchQueuedTasks(ctx, sid)
	}
}

func shortDispatchID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
