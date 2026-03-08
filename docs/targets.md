# Convergence Targets

## Active

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

### 🎯T1 Jevon's tool surface is locked down [high]

Achieved in cf54767. All inappropriate tools disabled via `--disallowedTools`.

### 🎯T2 Conversational interaction model works end-to-end [high]

Achieved in 83cc4a4. AskUserQuestion disabled, CLAUDE.md template instructs
conversational questions.

### 🎯T3 Test coverage exists for core packages [medium]

Achieved in cf45460 + 6215104. 61 tests across 7 packages. All packages
with code have tests; untested packages (auth, cli, voice, worker) are
empty stubs.
