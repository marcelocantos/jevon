# Trust Model

This document defines Jevon's permission model: what actions Jevon and
workers can take without approval, what requires user confirmation, and
how confirmation flows between Claude Code processes and the user.

## Actors

| Actor | Description | Current state |
|-------|-------------|---------------|
| **User** | Human operator interacting via phone app, `remote` TUI, or WebSocket client | Authenticated (future: mTLS client cert) |
| **Jevon** | Coordinator Claude Code instance managing workers | Runs with `--permission-mode bypassPermissions` |
| **Worker** | Task-specific Claude Code instance spawned by Jevon | Runs with `--dangerously-skip-permissions` |

## Permission tiers

### Tier 1: Autonomous (no confirmation needed)

Actions that are local, reversible, and contained within the worker's
assigned workdir.

**Jevon:**
- Read files anywhere in managed repos
- List and inspect worker sessions
- Format and relay messages between user and workers
- Query MCP tools (session status, list sessions)

**Workers:**
- Read files within their assigned workdir
- Write/edit files within their assigned workdir
- Run tests and builds within their assigned workdir
- Run read-only shell commands (grep, find, git status, git log, git diff)

### Tier 2: Confirmed (user must approve)

Actions that are hard to reverse, affect shared state, or cross
trust boundaries.

**Jevon:**
- Create new worker sessions
- Kill worker sessions
- Send commands to workers on behalf of the user

**Workers:**
- Git operations that modify history (commit, push, rebase, merge)
- Operations outside the assigned workdir
- Network operations (HTTP requests, API calls)
- File deletion
- Package installation or dependency changes
- Any action that Claude Code's built-in permission system would
  normally prompt for

### Tier 3: Prohibited

Actions that are never allowed regardless of confirmation.

**Jevon:**
- Modifying its own CLAUDE.md or .mcp.json
- Spawning processes outside of Claude Code workers
- Accessing credentials or secrets directly

**Workers:**
- Force-pushing to protected branches
- Deleting branches that aren't their own feature branches
- Accessing other workers' workdirs
- Modifying CI/CD pipeline configuration without review

## Confirmation flow

When a Tier 2 action is requested, the confirmation must reach the
user and return before the action proceeds.

```
Worker/Jevon                 jevond                    User device
     │                         │                           │
     │ permission_request      │                           │
     │ (tool, args, context)   │                           │
     ├────────────────────────>│                           │
     │                         │ ws: confirm_request       │
     │                         │ (tool, args, summary)     │
     │                         ├──────────────────────────>│
     │                         │                           │
     │                         │ ws: confirm_response      │
     │                         │ (approved: bool, reason?) │
     │                         │<──────────────────────────┤
     │ permission_response     │                           │
     │ (approved, reason?)     │                           │
     │<────────────────────────┤                           │
     │                         │                           │
```

### Protocol messages

**Server → Client:**

| Type | Fields | Description |
|------|--------|-------------|
| `confirm_request` | `id`, `actor` ("jevon"\|"worker:ID"), `tool`, `args`, `summary` | Permission request requiring user decision |

**Client → Server:**

| Type | Fields | Description |
|------|--------|-------------|
| `confirm_response` | `id`, `approved` (bool), `reason?` (string) | User's decision on a permission request |

### Timeout behaviour

- If no response arrives within 60 seconds, the action is **denied**
  (fail-closed).
- The user is notified that the request timed out.
- The worker/Jevon receives a denial with reason "timeout".

### Batching

When a worker triggers multiple Tier 2 actions in rapid succession
(e.g., committing and pushing), jevond may batch them into a single
confirmation request showing all pending actions. The user approves
or denies the batch as a unit.

## Implementation path

1. **🎯T4 (this document)**: Define the model. ✓
2. **Claude Code integration**: Replace `--permission-mode
   bypassPermissions` with a custom permission handler that routes
   confirmation requests through jevond's WebSocket protocol.
3. **🎯T5 Authentication**: Add mTLS so only provisioned devices can
   send confirmation responses.
4. **🎯T6 Permission enforcement**: Wire up the confirmation flow
   end-to-end and remove all bypass flags.

## Open questions

- Should workers be able to escalate to Jevon for pre-approval of
  common patterns (e.g., "approve all commits in this session")?
- Should there be a per-session trust level that the user can adjust
  (e.g., "I trust this worker to commit freely")?
- How should confirmation work when the user is offline or the phone
  app is disconnected? Queue and retry, or deny immediately?
