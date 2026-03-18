# Branch and Merge Strategy

SwarmOps agents work on isolated git branches within dedicated worktrees. This document covers the branching model, merge workflow, conflict resolution, and cleanup.

## Branch Model

Each agent receives:
- A **git worktree** at `{repo}/.worktrees/sw-{id[:12]}/`
- A **private branch** named `swarm/{id[:12]}`

The worktree is created from the repository's current `HEAD` (typically `main` or `master`) at the time the agent is spawned.

```
main ──────────────────────────────────────────► (ongoing)
  │
  ├─ swarm/a1b2c3d4e5f6  (agent A's branch)
  │   └─ commits from agent A
  │
  └─ swarm/9f8e7d6c5b4a  (agent B's branch)
      └─ commits from agent B
```

Agents commit directly to their `swarm/*` branch. They do not commit to `main`.

## Merge Workflow

The standard merge path for agent work is:

1. **Agent completes work** — commits all changes to `swarm/{id[:12]}`
2. **Agent opens a PR** — `gh pr create` from the agent branch to `main`
3. **Human review** — operator reviews the diff, runs tests, approves
4. **Human merges** — operator merges the PR via the GitHub UI or `gh pr merge`
5. **Cleanup** — worktree and branch removed (see Cleanup section)

Autopilot agents do **not** merge their own PRs by default. The merge step requires human action. This is a policy convention enforced via task prompts, not a technical restriction — branch protection rules on the remote are the recommended technical enforcement.

### Accelerated Merge (Trusted Agents)

For low-risk tasks where the operator has high confidence, agents can be instructed to merge their own PRs after all CI checks pass. This should be an explicit per-task instruction, not a blanket policy.

If auto-merge is enabled, the agent should:
1. Wait for all required CI checks to pass (`gh pr checks`)
2. Use squash merge to keep `main` history clean: `gh pr merge --squash --auto`
3. Confirm merge succeeded before marking the task complete

## Conflict Resolution

Conflicts arise when `main` has advanced past the point where the agent's branch was created, and the changes overlap.

### Rebase-First Strategy

Agents should rebase onto current `main` before opening a PR:

```bash
git fetch origin main
git rebase origin/main
```

If conflicts occur during rebase:
1. The agent resolves conflicts in each conflicted file
2. `git add {resolved-files}` and `git rebase --continue`
3. If a conflict cannot be resolved automatically, the agent should **stop and escalate** rather than guess

### Unresolvable Conflicts: Failure State Machine

Some conflicts cannot be resolved automatically:

| Situation | Agent Action | Human Action |
|-----------|-------------|-------------|
| Semantic conflict (logic changed under the agent) | Mark task `needs_review`; document the conflict in the task | Review both changes; decide which to keep |
| File deleted on `main` that agent modified | Mark task `needs_review`; summarise the divergence | Decide if the deletion was intentional |
| Rebase produces corrupt output | Abandon rebase (`git rebase --abort`); mark `needs_review` | Manual resolution on a clean branch |
| Conflict in generated files | Regenerate the file from source; resolve | Review the regenerated output |

**Agents should never force-push to resolve a conflict.** If the rebase fails and cannot be cleanly resolved, the correct escalation path is `needs_review` — not overwriting `main`-side changes.

## Parallel Agent Conflicts

When multiple agents work on the same repository simultaneously (common in swarm mode), their branches evolve independently. The merge order determines the final state.

**Strategies to reduce parallel conflicts:**
- Assign agents to non-overlapping files or packages where possible
- Use the task system to serialise work on shared files: task B depends on task A
- Prefer small, focused tasks over broad refactors when running multiple agents

The orchestrator agent is responsible for decomposing work to minimise overlapping scope. If conflicts are frequent, the task decomposition strategy should be revisited.

## Cleanup

After a PR is merged, the agent branch and worktree should be removed:

```bash
# Remove the worktree
git worktree remove {repo}/.worktrees/sw-{id[:12]} --force

# Delete the local branch
git branch -D swarm/{id[:12]}

# Delete the remote branch (if it was pushed)
git push origin --delete swarm/{id[:12]}
```

SwarmOps handles cleanup automatically when an agent is despawned via the TUI (`d` key, confirmed). The orphan sweeper also removes worktrees for agents that have no DB record (see [agent-stuck-detection.md](agent-stuck-detection.md)).

**Agent branches are not deleted by the orphan sweeper.** Only the worktree directory is removed. The branch remains in git history, which allows recovery of work if a cleanup was premature. Delete branches manually once you are confident the work is no longer needed.

## Branch Protection Recommendations

Configure the following on your remote (GitHub/GitLab):

| Rule | Purpose |
|------|---------|
| Require PR before merging to `main` | Prevents agents from pushing directly to `main` |
| Require at least 1 approval | Ensures human review before merge |
| Require status checks to pass | Gates merge on CI |
| Restrict who can push to `main` | Limits to maintainers only |
| Do not allow force pushes to `main` | Prevents history rewriting |

These rules enforce the merge policy at the remote level, independent of SwarmOps conventions.

## Worktree Naming and Collision

Worktree names are derived from the first 12 characters of the agent UUID. UUID collisions in 12 characters are astronomically unlikely, but if a worktree name collision occurs (e.g., after a database reset), agent spawn will fail with a git error. Remove the stale worktree manually.
