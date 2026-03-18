# Autopilot Authority and Limits

SwarmOps autopilot mode allows agents to execute multi-phase tasks with minimal human interaction. This document defines what autopilot agents are permitted to do, what is restricted by policy, and what is technically blocked.

## Authority Model

Autopilot agents operate under a **delegated authority model**: they act on behalf of the operator within boundaries established by task prompts, SwarmOps configuration, and external system permissions.

There are three tiers of restriction:

| Tier | Enforcement | Examples |
|------|------------|---------|
| **Hard limits** | Technical (code/OS) | Cannot write to SwarmOps DB, cannot spawn agents, disk quota |
| **Soft limits** | Policy (prompt-based) | Don't merge PRs, don't push to main, don't delete branches |
| **Convention** | Trust (model alignment) | Don't modify other agents' files, don't escalate without reason |

Understanding which tier applies to a given action is important for risk assessment.

## Hard Limits (Technically Enforced)

These limits cannot be bypassed without modifying SwarmOps itself:

**No SwarmOps API access**: Agents do not have an API token. They cannot call `/api/swarm/agents`, create sessions, spawn agents, or read the task queue via the SwarmOps HTTP API.

**No direct database access**: The SwarmOps SQLite database (`swarmops.db`) is accessed only by the server process. Agents have no database driver or credentials to query it.

**Disk quota**: `SWARM_MAX_DISK_MB` (default: 5000 MB). The disk usage poller checks periodically and can pause task creation when the limit is exceeded. This is checked at task dispatch time, not continuously — a single agent could temporarily exceed the quota before the next poll.

**Resource limits**: `SWARM_MAX_AGENTS` (default: 10) and `SWARM_MAX_TASKS` (default: 50) are enforced at the API level when creating agents and tasks.

## Soft Limits (Policy Enforced via Prompts)

These actions are **not technically blocked** but are excluded from agent task prompts. An agent that is explicitly instructed to perform these actions, or that is compromised by prompt injection, could attempt them.

### Merge Authority

| Action | Can Agent Attempt? | What Prevents It? | Recommended Guardrail |
|--------|-------------------|------------------|----------------------|
| Merge PR to main | Yes (via `gh pr merge`) | Task prompt instructions | Branch protection: require review |
| Push to main directly | Yes — SwarmOps does not block this | Task prompt; remote branch protection | Branch protection: restrict pushes |
| Delete remote branch | Yes (via `git push origin --delete`) | Convention | Branch protection |
| Push to agent branch | Yes | Nothing — intended behaviour | — |
| Open a PR | Yes | Nothing — intended behaviour | — |
| Request PR review | Yes | Nothing — intended behaviour | — |

### Deployment Authority

| Action | Can Agent Attempt? | What Prevents It? | Recommended Guardrail |
|--------|-------------------|------------------|----------------------|
| Run deployment scripts | Yes (file access) | Task prompt scoping | Require human step in deploy runbook |
| Trigger Komodo deploy | Yes (MCP access) | Task prompt scoping | HITL approval for production deploys |
| Modify infrastructure config | Yes (MCP access) | Task prompt scoping | Audit MCP server access |
| Send external notifications | Yes (comm MCP servers) | Task prompt scoping | Remove communication MCP servers if not needed |

## Escalation Paths

Autopilot agents can escalate to human review through:

1. **`needs_review` task state**: Agent sets task status to `needs_review`, which surfaces in the TUI and stops further auto-dispatch for that task.
2. **HITL (Human-in-the-Loop) MCP tool**: The `tkn-hitl` MCP server allows agents to pause and request explicit approval before proceeding.
3. **Orchestrator agent**: In multi-agent sessions, the orchestrator can be prompted to require human sign-off before advancing certain phases.

Escalation is **always available** — agents can and should escalate when uncertain rather than proceeding with destructive or irreversible actions.

## Talos Phase Authority

In the Talos 8-phase workflow (`spec → plan → plan_review → implement → impl_review → judge → deploy → document`), authority varies by phase:

| Phase | Agent Writes? | Human Gate? |
|-------|-------------|------------|
| spec | Task description only | Operator defines spec |
| plan | Implementation plan doc | Review phase |
| plan_review | Review comments | Operator approval recommended |
| implement | Code changes in worktree | — |
| impl_review | Review comments | Operator approval recommended |
| judge | Go/no-go assessment | Operator final sign-off |
| deploy | Deployment commands | Operator approval required by convention |
| document | Docs/changelog | — |

The `judge` and `deploy` phases are the highest-risk phases. By convention, autopilot does not auto-advance past `judge` without a human reviewing the judge's assessment.

## Context Rotation and Authority Handoff

When an agent's context window approaches capacity, SwarmOps triggers context rotation:

- **70% context**: Warning issued; agent prompted to compress and summarise
- **85% context**: Graceful handoff; current work is committed and a new agent session is spawned to continue
- **95% context**: Emergency handoff; 30-second grace period, then current session is killed and a fresh session continues

During handoff, authority passes to the next agent session. The handoff prompt includes a summary of completed work and remaining tasks. The new agent has the same authority as the previous one.

## What Autopilot Cannot Substitute For

Autopilot is not a replacement for human judgment on:

- **Novel situations**: If a task encounters an unexpected state (conflicting migrations, ambiguous requirements, external service outage), the agent should escalate.
- **Irreversible actions**: Any action that cannot be undone (production database migration, public communication, infrastructure deletion) should have a human gate.
- **Security decisions**: Auth changes, secret rotation, permission grants should not be delegated to autopilot without explicit per-action approval.
- **Cross-team coordination**: If a task requires coordination with people outside the SwarmOps system, a human should handle that communication.
