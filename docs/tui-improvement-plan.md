# SwarmOps TUI Improvement Plan

> Addresses gaps identified in `tui-gaps.md` and the subsequent multi-model peer review.
> Peer reviewed by GPT-5.2, Gemini 3.1 Pro, and Claude Opus 4.5. Revised plan incorporates findings.

---

## Guiding Principles

1. **One concern per file** — `swarm_tui.go` is 3385 lines and cannot grow further without a structural split. Foundation work happens first.
2. **Selection by identity, not index** — cursor position tracks a `(kind, sid, eid)` triple; rebuilt lists always restore by identity.
3. **Generic confirmation pattern** — no new `pendingXxx` fields; one `pendingConfirm` struct replaces all of them.
4. **Conservative performance** — new data fetches are on-demand or capped; no unbounded background polling added.
5. **Migrations are backwards-compatible** — new columns use `DEFAULT` or nullable; existing rows work without re-init.
6. **Each phase ships as a PR** — `make test-race` green, deployed to NUC, smoke tested before next phase begins.

---

## Phase 0 — Foundation (prerequisite for all phases)

*No user-visible changes. Makes all subsequent phases safe.*

### 0a. Split swarm_tui.go into packages

`swarm_tui.go` is a 3385-line god object. Split by concern without changing behaviour.

**New files (all in `package main`):**

| File | Contents |
|------|----------|
| `swarm_tui_model.go` | `tuiModel` struct, `newTUIModel()`, `Init()`, root `Update()`, root `View()` |
| `swarm_tui_sidebar.go` | `tuiItemKind`, `tuiSidebarItem`, `rebuildItems()`, `selItem()`, `lookupAgent/Task/Session()`, `viewSidebar()` |
| `swarm_tui_modal.go` | `tuiModalKind`, `tuiModal`, `tuiModalField`, `newTUIModal()`, `newTUIEditModal()`, `updateModal()`, `viewModal()`, `submitModal()` |
| `swarm_tui_views.go` | All secondary screens: goals, escalations, work queue, event log, Icinga — each view's `update*` and `view*` methods |
| `swarm_tui_client.go` | `swarmClient`, `tuiWSManager`, all `tea.Cmd` factories (`post`, `patch`, `get`, `fetchAll`, `fetchTerminal`) |
| `swarm_tui_render.go` | `viewHUD()`, `viewStatusBar()`, `viewDetail()`, `viewAgentDetail()`, `viewTaskDetail()`, `viewSessionDetail()`, `viewInput()`, `viewHelp()`, `viewHelpScreen()` |
| `swarm_tui_types.go` | All `tuiXxx` data structs (`tuiAgent`, `tuiTask`, `tuiSession`, `tuiState`, `tuiGoal`, `tuiEscalation`, `tuiEvent`, message types) |

**Process:** Move code, verify `make test-race` still passes. No logic changes.

**Why this must go first:** Every subsequent phase adds state to `tuiModel` or new methods to the view/update cycle. Without clear boundaries, those additions cause immediate regressions.

---

### 0b. Stable sidebar item identity + cursor restoration

Currently `rebuildItems()` restores cursor by identity (already implemented). But several upcoming features need a helper that can *navigate to* a specific item and *expand* a collapsed session to find it.

**Changes to `swarm_tui_sidebar.go`:**

- Add `func (m *tuiModel) navigateTo(kind tuiItemKind, sid, eid string)` that:
  1. Ensures the session `sid` is not collapsed (sets `collapsedSessions[sid] = false`)
  2. Calls `m.rebuildItems()`
  3. Scans `m.items` for the matching `(kind, sid, eid)` and sets `m.cursor`
- Move `collapsedSessions map[string]bool` to `tuiModel` now (empty, not yet used) so it's available to `rebuildItems()` in Phase 2d.

**Why now:** `navigateTo` is required by the triage view (Phase 1c) and collapse (Phase 2d). Building it without those features allows it to be tested independently.

---

### 0c. Generic confirmation modal (replace pendingXxx pattern)

The current codebase has `pendingDespawn *tuiSidebarItem` and `pendingDeleteSession *tuiSidebarItem` — two separate fields for the same pattern. The plan would add a third for agent/task delete. Stop here.

**Changes to `swarm_tui_modal.go`:**

Add a `pendingConfirm` struct to `tuiModel`:

