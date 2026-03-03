# jevon

Remote control for [Claude Code](https://docs.anthropic.com/en/docs/claude-code)
instances. Talk to a coordinator that manages Claude Code workers — from a
terminal, or (eventually) from your phone.

## How it works

```
  remote (TUI)  ──WebSocket──►  jevond  ──spawns──►  Jevon (Claude Code)
                                        ──manages──►  workers  (Claude Code)
                                   MCP ◄─────────────┘ (tool calls)
```

**jevond** is the coordinator daemon. It runs *Jevon* — a Claude Code
session that receives your messages and decides whether to answer directly or
delegate coding tasks to *worker* sessions. Jevon manages workers via
an in-process MCP server (no separate binary needed). Multiple clients can
connect simultaneously; messages and responses are broadcast to all.

**remote** is a terminal UI that connects to jevond over WebSocket. It renders
markdown responses, supports input history, and tracks unread messages when
you scroll up.

## Install

Download a binary from the
[latest release](https://github.com/marcelocantos/jevon/releases/latest)
(macOS arm64, Linux x86_64, Linux arm64), or build from source:

```bash
git clone https://github.com/marcelocantos/jevon.git
cd jevon
make jevond remote
```

Requires Go 1.22+ and a C compiler (CGo is needed for SQLite).

## Usage

```bash
# Start the coordinator
jevond --port 8080 --workdir ~/projects --model sonnet

# Connect from another terminal
remote --addr localhost:8080
```

Type a message and press Enter. Jevon will either answer directly or
spin up a Claude Code worker to handle the task. Results stream back in real
time.

### Flags

```
jevond:
  --port              Listen port (default 8080)
  --workdir           Default working directory for workers (default ".")
  --model             Default model for workers
  --jevon-model       Model for Jevon (default: same as --model)
  --debug             Enable debug logging
  --version           Print version and exit
  --help-agent        Print agent guide and exit

remote:
  --addr              jevond address (default "localhost:8080")
  --version           Print version and exit
  --help-agent        Print agent guide and exit
```

## Data

jevond stores its data in `~/.jevon/`:

| Path | Purpose |
|---|---|
| `jevon.db` | SQLite database (transcript, workers, raw logs) |
| `jevon/` | Jevon working directory and generated CLAUDE.md |
| `remote_history` | TUI input history |

## Agent integration

If you use an agentic coding tool, include
[`agents-guide.md`](agents-guide.md) in your project context for a detailed
reference. You can also run `jevond --help-agent` to get the same information.

## Licence

Apache 2.0 — see [LICENSE](LICENSE).
