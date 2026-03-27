# Audit Log

Chronological record of audits, releases, documentation passes, and other
maintenance activities. Append-only — newest entries at the bottom.

## 2026-03-01 — /release v0.1.0

- **Commit**: `a765c29`
- **Outcome**: Released v0.1.0 (darwin-arm64, linux-amd64, linux-arm64). All CI jobs passed.
- **Changes**: Added `--version` and `--help-agent` flags to all binaries, `agents-guide.md`, `STABILITY.md`, release CI workflow.

## 2026-03-27 — /release v0.2.0

- **Outcome**: Released v0.2.0 (darwin-arm64, linux-amd64, linux-arm64). Major pivot to desktop-first web UI with persistent Claude PTY agents.
- **Changes**: Web chat UI, agent registry, transcript memory (FTS5), JSONL as source of truth, sqlpipe removed, MCP race fix. sqldeep query support deferred (local CGO dependency not CI-compatible yet).
- **Notes**: Pre-alpha. Many surfaces marked Fluid in STABILITY.md.
- **No Homebrew tap**: Project is desktop-only, tap not needed.
