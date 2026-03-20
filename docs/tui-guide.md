# SwarmOps TUI Guide

> Complete reference for interacting with the SwarmOps terminal interface to orchestrate AI agent swarms.

## Overview

SwarmOps is a multi-agent orchestration system that runs Claude Code instances as parallel workers inside tmux sessions. The TUI (terminal user interface) is the primary control plane: you create sessions, spawn agents, assign tasks, monitor progress, handle escalations, and steer goals — all without leaving the terminal.

Run the TUI:

```
swarmops tui
# or via systemd on NUC:
ssh nuc-ubuntu-dev "TERM=xterm-256color swarmops tui"
```

---

## Layout

```
┌─────────────────────────────────────────────────────┐
│  ⬡ SwarmOps │ 2 sessions  │ 3/5 tasks  ██░░ 60%    │  ← HUD
├──────────────────┬──────────────────────────────────┤
│ Sidebar          │ Detail Panel                      │
│  (sessions,      │  (session / agent / task info)    │
│   agents,        │  ──────────────────────────────── │
│   tasks)         │  Event Viewport                   │
│                  │  (recent events / terminal view)  │
├──────────────────┴──────────────────────────────────┤
│  Session Name  ·  3 agents (2 live)  ·  123k tok    │  ← Status bar
├─────────────────────────────────────────────────────┤
│ Message to orchestrator… /goal <desc>               │  ← Chat input
├─────────────────────────────────────────────────────┤
│  ↑↓/jk nav · Enter attach · Tab// input · s spawn  │  ← Help hint
└─────────────────────────────────────────────────────┘
```

**Left sidebar** — flat list of sessions (bold), their agents (4-row sprite cards), and tasks (single rows with stage badge). The cursor selects which item the detail panel shows.

**Right detail panel** — context-sensitive: session summary, agent portrait (braille sprite + status), or task metadata (stage, CI status, PR link, confidence score, blocked reason).

**Event viewport** — below the detail panel: scrolling list of swarm events for the selected session. When an agent is selected and has a live tmux session, shows a live capture of their terminal instead.

**Status bar** — one-line summary of the selected item (tokens, cost, context %, current file).

**Chat input** — sends messages to the session orchestrator, or creates goals with `/goal`.

**Help hint** — always-visible one-line key reference; hold `?` for the full help screen.

---

## Key Bindings

### Navigation

| Key | Action |
|-----|--------|
| `↑` / `k` | Move cursor up in sidebar |
| `↓` / `j` | Move cursor down in sidebar |
| `Enter` | Attach to the selected agent's tmux session |
| `Tab` or `/` | Focus the chat input bar |
| `Esc` | Unfocus chat input / cancel pending action |
| `q` or `Ctrl+C` | Quit SwarmOps |

### Session Actions

| Key | Action |
|-----|--------|
| `c` | Create new session (opens name modal) |
| `r` | Resume selected session (re-runs SiBot briefing) |
| `A` | Toggle autopilot for selected session (Plane issue sync on/off) |
| `X` `X` | Delete selected session (press twice to confirm) |
| `E` | Edit selected session name |

### Agent Actions

| Key | Action |
|-----|--------|
| `s` | Spawn selected agent (start its tmux+Claude Code process) |
| `d` `d` | Stop (despawn) selected agent (press twice to confirm) |
| `n` | New agent (full form: name, role, mission, project, repo path) |
| `+` | Quick-spawn a worker (name only, role defaults to `worker`) |
| `E` | Edit selected agent (name, mission, project, repo path) |
| `P` | Edit role prompt for the selected agent's role in `$EDITOR` |

### Task Actions

| Key | Action |
|-----|--------|
| `t` | New task (title, description, project) |
| `E` | Edit selected task (title, description, project) |

### View Switching

| Key | View |
|-----|------|
| `e` | Escalations — pending human-in-the-loop requests |
| `g` | Goals — goal status, budget, linked tasks |
| `W` | Work queue — Plane backlog for selected session |
| `L` | Event log — full retrospective with agent filter |
| `I` | Icinga monitor — infrastructure service status |
| `R` | Refresh all data from server |
| `?` | Help overlay (hold to keep open, release to dismiss) |

---

## Working with Sessions

A **session** is the top-level container for a swarm. It holds agents, tasks, goals, events, and escalations. Sessions persist across restarts.

### Create a session

