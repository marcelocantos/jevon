# dais — Do As I Say!

Remote control for Claude Code instances. Mobile UI rendered via ge engine's
WebGPU wire protocol; Go coordinator daemon manages Claude Code workers.

## Architecture

### C++ App (`bin/dais`)
- Phone UI rendered via ge's wire protocol (server on desktop, player on phone)
- Built on the ge engine submodule (`ge/`)
- Source in `src/`

### Go Coordinator (`bin/daisd`)
- HTTP/WebSocket server managing Claude Code workers
- Voice pipeline: AssemblyAI streaming STT → LLM cleanup → learning memory
- mTLS auth with QR-based device provisioning
- Exposed to the internet via ngrok
- Source in `cmd/daisd/` and `internal/`

### Communication
Currently independent processes. Future: the C++ app connects to daisd
for command routing and status updates.

## Build

```bash
make              # Build both components
make player       # Build squz player (desktop testing)
make daisd        # Build Go coordinator only
make bin/dais     # Build C++ app only (requires LFS libs)
```

**Note:** The C++ app requires Git LFS objects (Dawn, SDL3 static
libraries) in the ge submodule. Run `cd ge && git lfs pull` after
resolving any LFS quota issues.

## Run

```bash
# Terminal 1: C++ app (prints QR code for phone connection)
make run-app

# Terminal 2: desktop squz player
make player && bin/player

# Terminal 3: Go coordinator
make run-daisd

# Or run both dais + daisd together:
make run
```

## Developer Setup

```bash
make init         # Install prerequisites, generate compile_commands.json
```

Requires: macOS arm64, clang++ (C++20), Go 1.22+.

## ge Engine

The ge submodule provides the rendering engine. See `ge/CLAUDE.md` for full
documentation. Key integration points:

- `ge/Module.mk` included by the top-level Makefile
- `BUILD_DIR` and `CXX` defined before the include
- App links `$(ge/SESSION_WIRE_OBJ)` + `$(ge/LIB)` + `$(ge/DAWN_LIBS)` + `$(ge/SDL_LIBS)`
- The player is app-agnostic: `make player` builds it

## Code Conventions

### C++
- C++20, clang++
- **spdlog** for logging; use `SPDLOG_INFO`, `SPDLOG_WARN`, `SPDLOG_ERROR`
  macros (not `spdlog::info` etc.) for automatic source location
- **pImpl** for classes pulling in heavy headers (Dawn, SDL, asio)
- Platform-specific code in separate files, not `#ifdef` blocks
- Designated initializers for config structs

### Go
- **slog** for structured logging
- `cmd/` for binaries, `internal/` for packages
- `go test ./...` for all tests

### General
- Prefer `master` over `main` as default branch
- Prefer header-only libraries where suitable options exist
- Keep concerns modular and orthogonal

## Testing

```bash
make test         # Run all tests
make test-go      # Run Go tests only
```

## Project Structure

```
dais/
├── Makefile              # Build orchestration
├── go.mod                # Go module
├── CLAUDE.md             # This file
├── src/
│   ├── main.cpp          # ge app entry point
│   ├── App.h             # App class
│   └── App.cpp           # Render, update, event handling
├── cmd/
│   └── daisd/
│       └── main.go       # Go coordinator entry point
├── internal/
│   ├── server/           # HTTP/WebSocket server
│   ├── worker/           # Claude Code worker management
│   ├── voice/            # Voice pipeline (AssemblyAI + LLM cleanup)
│   ├── auth/             # mTLS + QR provisioning
│   └── db/               # SQLite learning/memory
└── ge/                   # Engine submodule (github.com/marcelocantos/ge)
```
