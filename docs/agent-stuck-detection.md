# Agent Stuck Detection

SwarmOps uses two independent mechanisms to detect and recover from agents that have stopped making progress: the **stuck monitor** (pane-based, fast) and the **task watchdog** (heartbeat-based, slow).

## Stuck Monitor

The stuck monitor runs every **15 seconds** per active agent. It captures the last **30 lines** of the agent's tmux pane and classifies the agent's current status by pattern-matching against known output patterns.

### Status Classification

Status is assigned by testing patterns in priority order. The **first match wins**:

| Priority | Status | Matched Patterns |
|----------|--------|-----------------|
| 1 | `waiting` | `Proceed?`, `Allow this action?`, `bypass permissions`, `Do you want to`, `Press Enter`, `y/n`, `Y/N`, `yes/no` |
| 2 | `coding` | `Agent(`, `TodoWrite(`, `Glob(`, spinner glyphs (`⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`), `Running`, `Executing`, `Searching`, `Reading`, `Writing`, `Editing` |
| 3 | `thinking` | `Thinking`, `Analyzing`, `Planning`, `Considering`, `Reasoning` |
| 4 | `stuck` | Error indicators (`Error`, `Failed`, `Exception`, `panic`, `fatal`) |
| 5 | `coding` | Fallback — anything that doesn't match above |

The `waiting` status (permission prompt) is checked first so that agents blocked on MCP approval are not incorrectly classified as stuck.

### Stuck Promotion

An agent is promoted to `stuck` state when it has been in `thinking` status (and not `waiting` or `coding`) for longer than `SWARM_STUCK_TIMEOUT` (default: **30 minutes**).

Stuck promotion triggers:
1. A log entry marking the agent as stuck
2. A UI flash notification in the TUI
3. The agent status changes to `stuck` in the database
4. If an orchestrator is present, it is notified to assess and potentially reassign the task

### Known Limitations of the Stuck Monitor

**Heuristic classification is brittle.** The pattern matching is a best-effort approximation based on common Claude Code output. New output formats introduced by Anthropic can break the classification.

**30-line capture window is small.** For long-running commands (e.g., `npm install`, `go build`, database migrations), the relevant output may have scrolled off. The monitor may see the tail of an npm install as "no activity" when the agent is actually working normally. This can cause false `thinking` classifications.

**Spinner glyphs are locale/terminal-dependent.** The braille spinner characters (`⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`) must render correctly in the tmux pane. If the terminal encoding is wrong, spinner detection fails and a working agent may be classified as `thinking`.

**Output buffering can cause false positives.** If a subprocess is not flushing output to stdout, the tmux pane may appear idle while work is in progress. The agent is running fine; the stuck monitor just cannot see it.

**The fallback is `coding`.** If no patterns match, the agent is classified as `coding` (not `stuck`). This means the stuck monitor errs toward false negatives (missing real stuck agents) rather than false positives (incorrectly killing healthy agents). This is an intentional trade-off.

**`waiting` status has no timeout.** An agent classified as `waiting` (blocking on a permission prompt) never advances to `stuck` — the stuck timer only triggers on `thinking`. If an agent is blocking on a prompt that no one approves (e.g., a MCP permission that was declined and not dismissed), it will wait indefinitely. The task watchdog's heartbeat mechanism provides the backstop: if the agent stops sending heartbeats (e.g., the process hangs), it times out after 45 minutes. If the agent is alive but blocked, it will not be automatically recovered. Use the HITL MCP server or manual tmux intervention to unblock.

## Task Watchdog

The task watchdog is an **embedded goroutine** in the SwarmOps server process (not a separate service or daemon). It wakes every **60 seconds** and checks all running tasks.

### Timeout Conditions

A task is marked `timed_out` when **both** of the following are true:

1. **Heartbeat timeout exceeded**: No heartbeat has been received for more than **45 minutes**. (Agents send heartbeats via the IPC socket on each status update.)
2. **tmux session is dead**: The agent's tmux session (`sw-{id[:12]}`) no longer exists.

Both conditions must be met simultaneously. This prevents a false timeout if the agent is running but the IPC socket briefly hiccups.

**Absolute timeout**: Any task running for more than **2 hours** is marked `timed_out` regardless of heartbeat state. Active heartbeats do **not** prevent the absolute cutoff — it fires unconditionally. This catches agents that are alive and responsive but making no meaningful progress within the expected time window.

### What Happens on Timeout

When a task times out:
1. Task status is set to `timed_out` in the database
2. If the tmux session still exists, it is killed
3. The git worktree is left in place (for inspection/recovery)
4. Any committed work on the agent branch is preserved

Timed-out tasks are surfaced in the TUI and can be re-queued manually.

### Watchdog vs. Stuck Monitor: Differences

| Property | Stuck Monitor | Task Watchdog |
|----------|--------------|---------------|
| Check interval | 15 seconds | 60 seconds |
| Detection method | tmux pane content | Heartbeat + session existence |
| False positive risk | Low (errs toward coding) | Very low (dual condition) |
| Action on detect | Status: `stuck`; notify | Status: `timed_out`; kill session |
| Recovers from | Agent frozen at prompt | Agent process died silently |

## Orphan Sweeper

A third mechanism, the **orphan sweeper**, runs every **10 minutes** and cleans up resources left behind by tasks that completed or failed without proper cleanup.

It sweeps:
- `sw-*` tmux sessions with no matching agent record in the DB
- `sw-*` git worktrees in `{repo}/.worktrees/` with no matching agent record in the DB

A **2-minute grace period** applies: sessions/worktrees younger than 2 minutes are not swept, to avoid racing with freshly spawned agents whose DB records may not yet be committed.

The orphan sweeper does **not** touch agent branches — committed work is always preserved.

## Tuning

| Variable | Default | Effect |
|----------|---------|--------|
| `SWARM_STUCK_TIMEOUT` | 30 minutes | Time in `thinking` state before promoting to `stuck` |
| `watchdogHeartbeatTimeout` | 45 minutes | Time without heartbeat before timeout (code constant) |
| `watchdogAbsoluteTimeout` | 2 hours | Maximum task duration regardless of heartbeat (code constant) |
| `orphanSweepInterval` | 10 minutes | How often the orphan sweeper runs (code constant) |
| `orphanGracePeriod` | 2 minutes | Minimum session age before orphan eligibility (code constant) |

The heartbeat and absolute timeout values are currently code constants (not environment-configurable). Adjust them in `swarm_watchdog.go` if your tasks routinely run longer than 2 hours.