Press `c`. Enter a name in the modal, press Enter.

The session appears in the sidebar with no agents yet. The detail panel shows:
```
⬡ My Feature
  0 agents (0 live)  ·  0 tasks
  No active goals — /goal <desc> to set one
  n=agent  t=task  r=resume  e=escalations  R=refresh
```

### Set a goal for a session

Focus the chat input (`Tab` or `/`), type:

```
/goal Implement OAuth2 login flow with Plane integration
```

Press Enter. The goal is created and shown in the session detail and the Goals screen (`g`).

Goals are the high-level objectives SiBot (the orchestrator agent) decomposes into tasks. If autopilot is enabled, goals are also created automatically from Plane issues.

### Send a message to the orchestrator

Focus chat input, type any message that doesn't start with `/goal`, press Enter:

```
The auth PR is failing CI — please check and brief the QA agent
```

The message is sent to the session's orchestrator agent's tmux session as an injected prompt. A local echo appears in the event viewport immediately.

### Enable autopilot (Plane sync)

Press `A` with the session selected. Autopilot polls the linked Plane project and creates goals from backlog/unstarted issues automatically. Press `A` again to disable.

The status bar shows `⚙ auto` when autopilot is active.

### Delete a session

Select a session row, press `X`. A confirmation flash appears. Press `X` again within a few seconds to confirm. All agents, tasks, goals, events, and notes under the session are cascade-deleted.

---

## Working with Agents

An **agent** is a Claude Code instance running inside a tmux session, with a specific role and mission. Agents read a `CLAUDE.md` briefing file and a `SWARM_CONTEXT.md` that gives them API access to coordinate with SiBot.

### Agent roles

| Role | Purpose |
|------|---------|
| `orchestrator` | SiBot — coordinates the swarm, injects briefs, manages task board |
| `senior-dev` | Implements features, refactors, code reviews |
| `qa-agent` | Writes tests, runs suites, reports failures |
| `devops-agent` | CI/CD, Docker, deployments, infrastructure |
| `researcher` | Specs, investigation, documentation |
| `worker` | General purpose (default) |

### Spawn types

Three spawn types are determined by the agent's role and repo path:

| Type | When | Working dir |
|------|------|-------------|
| **worktree** | `repo_path` set + git repo | New git worktree at `<repo>/.worktrees/<agentID>` on a fresh branch |
| **sibot scratch** | `role=orchestrator` | `~/.swarmops/sibot/<agentID>/` |
| **scratch** | No repo path | `~/.swarmops/agents/<agentID>/` |

### Creating an agent (full form)

Press `n` with a session selected. Fields:

| Field | Required | Description |
|-------|----------|-------------|
| Name | Yes | Display name (e.g. `Alice`) |
| Role | No | Default `worker`; set to a role from the table above |
| Mission | No | One-line objective shown in sidebar and passed in briefing |
| Project | No | Plane project identifier for task tracking |
| Repo path | No | Absolute path to git repo; triggers worktree spawn type |

Press Enter to advance through fields. Final Enter submits.

### Quick-spawn a worker

Press `+` with a session selected. Enter a name only. The agent is created with role `worker` and no mission or repo path.

### Spawn an agent process

After creating an agent record, you must start it. Select the agent row and press `s`. This:

1. Creates a git worktree (if repo path set) or scratch directory
2. Writes `CLAUDE.md` and `SWARM_CONTEXT.md` briefing files
3. Starts a tmux session named `sw-<agentID[:8]>`
4. Launches Claude Code inside that session with the appropriate prompt

The agent status transitions from offline (faint name) to `thinking` or `coding`.

### Attach to an agent

Select an agent row and press `Enter`. If you're already inside tmux, SwarmOps issues `tmux switch-client`; otherwise `tmux attach-session`. The TUI suspends and you see the agent's live Claude Code session. Detach with `Ctrl+B D` to return.

### Stop an agent

Select an agent row and press `d`. A confirmation flash appears. Press `d` again to despawn. The tmux session is killed, the agent's status returns to offline.

### Edit an agent

Press `E` with an agent selected. You can update name, mission, project, and repo path. The agent must be respawned for changes to take effect.

### Customize a role prompt

Press `P` with any agent selected. SwarmOps fetches the current role prompt for that agent's role, writes it to a temp file, and opens it in `$EDITOR`. Save and close to persist. All future spawns of that role use the updated prompt.

