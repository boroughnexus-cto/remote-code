# SwarmOps TUI — Gap & Deficiency Assessment

> Analysis of features present in the API/backend but missing or incomplete in the TUI, plus usability issues in existing screens.
> Last updated: peer-reviewed by GPT-5.2, Gemini 3.1 Pro, and Claude Opus 4.5.

---

## Summary

The TUI covers the core orchestration loop well: create sessions, spawn agents, monitor status, handle escalations, manage goals, and attach to live sessions. However, several API capabilities have no TUI surface at all, and a few existing screens have usability rough edges. This document catalogues them in priority order.

Peer review (GPT-5.2, Gemini 3.1 Pro, Claude Opus 4.5) identified additional critical issues beyond the initial analysis — primarily around operator mental model, multi-session scaling, error diagnosis without tmux attach, and the dangerous combination of 20+ keybindings with a chat input in the same screen. These are integrated below.

---

## Missing Features (API exists, no TUI surface)

### 1. Direct agent injection (HIGH)

**API:** `POST /api/swarm/sessions/{sid}/agents/{aid}/inject`
**What it does:** Sends a text message directly into a specific agent's Claude Code session — bypassing the orchestrator.

**Gap:** The TUI chat input sends messages to the orchestrator's session only (via `/api/swarm/sessions/{sid}/orchestrator/message`). There is no way to inject a brief to a specific worker agent from the TUI without attaching to their tmux session.

**Impact:** When a worker is stuck or needs a course-correction, the user must either: (a) attach with Enter and type manually, or (b) wait for SiBot to re-brief them. Neither is fast. A common workflow — "tell Alice specifically to stop and focus on X" — requires leaving the TUI.

**Suggested fix:** When the chat input is focused and a specific agent is selected, offer a `/inject` prefix (e.g. `/inject Alice focus on writing tests for auth.go`) that routes to that agent rather than the orchestrator. Alternatively, add a new key binding (e.g. `i`) to open a single-field inject modal for the selected agent.

---

### 2. Write a note to an agent (MEDIUM)

**API:** `POST /api/swarm/sessions/{sid}/agents/{aid}/note`
**What it does:** Adds a persistent note to an agent's record (shown as `latest_note` in the TUI's agent card in yellow). Notes are written by the agent itself via the IPC API — but the API also accepts `created_by: "user"`, allowing human-authored notes.

**Gap:** Notes are agent-written only from the TUI's perspective. There's no way for the operator to annotate an agent with a context note (e.g. "waiting on external API key" or "deprioritised until Alice merges PR").

**Suggested fix:** Add a `N` key binding on an agent that opens a single-field modal for a note. These notes persist across spawns and are visible in the agent card.

---

### 3. Delete an agent record (MEDIUM)

**API:** `DELETE /api/swarm/sessions/{sid}/agents/{aid}`
**What it does:** Removes the agent from the database (after despawning).

**Gap:** The `d d` key binding despawns an agent (kills tmux, marks offline) but does not delete the record. Deleted agents disappear from the sidebar; offline (stopped) agents remain and accumulate. There is no way to remove an agent record from the TUI.

**Impact:** Long-lived sessions with many ephemeral agents accumulate tombstone rows in the sidebar. The only cleanup is via `X X` which deletes the entire session.

**Suggested fix:** Add a `D` key binding (capital, to distinguish from `d` despawn) that deletes the selected agent record after a confirmation prompt — only allowed when the agent is offline.

---

### 4. Delete a task record (MEDIUM)

**API:** `DELETE /api/swarm/sessions/{sid}/tasks/{tid}`
**What it does:** Removes a task from the session.

**Gap:** Tasks can be created and edited, but cannot be deleted from the TUI. Cancelled or duplicated tasks accumulate in the sidebar and goals screen.

**Suggested fix:** Add `D` key binding on task items (same pattern as agent delete) with a confirm-twice guard.

---

### 5. Manual task stage transition (MEDIUM)

**API:** `PATCH /api/swarm/sessions/{sid}/tasks/{tid}` with `{"stage":"implement"}`
**What it does:** Moves a task to any stage.

**Gap:** The TUI shows task stages but provides no way to manually move them. Agents and SiBot update stages via the API, but if an agent crashes mid-task (leaving it stuck at `implement`) or a task needs to be marked `done` after manual intervention, the operator has no TUI path.

**Suggested fix:** In the Edit Task modal (`E` on a task), add a `Stage` field with the valid stage values. Or add a dedicated `S` key binding on task items that cycles or presents a stage picker.

---

### 6. Goal budget management (LOW–MEDIUM)

