# Glossary

Terms used throughout SwarmOps documentation and codebase.

---

**Agent**
A running Claude Code process managed by SwarmOps. Each agent has a dedicated tmux session (`sw-{id[:12]}`), a git worktree, and a database record tracking its status and heartbeat. Agents execute tasks assigned to them by the auto-dispatch system or an orchestrator.

**Agent branch**
The git branch assigned to an agent: `swarm/{id[:12]}`. Agents commit their work to this branch. The branch persists after the worktree is removed, until explicitly deleted.

**Autopilot**
The mode in which SwarmOps operates without per-step human approval. Autopilot agents advance through task phases automatically according to their prompts. See [autopilot-authority.md](autopilot-authority.md) for authority limits.

**Auto-dispatch**
The 30-second background loop that pairs queued tasks with idle (non-orchestrator) agents. Tasks are dispatched in FIFO order. If no idle agents are available, queued tasks wait.

**Blackboard**
A shared memory store visible to all agents in a session, used for communication between agents and the orchestrator. Implemented as a key-value store in the SwarmOps database.

**Context rotation**
The process of handing off work from an agent whose context window is nearing capacity to a fresh agent session. Triggered at 85% context usage (graceful) or 95% (emergency). The new agent continues from a summarised handoff prompt.

**Disk usage poller**
A background goroutine that periodically measures disk usage across all active worktrees. If the total exceeds `SWARM_MAX_DISK_MB`, new task dispatch is paused.

**Escalation**
The process by which an agent signals that human judgment is required. Escalation paths include: setting task status to `needs_review`, using the HITL MCP tool, or notifying the orchestrator.

**Goal**
A high-level objective defined by the operator, decomposed into tasks by an orchestrator agent. Visible in the TUI Goals view (`g` key).

**Grace period**
The minimum age (2 minutes) before the orphan sweeper will remove a tmux session or worktree. Prevents false-positive cleanup of freshly spawned agents.

**Heartbeat**
A periodic signal sent by an agent to the SwarmOps server via the IPC socket, indicating the agent is alive. The task watchdog uses heartbeat timestamps to detect dead agents.

**HITL (Human-in-the-Loop)**
An MCP tool (`tkn-hitl`) that allows an agent to pause and request explicit human approval before proceeding. The operator receives a notification and must respond before the agent continues.

**IPC socket**
A Unix domain socket used for communication between agent processes and the SwarmOps server. Agents send status updates, heartbeats, and context percentage readings through the IPC socket.

**Orchestrator**
A special agent role responsible for decomposing goals into tasks, dispatching tasks to worker agents, monitoring progress, and handling escalations. Orchestrators are not auto-dispatched work tasks — they coordinate other agents.

**Orphan**
A tmux session or git worktree with the `sw-*` naming convention that has no corresponding agent record in the SwarmOps database. Orphans are cleaned up by the orphan sweeper.

**Orphan sweeper**
A background goroutine that runs every 10 minutes, identifying and removing orphan tmux sessions and worktrees.

**PA agent (Persistent Agent)**
A proposed agent type (TKN-147) that maintains continuity across sessions, accumulating context about a project or domain rather than being spawned fresh for each task. Not yet implemented.

**Prompt injection**
An attack where malicious instructions are embedded in content read by an agent (e.g., a web page, issue body, or file), attempting to override the agent's task prompt. SwarmOps applies basic mitigations via `sanitizeExternalContent()` and `detectInjectionAttempt()`. See [security-isolation.md](security-isolation.md).

**Session**
A SwarmOps session groups a set of agents and tasks around a shared objective. Sessions map to tmux sessions on the host. Each session has a name, a set of associated agents, and a goal backlog.

**Stuck**
An agent status indicating the agent has been in `thinking` state (not `waiting` or `coding`) for longer than `SWARM_STUCK_TIMEOUT` (default: 30 minutes). Stuck agents are surfaced in the TUI and trigger orchestrator notification.

**sw- prefix**
The naming convention for SwarmOps-managed tmux sessions and worktrees: `sw-{id[:12]}`. The prefix allows SwarmOps to distinguish its resources from manually created sessions/directories.

**Swarm**
Informally, a group of agents working in parallel on related tasks. More specifically, a SwarmOps session running multiple concurrent agents.

**SwarmOps**
The name of this system (formerly `remote-code`). A Go server providing HTTP API, WebSocket terminal access, TUI, and background services for orchestrating Claude Code agents.

**Talos**
The 8-phase task lifecycle used in SwarmOps: `spec → plan → plan_review → implement → impl_review → judge → deploy → document`. Named after the bronze automaton of Greek mythology. Each phase has a defined scope, output artifact, and review gate.

**Task**
A unit of work assigned to an agent. Tasks have a status (queued, assigned, running, needs_review, complete, timed_out) and a Talos phase. Tasks are stored in the SwarmOps database.

**Task bus**
A proposed improvement (TKN-146) to replace the current direct task assignment with a persistent, DB-backed queue supporting priority, retry, and dead-letter semantics. Not yet implemented.

**Task watchdog**
An embedded goroutine in the SwarmOps server that checks all running tasks every 60 seconds. Marks tasks `timed_out` when the heartbeat expires (45 minutes) and the tmux session is dead, or when the absolute timeout (2 hours) is reached.

**timed_out**
A terminal task status indicating the agent did not complete the task within the allowed time. The task can be re-queued manually.

**tmux**
A terminal multiplexer used by SwarmOps to run agent processes. Each agent runs in a named tmux session (`sw-{id[:12]}`). SwarmOps can attach to these sessions via the WebSocket terminal.

**Token budget**
The running total of tokens consumed by an agent, tracked per-session. Visible in the TUI HUD as `{n}k tok  ~${cost}`.

**TUI**
The terminal user interface for SwarmOps, built with Bubbletea. Provides real-time visibility into agents, tasks, sessions, and system health. Launch with `./remote-code tui`.

**Worktree**
A git worktree is a checked-out copy of a repository at a specific path and branch, separate from the main working tree. Each SwarmOps agent gets a worktree at `{repo}/.worktrees/sw-{id[:12]}/` on branch `swarm/{id[:12]}`.

**Worktrees directory**
The directory within a repository where agent worktrees are created: `{repo}/.worktrees/`. This directory should be listed in `.gitignore` to prevent worktrees from appearing in git status.