```go
type pendingConfirmAction struct {
    label      string          // human label for flash: "Stop Alice?"
    targetItem *tuiSidebarItem // item to act on
    onConfirm  tea.Cmd         // command to run on second press
}
pendingConfirm *pendingConfirmAction
```

- Replace `pendingDespawn` and `pendingDeleteSession` with `pendingConfirm`.
- Logic: first trigger sets `pendingConfirm`. Any key other than the confirm key (or Esc) clears it. Second matching press executes `onConfirm` and clears.
- Add `tuiModalConfirmTyped tuiModalKind` for destructive actions that require typing the item name (session delete). This uses the existing modal infrastructure with a single text field and a validator func: `validator func(string) bool`. Submit is disabled until `validator(input) == true`.

**Why now:** Prevents an additional `pendingXxx` field proliferating in Phase 2c. Also unblocks Phase 1e (hardened session delete uses `tuiModalConfirmTyped`).

---

### 0d. Add `deleteItem` command to client

Add `func (c *swarmClient) deleteItem(op, path string) tea.Cmd` to `swarm_tui_client.go`. Issues `DELETE`, returns `tuiDoneMsg` or `tuiErrMsg`. Used by Phase 2c and 3b.

---

## Phase 1 — Structural Fixes & Critical Safety

*After Phase 0. Each item is independently deployable.*

### 1a. Time-in-state for agents and tasks

**DB migration (`032_status_timestamps.sql`):**

```sql
ALTER TABLE swarm_agents ADD COLUMN status_changed_at INTEGER;
ALTER TABLE swarm_tasks  ADD COLUMN stage_changed_at INTEGER;
```

**Backend changes (`swarm.go`):**

Rather than patching 10+ call sites, add two wrapper helpers:

```go
func setAgentStatus(ctx context.Context, agentID, status string) {
    database.ExecContext(ctx,
        "UPDATE swarm_agents SET status = ?, status_changed_at = ? WHERE id = ?",
        status, time.Now().Unix(), agentID)
}

func setTaskStage(ctx context.Context, taskID, stage string) {
    database.ExecContext(ctx,
        "UPDATE swarm_tasks SET stage = ?, stage_changed_at = ? WHERE id = ?",
        stage, time.Now().Unix(), taskID)
}
```

Replace all direct `UPDATE swarm_agents SET status = ?` and `UPDATE swarm_tasks SET stage = ?` calls with these helpers. Use `grep -n "SET status = " swarm.go swarm_spawn.go swarm_watchdog.go` to find every call site.

Expose on API structs:
- `tuiAgent.StatusChangedAt int64 json:"status_changed_at,omitempty"`
- `tuiTask.StageChangedAt int64 json:"stage_changed_at,omitempty"`

**TUI changes (`swarm_tui_render.go`, `swarm_tui_sidebar.go`):**

- Add `func ageStr(changedAt int64) string` — returns `"just now"` (<30s), `"4m"` (<1h), `"1h4m"` (>=1h), `""` (zero/nil).
- Add `func ageColor(changedAt int64, status string) lipgloss.Color` — green (<5m), yellow (5–20m), red (>20m) when status is `waiting`/`stuck`/`blocked`.
- **Null-safe**: `if changedAt == 0 { return "" }` — never panic or display garbage.
- In `viewAgentDetail`: append `ageStr(agent.StatusChangedAt)` next to status label, colored by `ageColor`.
- In `viewTaskDetail`: append `ageStr(task.StageChangedAt)` next to stage label.
- In sidebar agent card row 2 (status line): append age when `waiting` or `stuck`.
- In sidebar task row: append `(age)` when `blocked` or `needs_human`.

**Files:** `db/migrations/032_status_timestamps.sql` (new), `database.go`, `swarm.go`, `swarm_spawn.go`, `swarm_watchdog.go`, `swarm_tui_types.go`, `swarm_tui_render.go`, `swarm_tui_sidebar.go`

---

### 1b. Session exception badges in sidebar

Pure TUI change. Data already available in `tuiState`.

**Changes to `swarm_tui_sidebar.go`:**

- Add `func sessionExceptions(st tuiState) (blocked, escalations, stuck int)`:
  - `blocked`: tasks with stage in `{blocked, needs_human, failed, timed_out}`
  - `escalations`: `len(st.Escalations)`
  - `stuck`: agents with `status == "stuck"`