**API:** `POST /api/swarm/sessions/{sid}/goals` accepts `token_budget` field. Goals also have a `complexity` field.
**What it does:** Sets a token budget that is tracked and displayed as a progress bar in the goals screen.

**Gap:** The TUI's `/goal <description>` chat command creates a goal with only a description — no budget or complexity. The goals screen shows budgets but there is no way to set or edit them from the TUI.

**Suggested fix:** Add a goals management modal (accessible from the goals screen with `e` on a selected goal) to edit description, complexity, and token budget. Or extend the `/goal` command to support a richer syntax: `/goal <desc> [budget=50000] [complexity=complex]`.

---

### 7. Manual cleanup trigger (LOW)

**API:** `POST /api/swarm/cleanup`
**What it does:** Scans for orphaned tmux sessions and stale agent state, removes them from the database.

**Gap:** Cleanup runs automatically on a background timer, but there is no way to trigger it manually from the TUI when you know there are orphaned sessions (e.g. after a crash or server restart).

**Suggested fix:** Add a `C` key binding in the main screen, or include it under a "maintenance" sub-menu. Could also be surfaced as a chat command: `/cleanup`.

---

### 8. Voice features (LOW / optional)

**API:** `POST /api/swarm/transcribe`, `GET /api/swarm/tts`, `GET /api/swarm/voice`
**What it does:** Speech-to-text (Whisper) transcription, TTS audio output, and a combined voice interface.

**Gap:** No TUI surface. Voice is entirely unused unless called directly via browser or external client.

**Suggested fix:** Not a priority for a terminal tool. Could consider a `V` binding that records a voice note via `arecord` piped to `/transcribe` and injects the result as a chat message, but this is niche.

---

### 9. Triage screen (LOW)

**API / backend:** `swarm_triage.go` implements triage logic for blocked/stuck tasks.
**What it does:** Surfaces stuck agents and blocked tasks in priority order for operator review.

**Gap:** The escalations screen (`e`) handles explicit human-in-the-loop escalations, but there is no triage view showing all tasks in `blocked`, `needs_human`, or `needs_review` stages across all sessions.

**Suggested fix:** Add a `T` (capital, distinct from `t` new-task) key binding that opens a triage screen listing all sessions' blocked/stuck items in one place, with direct navigation to the relevant session.

---

## Usability Issues in Existing Screens

### 10. Goals screen read-only (MEDIUM)

The goals screen (`g`) shows goal status but provides no actions. You can navigate goals but can't mark one complete, cancel it, or reassign its budget. The only goal action available is creating new ones via chat.

**Suggested fix:** Add key bindings within the goals screen: `Enter` to expand/collapse (already done), `x` to cancel a goal, and `e` to edit description/budget.

---

### 11. Work queue: no goal creation shortcut (MEDIUM)

The work queue screen (`W`) shows Plane issues but provides no action beyond viewing. The intended workflow (promote an issue to a goal) requires the user to mentally note the title, press `q` to close, focus chat, and type `/goal <remembered title>`.

**Suggested fix:** Add `Enter` or `g` in the work queue screen to create a goal from the selected issue — pre-populating the description from the issue title.

---

### 12. Event log: no jump to agent (LOW)

In the event log (`L`), you can filter by agent (`f`) but cannot jump from an event to the agent in the sidebar. The event detail view shows the agent ID but not the name.

**Suggested fix:** In the event detail view, show the resolved agent name. Add a `j` binding to jump to the related agent in the sidebar and close the event log.

---

### 13. No cross-session navigation (LOW)

The sidebar shows all sessions and their children in one flat list. With many sessions, it's easy to lose context about which session you're acting on. There's no way to collapse a session to hide its agents/tasks, and no way to jump directly to a session by name.

**Suggested fix:** Add session-level collapse (toggle with `Enter` on session rows). Add a `/search` or `Ctrl+F` command to jump to a session or agent by name prefix.

---

### 14. Agent terminal capture is polling, not streaming (LOW)

The viewport shows a terminal capture for the selected agent, but it's fetched by polling every ~450ms (animation tick ÷ 3). This gives a slightly laggy feel compared to the WebSocket-backed event stream.

**Impact:** Minor; attaching directly with `Enter` gives a real-time view. The viewport is mainly useful for quick at-a-glance status without detaching.

**Suggested fix:** Extend the WebSocket push to include terminal content updates, eliminating the polling loop.

---

### 15. No task assignment in TUI (LOW)

Tasks can be created and stage-edited but cannot be assigned to a specific agent from the TUI. The `agent_id` field on tasks is managed by SiBot and agents via API, but there's no operator path to manually assign a task to an agent.

