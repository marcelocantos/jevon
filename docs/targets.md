# Convergence Targets

## Active

### 🎯T1 Jevon's tool surface is locked down [high]

**Desired state:** Jevon's Claude Code session has only the tools it
needs — file reading, search, web access, and its own MCP tools.
Interactive, team-management, and coding-workflow tools that conflict
with Jevon's coordinator role are disabled.

**Acceptance criteria:**
- `--disallowedTools` flag in `internal/jevon/jevon.go` includes:
  AskUserQuestion, EnterPlanMode, ExitPlanMode, Agent, TeamCreate,
  TeamDelete, SendMessage, TaskCreate, TaskUpdate, TaskList, TaskGet,
  TaskOutput, TaskStop, EnterWorktree, Skill, NotebookEdit.
- Jevon builds and runs without errors.

**Status:** achieved — committed and pushed (cf54767).

### 🎯T2 Conversational interaction model works end-to-end [high]

**Desired state:** Jevon asks questions and requests confirmation as
plain conversational text. The user responds via text or voice. No
structured UI widgets are needed for agent-initiated questions.

**Acceptance criteria:**
- AskUserQuestion is disabled (covered by 🎯T1).
- Jevon's generated CLAUDE.md instructs it to ask questions
  conversationally rather than using structured prompts.
- Worker questions are relayed through Jevon (already the case — workers
  run fire-and-forget, results come back to Jevon who can rephrase).

**Status:** achieved — tool disabled, CLAUDE.md template updated (83cc4a4).

### 🎯T3 Test coverage exists for core packages [medium]

**Desired state:** The server, jevon, mcpserver, and voice packages
have meaningful test coverage. At minimum, unit tests for the core
request/response paths.

**Acceptance criteria:**
- `go test ./...` exercises tests in internal/server, internal/jevon,
  internal/mcpserver.
- No package under internal/ has zero test files (excluding trivially
  small packages).

**Status:** not started — STABILITY.md notes "No tests for remote,
server, jevon, mcpserver, voice, auth, db."

### 🎯T4 Trust model defined for pre-1.0 [low]

**Desired state:** A documented trust model replaces blanket permission
bypass. Defines what Jevon and workers can do without approval, what
requires confirmation, and how confirmation flows to the user.

**Acceptance criteria:**
- A design document exists (e.g., docs/trust-model.md) describing the
  permission tiers and confirmation flow.
- STABILITY.md's "Needs a trust model before 1.0" note is resolved.

**Status:** identified — noted in STABILITY.md, no design work started.

## Achieved

(none yet)