### Reading the agent card

The sidebar shows a 4-line sprite card per agent:

```
🤖🤖  Alice
🤖🤖  ⣾ coding
🤖🤖  Implement auth flow
🤖🤖  senior-dev
```

The detail panel shows a taller braille portrait plus:
- Status with animated spinner
- Current mission (italic)
- Current file being edited
- Linked task title + stage
- Latest note (yellow, from agent's internal notes API)
- tmux session name with `[Enter]` hint
- Context usage bar (green → yellow → red at 70%/85%/95%)
- Model name and token count

Status values and their meanings:

| Status | Meaning |
|--------|---------|
| `thinking` | Claude is reasoning, no file edits yet |
| `coding` | Active file edits in progress |
| `waiting` | No output change for >2 minutes (possible stuck) |
| `stuck` | Watchdog confirmed stuck — escalation sent |
| `done` | Completed; offline |

---

## Working with Tasks

A **task** represents a unit of work tracked on the session's task board. Tasks can be linked to goals, assigned to agents, and have CI/PR metadata attached automatically.

### Create a task

Press `t` with a session selected. Fields:

| Field | Required | Description |
|-------|----------|-------------|
| Title | Yes | Short task name |
| Description | No | Detailed spec |
| Project | No | Plane project identifier |

### Task stages

Tasks progress through these stages (SiBot or agents update them via API):

| Stage | Meaning |
|-------|---------|
| `spec` | Being defined (default) |
| `implement` | In development |
| `test` | Under QA |
| `deploy` | Being deployed |
| `done` | Complete |
| `blocked` | Stuck — reason shown in detail panel |
| `needs_review` | Awaiting human code review |
| `needs_human` | Requires human decision |
| `failed` | Failed; terminal state |
| `timed_out` | Watchdog killed it |

Stage is shown as a badge in the sidebar (`[impl  ]`, `[test  ]`, etc.) and as a colored label in the detail panel.

### Task detail panel

When a task is selected, the right panel shows:

- Stage (color-coded)
- Task ID prefix
- Phase and phase order (for 8-phase Talos tasks: `spec`, `plan`, `plan_review`, `implement`, `impl_review`, `judge`, `deploy`, `document`)
- CI status with icon (✅ success, ❌ failed, ⏳ in progress)
- PR URL
- Confidence score (green ≥80%, yellow ≥60%, red <60%)
- Token spend
- Blocked reason (red)

### CI integration

When an agent creates a PR, the CI status is tracked automatically via `swarm_ci.go`. The sidebar task row shows a dot indicator:

- `●` green — CI passing
- `●` red — CI failing
- `○` dim — No CI or unknown

---

## Escalations Screen (`e`)

Escalations are human-in-the-loop requests raised by agents when they're blocked or need a decision.

Press `e` with a session selected to open the escalations screen.

**Navigation:** `↑`/`↓` or `j`/`k` to move between escalations. Press `Enter` on one to open a text input and type your response. Press `Enter` to send. The agent receives the response as an injected message.

Press `e`, `Esc`, or `q` to close.

Escalations also trigger Telegram notifications via `swarm_notify.go` when they're raised.

---

## Goals Screen (`g`)

Shows all goals for the selected session with their status, budget, and linked tasks.

Press `g` with a session selected.

**Each row shows:**
- Status icon: `▶` active, `✓` complete, `✗` failed, `○` cancelled
- Description (truncated to terminal width)
- Complexity badge: `(trv)` trivial, `(cpx)` complex
- Task progress: `3/7 tasks`
- Token budget bar: `[████░░░░ 55%]` (green → orange → red at 80%/100%)

**Navigation:** `↑`/`↓` or `j`/`k`. Selecting a goal expands it to show linked task rows below with their phase and stage.

**Add a goal:** Use `/goal <description>` in the chat input from the main screen.

**Close:** `g`, `Esc`, or `q`.

---

## Work Queue Screen (`W`)

Shows the Plane backlog for the selected session's linked project.

Press `W` with a session selected. SwarmOps fetches backlog and unstarted issues from Plane. Navigate with `j`/`k`, press `q` or `Esc` to close.

With autopilot enabled (`A`), issues from this queue become goals automatically. You can also manually create a goal from a queue item by noting its title and using `/goal` in the chat input.

---

## Event Log Screen (`L`)

Full event history for the selected session.

Press `L` with a session selected.

**Navigation:**

| Key | Action |
|-----|--------|
| `↑`/`k`, `↓`/`j` | Move through events |
| `g` | Jump to first event |
| `G` | Jump to last event |
| `f` | Filter events to the agent under cursor |
| `F` | Clear filter, show all agents |
| `Enter` | Open event detail view (full payload) |
| `q` / `Esc` | Close event log |

**Event types and colours:**

| Color | Event types |
|-------|-------------|
| Green | `agent_spawned`, `agent_started` |
| Teal | `agent_message`, `inject` |
| Blue | `task_created`, `task_moved` |
| Red | `agent_stuck`, `watchdog_timeout` |
| Dim | All others |

**Event detail** (press `Enter`): full-screen view of the raw event payload. Press `Esc` or `q` to return to the log.

---

## Icinga Monitor (`I`)

Live view of infrastructure service status from Icinga2.

Press `I` from anywhere in the main screen.

**Layout:** two panes — top lists services (host/service/status/output), bottom lists recent alerts and state transitions.

**Navigation:**

| Key | Action |
|-----|--------|
| `Tab` | Switch focus between top and bottom pane |
| `↑`/`k`, `↓`/`j` | Move within focused pane |
| `r` / `R` | Refresh from Icinga API |
| `g` | Jump to top of focused pane |
| `q` / `Esc` | Close Icinga monitor |

Status colours: green (OK), yellow (WARNING), red (CRITICAL), dim (UNKNOWN).

---

## Common Workflows

### Start a new swarm session for a feature

1. Press `c`, name the session (e.g. `auth-oauth`)
2. Press `n` to create an orchestrator agent: name `SiBot`, role `orchestrator`
3. Press `s` to spawn SiBot — it starts in `~/.swarmops/sibot/<id>/`
4. Press `n` to create a worker: name `Alice`, role `senior-dev`, repo path `/home/user/git/myapp`
5. Press `s` to spawn Alice — creates a git worktree at `myapp/.worktrees/sw-<id>/`
6. Focus chat (`Tab`), type `/goal Implement OAuth2 login with session persistence`, press Enter
7. SiBot receives a heartbeat, reads the goal, and injects briefs to Alice

### Monitor a running swarm

1. Navigate the sidebar with `↑`/`↓` — agents show real-time status spinners
2. Select an agent to see live terminal capture in the viewport
3. Press `Enter` to attach directly to their tmux session
4. Press `g` to check goal progress and budget burn
5. Press `L` to review the event timeline
6. Press `e` to handle any pending escalations

### Respond to an escalation

1. Press `e` — see list of pending escalations with their questions
2. Navigate to the one you want to answer, press `Enter`
3. Type your response in the input field, press `Enter` to send
4. The agent receives the response as an injected message and continues

### Edit a role prompt

1. Select any agent with the role you want to edit (e.g. a `senior-dev`)
2. Press `P` — SwarmOps opens the current prompt in `$EDITOR`
3. Edit and save
4. Flash message confirms: `Role prompt updated: senior-dev`
5. Next spawn of a `senior-dev` uses the new prompt

### Unblock a stuck agent

1. Select the stuck agent (status: `stuck`, status bar shows in red)
2. Press `Enter` to attach to their tmux session
3. Read the error or blocker, type a message, detach with `Ctrl+B D`
4. Or press `Tab`, type a message in chat — it's sent to the session orchestrator who can re-brief

### Resume an interrupted session

Select the session row, press `r`. SwarmOps posts to the resume endpoint which triggers SiBot to re-read its state and re-brief all live agents.

---

## Chat Input Reference

| Input | Action |
|-------|--------|
| `/goal <description>` | Create a new goal for the selected session |
| Any other text | Send as message to the session's orchestrator agent |

Focus chat with `Tab` or `/`. Send with `Enter`. `Esc` unfocuses without sending.

---

## HUD Reference

The top bar shows cross-session summary:

```
⬡ SwarmOps │ 2 sessions │ ██░░░░ 3/8 tasks 37% │ 2 coding · 1 thinking
```

The health percentage is `done / (total - blocked)` tasks. Color: red (<40%), teal (40–79%), green (≥80%).

Live agent counts (`coding`, `thinking`, `waiting`, `stuck`) update every 150ms via animation tick.