**Suggested fix:** In the Edit Task modal, add an `Agent` field with a picker (agent names for the current session).

---

## Feature Completeness Matrix

| Capability | API | TUI |
|-----------|-----|-----|
| Create/rename/delete session | ✅ | ✅ (no delete-without-losing-all) |
| Create/edit agent | ✅ | ✅ |
| Delete agent record | ✅ | ❌ |
| Spawn/despawn agent | ✅ | ✅ |
| Attach to agent tmux | — | ✅ |
| Inject to orchestrator | ✅ | ✅ (via chat) |
| Inject to specific agent | ✅ | ❌ |
| Write agent note | ✅ | ❌ |
| Read agent notes | ✅ | ✅ (latest_note in card) |
| Create task | ✅ | ✅ |
| Edit task | ✅ | ✅ (title/desc/project) |
| Delete task | ✅ | ❌ |
| Move task stage | ✅ | ❌ |
| Assign task to agent | ✅ | ❌ |
| View task CI/PR status | ✅ | ✅ |
| Create goal | ✅ | ✅ (via /goal) |
| Set goal budget | ✅ | ❌ |
| View goal progress | ✅ | ✅ |
| Cancel/edit goal | ✅ | ❌ |
| Handle escalations | ✅ | ✅ |
| Event log with filter | ✅ | ✅ |
| Icinga monitor | ✅ | ✅ |
| Work queue (Plane) | ✅ | ✅ (read-only) |
| Promote queue item → goal | ✅ | ❌ |
| Autopilot toggle | ✅ | ✅ |
| Edit role prompts | ✅ | ✅ |
| Manual cleanup trigger | ✅ | ❌ |
| Triage view | ✅ | ❌ |
| Voice/transcription | ✅ | ❌ |

---

---

## Peer Review Findings (Additional Issues)

These issues were identified by the multi-model peer review and are not covered in the sections above.

---

### 16. No global attention/triage view — operators miss fires in other sessions (CRITICAL)

**Consensus finding (all three models).**

With 2–5 concurrent sessions, the only cross-session signals are Telegram notifications. The TUI gives no in-band indication that Session C has a blocked task or escalation while you're looking at Session A. There are no per-session health badges in the sidebar, no exception counter, no "jump to next problem" binding.

**Impact:** Operators manage exceptions, not lists. Without in-band attention routing, incidents go unnoticed until a Telegram ping arrives or the operator happens to scroll to the right session.

**Suggested fix:**
- Add exception badges on session sidebar rows: `auth-oauth [2 blocked] [1 escalation]`
- Add a global triage home screen (capital `T`) showing all sessions' blocked/failed/needs_human items sorted by age, with one-key actions: retry, assign, attach, ack
- Add `]e` / `[e` bindings to jump to next/previous exception across sessions

---

### 17. Flat sidebar doesn't match the system's actual hierarchy (CRITICAL)

**Consensus finding (all three models).**

The sidebar renders sessions, agents, and tasks in a single flat list. The real structure is `Session → (Orchestrator) → Workers → Tasks`. The flat list:
- Makes it impossible to tell at a glance which agent owns which task
- Conflates the control plane (orchestrator) with workers
- Scales very poorly: 5 sessions × 5 agents × 3 tasks = 40+ rows with no grouping

**Suggested fix:**
- Make sessions collapsible tree headers with rollup counts (`3 agents · 2 live · 5 tasks · 1 blocked`)
- Render orchestrator agents with a distinct visual marker (different icon or color)
- Consider moving tasks out of the sidebar entirely — tasks are better surfaced in the detail panel and goals screen, not as navigation targets
- Auto-collapse healthy sessions; auto-expand sessions with exceptions

---

### 18. No diagnostic preview — must attach tmux to understand why an agent is stuck (HIGH)

**Consensus finding (all three models).**

When an agent is `stuck`, `blocked`, or `timed_out`, the detail panel shows the status label and the blocked reason string (if set). It doesn't show the last N lines of agent output, the last file the agent touched, or what action caused the failure. Attaching tmux is the only way to diagnose.

**Impact:** At 25 agents across 5 sessions, walking through tmux attach for each stuck agent doesn't scale. Mean time to diagnosis rises sharply.

**Suggested fix:**
- In the agent detail panel, show the last 3–5 lines of terminal capture when status is `stuck`/`waiting`/`failed` (data is already being polled)
- Add "last attempted action" from the event stream
- Add "time in current status" — this is the single most useful ops signal (e.g., "stuck for 23 minutes")
- Add contextual recovery actions per failure state (visible in detail panel): Retry / Restart / Reassign / Attach

