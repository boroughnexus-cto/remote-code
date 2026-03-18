# Agent Isolation Model

SwarmOps provides **process-level isolation** between agents through separate tmux sessions and git worktrees. This is a practical isolation model enforced by operating system process boundaries — not a cryptographic or kernel sandbox.

## What Isolation Means in Practice

Each agent runs as an independent OS process inside a dedicated tmux session (`sw-{id[:12]}`). Agents do not share a process, memory space, or working directory with each other.

The following are enforced by OS boundaries:

| Boundary | Mechanism | Strength |
|----------|-----------|----------|
| Process memory | Separate processes | Hard (OS) |
| Working directory | Separate git worktrees | Hard (filesystem) |
| tmux session | Named sessions, no cross-attach by default | Soft (convention) |
| Network access | Shared host network stack | None |
| File system (outside worktree) | Shared host filesystem | None |

## Git Worktree Isolation

Each agent receives a dedicated git worktree:

```
{repo}/.worktrees/sw-{id[:12]}/   ← agent's working directory
branch: swarm/{id[:12]}            ← private branch
```

Changes made in one agent's worktree are invisible to other agents until explicitly merged. Agents work on separate branches. However, because worktrees are subdirectories of the shared host filesystem (`.worktrees/sw-{id[:12]}/`), one agent can read or write another agent's uncommitted files by traversing the directory tree — there is no filesystem-level enforcement preventing this.

**What worktree isolation does NOT prevent:**
- An agent reading files outside its worktree (e.g., `~/` or other worktrees) — the host filesystem is shared
- An agent reading committed files from other branches via `git`
- An agent writing to shared locations (logs, tmp files, databases on the host)

## Cross-Session Access

Agents run on the same host under the same user account. An agent **could technically** access other agents' tmux sessions via `tmux send-keys` or read other worktrees via filesystem paths. SwarmOps does not impose technical controls that prevent this.

**The isolation model relies on:**
1. Claude Code's own safety training not to interfere with other processes
2. The agent's task prompt scoping work to its assigned worktree
3. Operator responsibility for task prompt hygiene

This is a **trust-based model**, appropriate for controlled environments where you trust the models being run. It is not appropriate as a security boundary between hostile workloads.

## Authority Limits: Policy vs. Enforcement

SwarmOps enforces certain limits technically; others are policy-level (enforced by prompts, not code).

### Technically Enforced Limits

| Action | Enforcement |
|--------|-------------|
| Push to `main` directly | Requires push permission on the remote; recommended to restrict via branch protection — SwarmOps itself does not block this |
| Write to SwarmOps database | No direct DB access from agent process |
| Spawn additional agents | Agent has no API token to call the SwarmOps API |
| Exceed disk quota | `SWARM_MAX_DISK_MB` checked by disk usage poller; tasks paused if exceeded |

### Policy-Level Limits (Prompt Enforced, Not Technically Blocked)

| Action | Current State | Recommended Guardrail |
|--------|--------------|----------------------|
| Merge PRs | Agents are instructed not to; no technical block on `git merge` | Branch protection rules on the remote |
| Delete branches | Convention only; `git branch -D` is available | Remote branch protection |
| Read secrets in worktree files | `.gitignore` prevents committing secrets; does not prevent reading | Do not place secrets in repo directories |
| Modify agent prompts/hooks | Convention only | Restrict hook file permissions |
| Send messages via MCP servers | Inherited MCP access; depends on available servers | Audit MCP server list before enabling agents |

The key distinction: **"Agents don't do X"** means the task prompt instructs against it. **"Agents cannot do X"** should be reserved for cases with a technical enforcement mechanism.

## .gitignore and Secrets

`.gitignore` entries **prevent files from being committed** to git. They do **not** prevent an agent from reading those files. If a `.env` file containing database credentials exists in the working directory, an agent can read it even if it is gitignored.

**SwarmOps's actual posture:** The system does not explicitly inject secrets into agents. However, agents inherit the host environment — environment variables present in the SwarmOps server process are available to spawned agent processes. If secrets exist as environment variables on the host, they are effectively passed to agents through inheritance. "Not designed to pass secrets" means SwarmOps adds no additional secrets; it does not mean agents receive a clean environment.

If your threat model requires keeping secrets away from agent processes, run SwarmOps with a minimal environment and do not place secret files in any directory an agent might access.

## Credential Inheritance and MCP Server Blast Radius

Agents inherit the full MCP server configuration of the SwarmOps process. Every MCP server accessible to you is accessible to every spawned agent. This includes:

- File system tools (read/write anywhere on the host)
- External API tools (Tailscale, Cloudflare, GitHub, etc.)
- Communication tools (Telegram, WhatsApp, email)
- Infrastructure tools (Komodo, Unraid, Home Assistant)

**Before enabling agent autonomy**, audit the MCP server list and consider:
- Which MCP servers does an agent actually need for its task?
- What is the blast radius if an agent misuses an MCP tool?
- Are any MCP servers irrevocably destructive (delete infrastructure, send external messages)?

There is currently no per-agent MCP server scoping. All agents share the same server access.

## Prompt Injection Mitigations

SwarmOps applies basic mitigations when injecting external content into agent prompts:

**`sanitizeExternalContent()`** — escapes `~~~` sequences that could be interpreted as tool-call boundaries.

**`detectInjectionAttempt()`** — scans for 10 known injection phrases (e.g., "ignore previous instructions", "disregard your system prompt"). Flagged content is quarantined and not injected.

These mitigations are **heuristic and incomplete**. They catch obvious patterns but are not a comprehensive defense against adversarial prompt injection. Do not run agents against untrusted external content (arbitrary web pages, third-party issue bodies, user-submitted text) without human review.

## Summary

SwarmOps provides practical isolation suitable for **cooperative agent teams working on trusted codebases**. It is not a security sandbox. The model assumes:

- Agents are running well-aligned models
- Tasks are authored by the operator
- External content injected into prompts has been reviewed
- The host environment is appropriately scoped