- In `viewSidebar` session row: append a compact badge `[2b 1e 1s]` (b=blocked, e=escalation, s=stuck). Omit zero components. Render in red if any non-zero, dim otherwise.
- When session is NOT the selected session and has non-zero exceptions: render a `▸` left-margin indicator in dim red.

**Files:** `swarm_tui_sidebar.go`

---

### 1c. Global triage view (`T`)

**Changes to `swarm_tui_model.go`:**

Add `triageView bool`, `triageCursor int` to `tuiModel`.

**Changes to `swarm_tui_views.go`:**

Add `type triageItem struct { kind tuiItemKind; sid, eid, sessionName, label, detail, age string; severity int }`.

Add `func buildTriageItems(m tuiModel) []triageItem`:
- Iterate all sessions/agents/tasks/escalations.
- Severity: escalation=3, stuck agent=3, blocked/needs_human task=2, needs_review task=1.
- Sort: `severity DESC, age (oldest first)`.
- Requires `ageStr` from Phase 1a — severity-only triage still works without age if 1a isn't done, but 1a should precede this.

Add `func (m tuiModel) updateTriageView(msg tea.KeyMsg) (tuiModel, []tea.Cmd)`:
- `j`/`↓`, `k`/`↑`: move cursor.
- `Enter`: call `m.navigateTo(item.kind, item.sid, item.eid)` from Phase 0b, close triage.
- `r`: POST to resume/retry endpoint for the selected item (tasks only).
- `T`/`Esc`/`q`: close.

Add `func (m tuiModel) viewTriageScreen() string`:
- Header with count and key hints.
- Each row: `[session] AGE SEV TYPE title/name`.
- Empty state: `✓ Nothing needs attention`.

In root `View()` and `Update()`, add triage handling. In `updateSidebar`, add `case "T":`.

**Files:** `swarm_tui_model.go`, `swarm_tui_views.go`

---

### 1d. Diagnostic preview in agent detail panel

**Scope deliberately limited** (peer review flagged background polling as a performance trap):

- Fetch terminal content for the *selected* agent only — this already happens every ~450ms when an agent is selected.
- In `viewAgentDetail` (`swarm_tui_render.go`): when `agent.Status` is `stuck`, `waiting`, or `failed`, extract the last 4 non-empty lines from `m.termContent[aid]` and render them below the sprite card in a dim box headed `last output:`.
- If `termContent` is empty, show `(press Enter to attach for live view)`.
- **No change to background fetch logic** — do not extend polling to unselected agents. This is intentionally conservative.
- Add `termContent` pruning: in `rebuildItems()`, prune `termContent` entries for agentIDs no longer in any session's agent list.

**Files:** `swarm_tui_render.go`, `swarm_tui_sidebar.go`

---

### 1e. Hardened session delete confirmation

Replaces the current `X X` double-keypress with a typed-name modal (using infrastructure from Phase 0c).

**Changes to `swarm_tui_sidebar.go`:**

- Change `case "X":` handler: on first press, open `tuiModalConfirmTyped` with:
  - prompt: `Delete session "NAME"? Type session name to confirm:`
  - `validator: func(s string) bool { return s == sess.Name }`
  - `onConfirm`: the existing DELETE session tea.Cmd
- Remove `pendingDeleteSession` field (subsumed by `pendingConfirm` from Phase 0c).
- Update `rebuildItems()`, `viewSidebar()`, and any other references to `pendingDeleteSession`.

**Files:** `swarm_tui_modal.go`, `swarm_tui_sidebar.go`

---

## Phase 2 — Operator UX Improvements

### 2a. Mode indicator + direct agent inject (combined per peer review)

These two are combined: the mode indicator is 5 lines of code and shouldn't be a standalone PR.

**Mode indicator (`swarm_tui_render.go`):**
- In `viewStatusBar()`, add right-side badge: `[CHAT]` (teal) when `focus == tuiFocusInput`, `[MODAL]` (yellow) when `focus == tuiFocusModal`, `[CONFIRM]` (red) when `pendingConfirm != nil`.

**Direct agent inject (`swarm_tui_sidebar.go`, `swarm_tui_modal.go`):**
- Add `tuiModalInjectAgent tuiModalKind`.
- `newTUIModal(tuiModalInjectAgent, sid)`: single textarea field, label `"Message"`, placeholder `"Direct instruction to agent's Claude Code session…"`.
- `case "i":` in `updateSidebar`: if selected item is a live agent, open inject modal.
- `submitModal` for `tuiModalInjectAgent`: POST to `/api/swarm/sessions/{sid}/agents/{eid}/inject`.