---

### 19. Keybinding overload + chat input = accidental activation risk (HIGH)

**Consensus finding (all three models).**

The TUI has 20+ single-letter keybindings. The chat input can be focused (`Tab` or `/`). If focus management has any ambiguity, pressing `n` while "in chat" sends "n" to the orchestrator; pressing `d d` while focused on the wrong agent stops the wrong process.

There's also no visible mode indicator — users cannot tell from the screen whether they're in "navigate" or "input" mode.

**Suggested fix:**
- Show a clear mode indicator in the status bar: `[NAV]` vs `[CHAT]`
- Ensure `Esc` always reliably returns to navigation mode
- Add a command palette (`Ctrl+P` or `:`) for discoverability — reduces the need to memorise 20 keys
- Consider a leader-key scheme: `Space` as leader, then `a` for agent actions, `t` for task actions, `s` for session actions. This frees up single keys and prevents accidental triggers.
- At minimum: show contextual keybindings in the help hint based on what's currently selected (agent selected → show agent keys; task selected → show task keys)

---

### 20. Destructive actions lack adequate safeguards (HIGH)

**Consensus finding (all three models).**

`d d` stops an agent and `X X` deletes a session. Both are fast double-key sequences. Over SSH with latency, or when navigating quickly through a long sidebar, these are easy to trigger on the wrong item. There's a confirmation flash before the second press, but the window is short and the consequence (especially `X X`) is irreversible.

**Suggested fix:**
- For session delete (`X X`): require typing the session name or a short confirmation string, not just a second keypress. This mirrors the `git branch -D` vs `git push --force` distinction.
- Add an "undo window" for agent despawn — the agent record survives for 60 seconds and can be re-spawned without losing its configuration
- Make the "pending destructive action" state visually unmissable (red background on the sidebar item)

---

### 21. No "time in state" / age signal on agents and tasks (HIGH)

**Consensus finding (GPT-5.2, Claude Opus).**

The detail panel and sidebar show current status but not duration. An agent that's been `waiting` for 30 seconds is fine; one that's been `waiting` for 45 minutes needs intervention. There's no way to tell them apart from the TUI.

**Suggested fix:**
- Show "time in current status" on every agent and task: `⣾ waiting (23m)`, `[implement] (1h 4m)`
- Color-code by age: green (< 5m), yellow (5–20m), red (> 20m) for `waiting`/`blocked` states
- In the sidebar task row, show age-in-stage instead of (or alongside) the stage badge

---

### 22. State reconciliation with tmux — zombie/ghost agents (MEDIUM)

**Finding from Gemini 3.1 Pro and GPT-5.2.**

If someone SSHs into the NUC and manually kills a tmux session, or if a Claude Code process crashes, SwarmOps continues to show the agent as live. The watchdog (`swarm_watchdog.go`) detects stuck agents based on heartbeat/terminal activity, but there's a detection lag and it doesn't catch hard kills.

