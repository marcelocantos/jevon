# dais — Do As I Say!

Remote control for [Claude Code](https://docs.anthropic.com/en/docs/claude-code)
instances. Talk to a coordinator that manages Claude Code workers — from a
terminal, or (eventually) from your phone.

## How it works

```
  remote (TUI)  ──WebSocket──►  daisd  ──spawns──►  shepherd (Claude Code)
                                       ──manages──►  workers  (Claude Code)
                                       ◄──REST────  dais-ctl (used by shepherd)
```

**daisd** is the coordinator daemon. It runs a *shepherd* — a Claude Code
session that receives your messages and decides whether to answer directly or
delegate coding tasks to *worker* sessions. Multiple clients can connect
simultaneously; messages and responses are broadcast to all.

**remote** is a terminal UI that connects to daisd over WebSocket. It renders
markdown responses, supports input history, and tracks unread messages when
you scroll up.

**dais-ctl** is a helper CLI that the shepherd uses to manage workers
(create, list, command, wait, kill). You don't normally run it directly.

## Install

Download a binary from the
[latest release](https://github.com/marcelocantos/dais/releases/latest)
(macOS arm64, Linux x86_64, Linux arm64), or build from source:

```bash
git clone https://github.com/marcelocantos/dais.git
cd dais
make daisd dais-ctl remote
```

Requires Go 1.22+ and a C compiler (CGo is needed for SQLite).

## Usage

```bash
# Start the coordinator
daisd --port 8080 --workdir ~/projects --model sonnet

# Connect from another terminal
remote --addr localhost:8080
```

Type a message and press Enter. The shepherd will either answer directly or
spin up a Claude Code worker to handle the task. Results stream back in real
time.

### Flags

```
daisd:
  --port              Listen port (default 8080)
  --workdir           Default working directory for workers (default ".")
  --model             Default model for workers
  --shepherd-model    Model for the shepherd (default: same as --model)
  --debug             Enable debug logging
  --version           Print version and exit
  --help-agent        Print agent guide and exit

remote:
  --addr              daisd address (default "localhost:8080")
  --version           Print version and exit
  --help-agent        Print agent guide and exit
```

## Data

daisd stores its data in `~/.dais/`:

| Path | Purpose |
|---|---|
| `dais.db` | SQLite database (transcript, workers, raw logs) |
| `shepherd/` | Shepherd working directory and generated CLAUDE.md |
| `remote_history` | TUI input history |

## Agent integration

If you use an agentic coding tool, include
[`agents-guide.md`](agents-guide.md) in your project context for a detailed
reference. You can also run `daisd --help-agent` to get the same information.

## Licence

Apache 2.0 — see [LICENSE](LICENSE).