**Dynamic chat placeholder (`swarm_tui_render.go`):**
- In `viewInput()`, set placeholder text based on `m.selItem()`:
  - If live agent selected: `→ {name} (i=inject direct) or /goal <desc>`
  - If session: `Message to orchestrator… /goal <desc> (Enter sends, Esc unfocuses)`
  - If no orchestrator: `No orchestrator running — spawn one first`

**Files:** `swarm_tui_render.go`, `swarm_tui_sidebar.go`, `swarm_tui_modal.go`

---

### 2b. Delete agent and task records (`D` key)

Uses `pendingConfirm` from Phase 0c and `deleteItem` from Phase 0d.

**Changes to `swarm_tui_sidebar.go`:**

```go
case "D":
    it := m.selItem()
    switch it.kind {
    case tuiItemAgent:
        agent := m.lookupAgent(it.sid, it.eid)
        if agent.TmuxSession != nil {
            m.setFlash("Despawn first (dd), then delete (D)", false)
            break
        }
        m.pendingConfirm = &pendingConfirmAction{
            label:      "Delete agent \"" + agent.Name + "\"?",
            targetItem: it,
            onConfirm:  m.client.deleteItem("delete-agent", "/api/swarm/sessions/"+it.sid+"/agents/"+it.eid),
        }
        m.setFlash(m.pendingConfirm.label+" Press D again to confirm", true)
    case tuiItemTask:
        task := m.lookupTask(it.sid, it.eid)
        m.pendingConfirm = &pendingConfirmAction{
            label:      "Delete task \"" + task.Title + "\"?",
            targetItem: it,
            onConfirm:  m.client.deleteItem("delete-task", "/api/swarm/sessions/"+it.sid+"/tasks/"+it.eid),
        }
        m.setFlash(m.pendingConfirm.label+" Press D again to confirm", true)
    }
```

- The generic `pendingConfirm` confirm-key is the same key that opened it (`D`). Update the top-of-`updateSidebar` guard to check `pendingConfirm.confirmKey`.
- Add `"delete-agent": "Agent deleted"`, `"delete-task": "Task deleted"` to `tuiDoneMsg` labels.
- Update help screen.

**Files:** `swarm_tui_sidebar.go`

---

### 2c. Session collapse (`Enter` on session rows)

Uses `collapsedSessions` and `navigateTo` from Phase 0b (already wired in).

**Changes to `swarm_tui_sidebar.go`:**

- In `updateSidebar`, change the `Enter` handler: if `it.kind == tuiItemSession`, toggle `m.collapsedSessions[it.sid]` and call `m.rebuildItems()`. Existing tmux-attach logic only fires for `tuiItemAgent`.
- In `viewSidebar` session rows: prepend `▼ ` (expanded) or `▶ ` (collapsed) before session name.
- In `rebuildItems()`: skip agents and tasks for `m.collapsedSessions[sess.ID] == true`.

**Auto-collapse heuristic (minimal, per peer review):** Only set a session as initially-collapsed when it is first added to `collapsedSessions` if: `len(m.sessions) >= 3 && live == 0 && exceptions == 0`. This runs once on first observation. Once a user explicitly toggles, that takes precedence permanently. **No `userExpandedSessions` map** — the single `collapsedSessions` map is the truth; auto-collapse only writes to it on session first-seen.

**Files:** `swarm_tui_sidebar.go`

---

### 2d. Manual task stage transition (`S` key)

**Changes to `swarm_tui_sidebar.go`, `swarm_tui_modal.go`:**

- Add `tuiModalEditTaskStage tuiModalKind`.
- `case "S":` when `it.kind == tuiItemTask`: open modal with current stage pre-populated.
- Modal: single field, label `"Stage"`, placeholder `"spec · implement · test · deploy · done · blocked · failed"`.
- `submitModal`: PATCH `/api/swarm/sessions/{sid}/tasks/{eid}` with `{"stage": value}`.
- Note: the API accepts any stage string but does not validate transitions. This is intentional — operator override.
- Update help screen.

**Files:** `swarm_tui_sidebar.go`, `swarm_tui_modal.go`

---

## Phase 3 — Completeness & Polish

### 3a. Work queue goal promotion + cursor