**Impact:** Operators see `● live` badges and "coding" status on agents that are actually dead. This leads to wrong decisions (e.g., "why isn't Alice making progress? let me inject again" when Alice's process is gone).

**Suggested fix:**
- The terminal capture polling already checks if tmux session exists — surface this: if `tmux ls` doesn't include the session name, immediately set agent status to `dead` rather than `waiting`
- Add a "last heartbeat age" field to the agent detail panel
- Show "data freshness" in the status bar: `Updated 4s ago` or `Stale — polling paused`

---

### 23. No session spawn templates — cold start is too manual (MEDIUM)

**Finding from Gemini 3.1 Pro.**

Creating a session requires: `c` → name → `n` → orchestrator name+role → `s` → `n` → dev name+role+repo → `s` → `n` → qa name+role → `s` → `/goal`. That's 12+ steps for a standard 3-agent swarm.

**Suggested fix:**
- When pressing `c` (new session), offer template options:
  - `[1] Standard Dev Swarm` → 1 orchestrator + 1 senior-dev + 1 qa-agent, asks for repo path once
  - `[2] Solo Researcher` → 1 orchestrator + 1 researcher, no repo path
  - `[3] Custom` → current behaviour
- Templates are just YAML/JSON in config; operators can define their own

---

### 24. No explicit message target indicator in chat input (MEDIUM)

**Finding from GPT-5.2.**

The chat input placeholder reads "Message to orchestrator… /goal <desc>" but this is only accurate when a session is selected. If the context switches mid-type, or if the operator forgets the routing rules (`/goal` vs plain text), messages go to the wrong place silently.

**Suggested fix:**
- Always show the target in the input border or label: `→ orchestrator [auth-oauth]` or `→ session has no orchestrator`
- When an agent (not session) is selected, consider routing chat to that agent directly or show "will route to orchestrator"

---

### 25. No unread / "what changed since I last looked" tracking (MEDIUM)

**Finding from GPT-5.2.**

When you switch between sessions or return after 10 minutes, there's no way to know what changed. All events look the same. You either refresh everything (`R`) and re-read the whole viewport, or you miss changes.

**Suggested fix:**
- Track a "last seen" timestamp per session/agent
- Show unread event counts as badges: `auth-oauth [7 new]`
- Add a `M` (mark all seen) binding or auto-mark on selection
- Highlight events that arrived since last visit in a different color (fades after they're seen)

---

### 26. Bulk recovery actions — single-item ops don't scale (MEDIUM)

**Finding from Claude Opus 4.5.**

All intervention actions are single-item: stop one agent, retry one task. When 5 agents all go stuck on the same error (e.g., API rate limit, network outage), you need to act on all of them at once.

**Suggested fix:**
- Add multi-select to sidebar (e.g., `Space` to toggle selection on current item)
- Bulk actions on selection: Stop all, Restart all, Reassign all tasks
- Add "Stop all agents in session" as a session-level action
- Mention "global pause" in a future phase: suspend all tmux sessions in all swarms simultaneously

---

### 27. No circuit breaker / global pause (LOW–MEDIUM)

**Finding from Claude Opus 4.5.**

When something goes wrong at scale (runaway cost, wrong API key, bad role prompt deployed), there's no way to pause everything quickly. The operator must despawn each agent one at a time.

**Suggested fix:**
- Add `Ctrl+P` (pause all) as a session-level action: suspends all tmux panes in the session
- Add a global kill switch: stops all agents across all sessions, writes a `PAUSED` state to the DB
- Show a prominent "PAUSED" indicator in the HUD when active

---

### 28. No workspace/files-changed visibility (LOW)

**Finding from Claude Opus 4.5.**

Agents work in git worktrees or scratch directories. The TUI shows `current_file` (the file being edited right now) but not what the agent has changed overall — no `git diff --stat`, no file list, no branch name.

**Suggested fix:**
- In the agent detail panel, add a "changed files" section showing `git diff --name-only` output for worktree agents (this can be fetched same way as terminal capture)
- Show branch name for worktree agents
- Add a `B` binding to open the worktree path in a file browser (`ranger` / `lf`) via `tea.ExecProcess`

---

### 29. No audit trail of operator actions (LOW)

**Finding from Claude Opus 4.5.**

The event log records agent actions but not operator actions. There's no record of "at 14:32 operator injected message to Alice" or "at 15:01 operator manually moved task from implement to test". Post-mortems are difficult.

**Suggested fix:**
- Write operator actions to the swarm events table with a `source: operator` field (this is already supported — the note API has `created_by`)
- Surface operator actions in the event log with a distinct color (e.g., orange)

---

## Updated Priority Recommendations

Integrating original analysis + peer review findings:

### Tier 1 — Do Now (structural / safety)
1. **Global triage view with session badges** — operators miss fires without in-band signals
2. **Hierarchical sidebar** with collapsible sessions and rollup counts
3. **Time-in-state on agents and tasks** — the single most useful ops signal, cheap to add
4. **Diagnostic preview in detail panel** — last output lines + recovery actions for stuck agents
5. **Destructive action safeguards** — session delete should require typed confirmation

### Tier 2 — Short Term (usability)
6. **Direct agent inject** — `/inject` in chat or `i` key on agent
7. **Command palette** (`Ctrl+P` or `:`) — reduces keybinding memorisation burden
8. **Explicit chat target indicator** — always show who receives the message
9. **Delete agent/task records** — tombstone accumulation
10. **Unread / "what changed" tracking** — badges + last-seen markers

### Tier 3 — Medium Term (completeness)
11. **Manual task stage transition** (add Stage field to edit modal)
12. **Session spawn templates** (reduce cold-start friction)
13. **Tmux state reconciliation** (zombie/ghost agent detection)
14. **Work queue → goal promotion** (complete the Plane loop)
15. **Goals screen edit** (budget + cancel)
16. **Bulk recovery actions** (multi-select + bulk stop/retry)

### Tier 4 — Lower Priority
17. Agent notes from TUI
18. Task assignment in TUI
19. Triage cross-session view (subsumed by #1)
20. Goal budget CLI syntax
21. Manual cleanup trigger
22. Circuit breaker / global pause
23. Workspace/files-changed visibility
24. Operator audit trail
25. Voice features (not a terminal priority)
