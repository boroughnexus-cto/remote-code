# Credential Handling and MCP Access

This document covers how credentials flow into agent processes, what access agents inherit, and how to reason about blast radius.

## How Agents Receive Credentials

Agents do **not** receive credentials from SwarmOps explicitly. There is no credential injection, vault lookup, or per-agent token provisioning.

Agents inherit access through two channels:

### 1. Environment Variables

The SwarmOps server process inherits environment variables from the shell that started it. When Claude Code is spawned as an agent, it inherits the same environment. Any API keys, tokens, or secrets present as environment variables in the SwarmOps process are available to every agent.

**What this means:**
- If `ANTHROPIC_API_KEY` is set in the environment, every agent can use it (and spend from it)
- If a cloud provider key is in the environment, agents can make API calls
- There is no per-agent environment scoping

### 2. MCP Server Configuration

Agents run as full Claude Code sessions. They have access to the same MCP server configuration as the operator. Every tool available to you is available to every agent.

See [security-isolation.md](security-isolation.md) for the full blast radius table.

## Secrets in Worktree Files

Agents work in git worktrees: `{repo}/.worktrees/sw-{id[:12]}/`. Secrets that exist as files in the repository (or in directories the agent traverses) are readable by the agent.

**SwarmOps does not intentionally pass secrets to agents**, but it also cannot prevent an agent from reading a file that happens to be present in its working directory.

**Practices to reduce exposure:**
- `.gitignore` secret files to prevent accidental commits (but `.gitignore` does not prevent reads)
- Do not place `.env` files containing production credentials in repository directories used by agents
- Use environment variables for secrets that agents legitimately need, rather than files
- Rotate credentials if an agent run is suspected to have read secret files

## MCP Server Blast Radius

The following table covers the categories of MCP servers in a typical SwarmOps deployment and their risk profile when accessed by an agent:

| Category | Examples | Blast Radius if Misused |
|----------|----------|------------------------|
| File system | Read, Write, Edit | Arbitrary file read/write on host |
| Infrastructure | Komodo, Unraid | Start/stop containers, deploy stacks, modify config |
| Cloud | Cloudflare, Tailscale | Modify DNS, ACLs, tunnel routes, firewall rules |
| Communication | Telegram, WhatsApp, email | Send messages to external parties |
| External APIs | GitHub, Plane, Todoist | Create/modify/delete issues, PRs, tasks |
| Home automation | Home Assistant | Control physical devices |

**The blast radius is cumulative** — an agent with access to all these servers could, in theory, cause harm across all of them in a single session.

### Recommended Approach

Before running agents on a new class of task, ask:

1. **Which MCP servers does this task require?** File tools for code tasks, GitHub tools for PR tasks.
2. **Which servers are in scope but irrelevant?** Communication and infrastructure tools are rarely needed for code tasks.
3. **What is the worst-case outcome?** If the agent misunderstands the task or is injected with malicious instructions.

Currently, MCP server scoping is not implemented per-agent. If you need to restrict agent access, the only mechanism is to remove servers from your Claude Code MCP configuration entirely.

## API Key Costs

Agents use the same Anthropic API key as the operator. Costs accumulate against the same billing account. SwarmOps tracks token usage per agent (visible in the TUI HUD), but does not enforce spending limits.

The `SWARM_MAX_AGENTS` limit (default: 10) bounds the maximum number of concurrent agents making API calls. There is no per-task budget enforcement.

## Audit Trail

All agent tasks, status transitions, and heartbeats are recorded in the SwarmOps SQLite database (`swarmops.db`). This provides a post-hoc audit trail of what agents were running and when, but not a log of every tool call made by each agent.

For a full record of agent actions, review the tmux session scrollback or the Claude Code session transcript for each agent session.

## Summary

| Property | Status |
|----------|--------|
| Explicit credential injection | Not implemented — inheritance only |
| Per-agent environment scoping | Not implemented |
| Per-agent MCP server scoping | Not implemented |
| Secret file protection | Convention (`.gitignore`) — not enforced |
| API spend limits | Not enforced |
| Action audit trail | Task-level only (DB) |