**Missing field:** Add `workQueueCursor int` to `tuiModel` (currently the screen has no cursor — navigation keys silently do nothing).

**Changes to `swarm_tui_views.go`:**

- Add cursor to `updateWorkQueueView`: `j`/`↓` and `k`/`↑` move `workQueueCursor`.
- Render selected row highlight in `viewWorkQueueScreen` using `workQueueCursor`.
- Add `Enter` / `g` binding: POST goal from `workQueueItems[workQueueCursor].Title`, close screen, flash confirmation.
- Cap cursor at `len(workQueueItems)-1` after data loads.

**Files:** `swarm_tui_model.go` (add field), `swarm_tui_views.go`

---

### 3b. Goals screen: cancel action + goals PATCH endpoint

**Backend check:** Verify `PATCH /api/swarm/sessions/{sid}/goals/{gid}` exists in `swarm.go`. If not (search for `swarm_goals` PATCH), add:

```go
case http.MethodPatch:
    var req map[string]interface{}
    json.NewDecoder(r.Body).Decode(&req)
    if status, ok := req["status"].(string); ok {
        database.ExecContext(ctx,
            "UPDATE swarm_goals SET status = ? WHERE id = ? AND session_id = ?",
            status, goalID, sessionID)
        swarmBroadcaster.schedule(sessionID)
    }
    w.WriteHeader(http.StatusNoContent)
```

**TUI changes to `swarm_tui_views.go`:**

- In `updateGoalView`: add `case "x":` — patch goal status to `cancelled` via PATCH endpoint.
- Add `case "e":` — for a future "edit goal" modal (create the skeleton only; actual edit fields in a follow-on).
- Update goals screen header hint: `x cancel · g/esc close`.

**Files:** `swarm.go` (if endpoint missing), `swarm_tui_views.go`

---

### 3c. Session spawn templates

**Design choice (per peer review):** No modal chaining. Templates create all agents in a single coordinated sequence server-side. Two options:

**Option A (preferred):** New backend endpoint `POST /api/swarm/sessions/{sid}/spawn-template` that accepts `{"template": "standard-dev", "repo_path": "..."}` and creates orchestrator + workers atomically, then returns the list of agent IDs.

**Option B (fallback):** Client-side saga with `tuiTemplateSpawnMsg{sid, agents []agentSpec, index int}` — on each `tuiDoneMsg{op: "create-agent"}`, send the next `tuiTemplateSpawnMsg` until done. Show `Spawning 2/3…` in flash.

Implement Option A if API effort is acceptable (adds one endpoint to `swarm.go`). Option B as fallback.

**TUI changes to `swarm_tui_modal.go`:**

- Add `tuiModalNewSessionTemplate tuiModalKind`.
- `case "c":` now opens template picker first: numbered options `1 standard-dev / 2 solo-researcher / 3 custom`.
- Choosing `3` or entering a non-number falls through to existing `tuiModalNewSession`.
- Templates are hardcoded structs for now.

**Files:** `swarm.go` (Option A), `swarm_tui_modal.go`

---

### 3d. Agent notes from TUI (`N` key)

**DB confirmation:** `swarm_agent_notes` table already exists (created in `010_swarm.sql` era, confirmed by test references in `api_test.go` line 44). No migration needed.

**TUI changes to `swarm_tui_sidebar.go`, `swarm_tui_modal.go`:**

- Add `tuiModalAgentNote tuiModalKind`.
- `case "N":` when agent selected: open modal, single textarea, label `"Note"`.
- `submitModal`: POST to `/api/swarm/sessions/{sid}/agents/{eid}/note` with `{"content": value, "created_by": "operator"}`.
- Update help screen.

**Files:** `swarm_tui_sidebar.go`, `swarm_tui_modal.go`

---

### 3e. Tmux zombie/ghost detection

**Changes to `swarm_watchdog.go`:**

Extend the existing background ticker (already checks heartbeats) to also validate tmux session existence:

```go
// Rate-limit: check each agent at most once per 30s to avoid subprocess spam
if time.Since(lastTmuxCheck[agentID]) < 30*time.Second {
    continue
}
lastTmuxCheck[agentID] = time.Now()

cmd := exec.Command("tmux", "has-session", "-t", *agent.TmuxSession)
if err := cmd.Run(); err != nil {
    // tmux session is gone — mark dead
    setAgentStatus(ctx, agentID, "failed")
    database.ExecContext(ctx, "UPDATE swarm_agents SET tmux_session = NULL WHERE id = ?", agentID)
    swarmBroadcaster.schedule(agent.SessionID)
}
```

