# Jevon Agent Guide

Jevon is a remote control system for Claude Code instances.
It consists of a coordinator daemon (`jevond`) and a TUI client (`remote`).

## Architecture

```
  remote (TUI)  ──WebSocket──►  jevond  ──spawns──►  Jevon (Claude Code)
  phone app     ──WebSocket──►          ──manages──►  workers  (Claude Code)
                                   MCP ◄─────────────┘ (tool calls)
```

- **jevond**: HTTP/WebSocket server. Manages a Jevon session and worker
  pool. Exposes an in-process MCP server for Jevon → worker management.
  Streams transcript to connected clients. Stores conversation history
  and raw NDJSON logs in SQLite (`~/.jevon/jevon.db`).
- **remote**: Terminal UI client. Connects to jevond via WebSocket, renders
  markdown responses with glamour, supports input history and scroll.

## Running

```bash
# Start the coordinator (default port 8080)
jevond --port 8080 --workdir ~/projects --model sonnet

# Connect a terminal client
remote --addr localhost:8080
```

## Key concepts

- **Jevon**: A Claude Code session managed by jevond that coordinates
  workers. It receives user messages and decides whether to answer
  directly or delegate to workers. It manages workers via MCP tools
  provided by the Jevon server.
- **Workers**: Claude Code sessions that do actual coding work. Jevon
  creates and manages them via MCP tools.
- **Remote clients**: Multiple TUI or mobile clients can connect
  simultaneously. User messages and responses are broadcast to all.

## WebSocket protocol

Clients connect to `ws://<host>:<port>/ws/remote`. Messages are JSON:

```json
// Client → server
{"type": "message", "text": "build the login page"}

// Server → client
{"type": "text", "content": "partial markdown..."}
{"type": "status", "state": "thinking|idle"}
{"type": "error", "message": "something went wrong"}
{"type": "history", "entries": [{"role": "user|jevon", "text": "...", "timestamp": "..."}]}
{"type": "user_message", "text": "...", "timestamp": "..."}
```

## Configuration

- **`~/.jevon/`**: Data directory (SQLite DB, Jevon workdir, input history).
- **`~/.claude/managed-repos.md`**: Optional file listing repos Jevon
  should know about. Injected into Jevon's CLAUDE.md.

## Gotchas

- The C++ app (`bin/jevon`) requires Git LFS objects and is not included
  in release binaries. Only the Go binaries are distributed.
- Jevon's CLAUDE.md and .mcp.json are generated at startup and
  written to `~/.jevon/jevon/`. Do not edit them manually.
