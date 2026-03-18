# SwarmOps Documentation

Reference documentation for SwarmOps — a Go-based orchestration layer for Claude Code agent swarms.

## Contents

| Document | Summary |
|----------|---------|
| [security-isolation.md](security-isolation.md) | Agent isolation model: what is technically enforced vs. convention; prompt injection mitigations |
| [security-credentials.md](security-credentials.md) | Credential inheritance, MCP server blast radius, API key costs |
| [autopilot-authority.md](autopilot-authority.md) | What autopilot agents can do; hard vs. soft limits; Talos phase authority |
| [agent-stuck-detection.md](agent-stuck-detection.md) | Stuck monitor, task watchdog, orphan sweeper — how SwarmOps detects and recovers from stuck agents |
| [branch-merge-strategy.md](branch-merge-strategy.md) | Branching model, merge workflow, conflict resolution, cleanup |
| [glossary.md](glossary.md) | Definitions for all SwarmOps terms |

## Quick Reference

### Resource Limits (Environment Variables)

| Variable | Default | Description |
|----------|---------|-------------|
| `SWARM_MAX_AGENTS` | 10 | Maximum concurrent agents |
| `SWARM_MAX_TASKS` | 50 | Maximum queued/running tasks |
| `SWARM_MAX_DISK_MB` | 5000 | Disk quota across all worktrees (MB) |
| `SWARM_STUCK_TIMEOUT` | 30m | Time in `thinking` state before stuck promotion |
| `PORT` | 8080 | SwarmOps HTTP server port |

### Timeouts (Code Constants)

| Constant | Value | File |
|----------|-------|------|
| `watchdogHeartbeatTimeout` | 45m | `swarm_watchdog.go` |
| `watchdogAbsoluteTimeout` | 2h | `swarm_watchdog.go` |
| `orphanSweepInterval` | 10m | `swarm_cleanup.go` |
| `orphanGracePeriod` | 2m | `swarm_cleanup.go` |

### Context Rotation Thresholds

| Threshold | Value | Action |
|-----------|-------|--------|
| Warning | 70% | Agent prompted to compress and summarise |
| Graceful handoff | 85% | Commit work; spawn fresh session |
| Emergency handoff | 95% | 30s grace; kill session; fresh session continues |

### TUI Key Bindings (Selected)

| Key | Action |
|-----|--------|
| `g` | Goals view |
| `d` (×2) | Despawn agent (confirm) |
| `X` (×2) | Delete session (confirm) |
| `?` | Full help screen |
| `q` | Quit |

## Architecture Overview

```
SwarmOps Server (HTTP + WebSocket)
├── Background services
│   ├── Swarm monitor (15s — pane-based stuck detection)
│   ├── Task watchdog (60s — heartbeat + absolute timeout)
│   ├── Orphan sweeper (10m — clean stale sessions/worktrees)
│   ├── Auto-dispatch (30s — FIFO task→agent pairing)
│   ├── Disk usage poller
│   └── IPC poller (context rotation signals)
├── SQLite database (swarmops.db)
│   ├── swarm_agents
│   ├── swarm_tasks
│   ├── swarm_sessions
│   └── swarm_goals
└── Agent processes (tmux sessions: sw-{id[:12]})
    ├── Worktree: {repo}/.worktrees/sw-{id[:12]}/
    └── Branch: swarm/{id[:12]}
```

## Related Reading

- `STYLE_GUIDE.md` — Code style and contribution guidelines
- `README.md` (root) — Getting started, installation, configuration
- Talos workflow: see [glossary.md#talos](glossary.md)