- Use `lastTmuxCheck map[string]time.Time` to rate-limit (initialised in watchdog goroutine).
- Graceful degradation: wrap the entire block in `if _, err := exec.LookPath("tmux"); err != nil { continue }` — skip silently if tmux not installed.
- No debouncing for N-consecutive-misses: `tmux has-session` is reliable; a single failure means the session is gone.

**Files:** `swarm_watchdog.go`

---

## Test Strategy

Each phase must include:

**Phase 0:**
- `make test-race` still passes after file split (no logic changes).
- Add table tests for `buildTriageItems` (pure function, no DB/tmux) once written.
- Add table tests for `rebuildItems` invariants: "cursor follows identity after rebuild", "collapsed session items not included".

**Phase 1a:**
- Unit test for `setAgentStatus` / `setTaskStage` helpers: verify `status_changed_at` is written.
- Test `ageStr` helper (pure function).

**Phase 1c:**
- Unit test for `buildTriageItems`: given a crafted `tuiModel`, verify ordering by severity+age.

**All API-touching phases (2b, 2c, 3a–3d):**
- Add cases to `swarm_test.go` for each new or modified endpoint (already has helpers `swarmReq`, `createSwarmSession`).

---

## Out of Scope (Explicitly Deferred)

| Feature | Reason |
|---------|--------|
| Command palette | Significant new infrastructure (fuzzy search); deferred to standalone feature |
| Bulk recovery actions | Requires multi-select UI primitives not present in bubbletea stack |
| Circuit breaker / global pause | Better handled at systemd level; low frequency need |
| Unread / acknowledgement tracking | Requires new DB table + event model changes |
| Workspace / git diff view | Terminal capture already gives ad-hoc visibility |
| Audit trail of operator actions | `source` column on events table; follow-on migration |
| Voice features | Not a terminal tool priority |
| Spawn template config file | Phase 3c uses hardcoded templates; config is a follow-on |

---

## Migration Index

| # | File | Adds |
|---|------|------|
| 032 | `032_status_timestamps.sql` | `status_changed_at` on `swarm_agents`, `stage_changed_at` on `swarm_tasks` |

All other phases are TUI or backend changes with no new migrations.

---

## Execution Order

```
Phase 0  (foundation — no user-visible changes)
  0a split files  →  0b navigateTo + collapsedSessions map  →  0c pendingConfirm  →  0d deleteItem cmd

Phase 1  (ship as "structural + safety" PR)
  1a timestamps  →  1b badges  →  1c triage  →  1d diagnostic preview  →  1e hardened delete

Phase 2  (ship as "operator UX" PR)
  2a mode indicator + inject  →  2b delete records  →  2c collapse  →  2d stage transition

Phase 3  (ship as "completeness" PR)
  3a work queue promotion  →  3b goals cancel  →  3c templates  →  3d notes  →  3e zombie detection
```

---

## Files Modified

| File | Phases | Change type |
|------|--------|-------------|
| `swarm_tui.go` | 0a | Deleted (split into below) |
| `swarm_tui_model.go` | 0a–3 | New (root model, init, Update, View) |
| `swarm_tui_sidebar.go` | 0a–2d | New (sidebar, items, keys) |
| `swarm_tui_modal.go` | 0a–3d | New (modals, confirmations) |
| `swarm_tui_views.go` | 0a–3b | New (secondary screens) |
| `swarm_tui_client.go` | 0a, 0d | New (REST/WS commands) |
| `swarm_tui_render.go` | 0a–2a | New (all view functions) |
| `swarm_tui_types.go` | 0a, 1a | New (data structs) |
| `db/migrations/032_status_timestamps.sql` | 1a | New migration |
| `database.go` | 1a | Add 032 to slice |
| `swarm.go` | 1a, 3b, 3c | Helpers, goals PATCH, template endpoint |
| `swarm_spawn.go` | 1a | Use `setAgentStatus` helper |
| `swarm_watchdog.go` | 1a, 3e | Use `setAgentStatus`; tmux liveness check |
| `swarm_test.go` | 1c, 2b, 3a–3d | New test cases |
| `docs/tui-guide.md` | After each phase | Update |
