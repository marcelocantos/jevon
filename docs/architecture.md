# Claude Code Farm — Architecture

## Context

Control multiple concurrent Claude Code sessions from an iPad (or Android device) without manually switching between them. Permission prompts from all sessions surface in a single unified queue. The server runs locally on the Mac; the mobile client connects over an ngrok tunnel authenticated via mTLS with client certs delivered by QR code.

---

## System overview

```
┌─────────────────────────────────────────────────────────┐
│  Mac (local)                                            │
│                                                         │
│  ┌───────────────────────────────────────────────────┐  │
│  │  Orchestrator (Bun + TypeScript)                  │  │
│  │                                                   │  │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐          │  │
│  │  │ Session 1│ │ Session 2│ │ Session N│  Agent SDK│  │
│  │  └────┬─────┘ └────┬─────┘ └────┬─────┘          │  │
│  │       │             │            │                │  │
│  │  ┌────▼─────────────▼────────────▼─────┐          │  │
│  │  │  Session Manager                    │          │  │
│  │  │  - lifecycle (spawn/kill/restart)   │          │  │
│  │  │  - permission queue (aggregate)     │          │  │
│  │  │  - output buffer (per-session)      │          │  │
│  │  └────────────────┬────────────────────┘          │  │
│  │                   │                               │  │
│  │  ┌────────────────▼────────────────────┐          │  │
│  │  │  API Layer (Hono on Bun)            │          │  │
│  │  │  - REST: sessions, permissions      │          │  │
│  │  │  - WebSocket: streaming, events     │          │  │
│  │  │  - mTLS termination                 │          │  │
│  │  └────────────────┬────────────────────┘          │  │
│  │                   │                               │  │
│  │  ┌────────────────▼────────────────────┐          │  │
│  │  │  Auth & Tunnel                      │          │  │
│  │  │  - CA + client cert generation      │          │  │
│  │  │  - QR code serving (local only)     │          │  │
│  │  │  - ngrok TLS tunnel                 │          │  │
│  │  └────────────────┬────────────────────┘          │  │
│  └───────────────────│───────────────────────────────┘  │
│                      │                                  │
└──────────────────────│──────────────────────────────────┘
                       │ ngrok TLS tunnel
                       │
              ┌────────▼────────┐
              │  Flutter App    │
              │  (iPad/Android) │
              │                 │
              │  - Dashboard    │
              │  - Perm queue   │
              │  - Voice I/O    │
              └─────────────────┘
```

---

## Components

### 1. Orchestrator server (Bun + TypeScript)

**Runtime**: Bun (native TLS, fast startup, TS without transpile step)
**Framework**: Hono (lightweight, Bun-native, WebSocket support)
**SDK**: `@anthropic-ai/claude-agent-sdk`

#### 1.1 Session Manager

Each "session" wraps a Claude Agent SDK `query()` call running as an async generator.

```
Session {
  id: string (ulid)
  name: string (user-assigned)
  status: "running" | "awaiting_permission" | "idle" | "stopped" | "error"
  cwd: string (working directory)
  createdAt: Date
  model: string
  allowedTools: string[]
  prompt: string

  // Runtime state
  agentStream: AsyncGenerator  // from SDK query()
  outputLog: RingBuffer<Event> // capped scrollback
  pendingPermission: PermissionRequest | null
}
```

**Lifecycle operations**:
- `spawn(config)` — Start a new SDK session. The SDK's `query()` returns an async iterable of events. The manager consumes it in a background loop, buffering output and intercepting permission requests.
- `sendInput(sessionId, text)` — Send follow-up input to a running session (continue conversation).
- `respondPermission(sessionId, requestId, approved)` — Resolve a pending permission callback. The SDK provides a callback/promise mechanism for tool approval; the manager holds the promise until the mobile client responds.
- `kill(sessionId)` — Abort the async generator, clean up.
- `restart(sessionId)` — Kill + respawn with same config.

**Permission interception**: The Agent SDK surfaces tool calls before execution. The manager:
1. Pauses the session (holds the approval promise).
2. Pushes a `PermissionRequest` to the global queue.
3. Notifies connected clients via WebSocket.
4. Waits for client response.
5. Resolves the promise (approve/deny) to resume the agent.

```
PermissionRequest {
  id: string
  sessionId: string
  sessionName: string
  tool: string          // "Bash", "Edit", "Write", etc.
  input: object         // tool parameters (command, file_path, etc.)
  timestamp: Date
  status: "pending" | "approved" | "denied"
}
```

#### 1.2 API layer (Hono)

**REST endpoints**:

```
POST   /sessions              Create session {name, cwd, prompt, model?, allowedTools?}
GET    /sessions              List all sessions (summary)
GET    /sessions/:id          Session detail + recent output
POST   /sessions/:id/input    Send follow-up prompt
POST   /sessions/:id/kill     Kill session
POST   /sessions/:id/restart  Restart session
DELETE /sessions/:id          Kill + remove

GET    /permissions           List pending permissions (all sessions)
POST   /permissions/:id       Respond {approved: bool, reason?: string}

GET    /auth/qr               Serve QR code (local network only — bound to LAN IP)
POST   /auth/revoke           Revoke a client cert by fingerprint
```

**WebSocket** (`/ws`):

Bidirectional channel for real-time events. After mTLS handshake, the client receives:

```
// Server → Client events
{type: "session_output",    sessionId, data: AgentEvent}
{type: "permission_request", request: PermissionRequest}
{type: "session_status",    sessionId, status, ...}
{type: "permission_resolved", requestId, approved}

// Client → Server events
{type: "subscribe",   sessionIds: string[] | "*"}
{type: "unsubscribe", sessionIds: string[]}
```

Clients subscribe to specific sessions or all (`"*"`). Output streams only for subscribed sessions (saves bandwidth on mobile).

#### 1.3 Auth & tunnel

**Certificate chain**:

```
Farm CA (generated on first run, stored in ~/.claude-farm/ca/)
  └── Client cert (generated per QR scan, stored in ~/.claude-farm/clients/)
```

**First-run setup**:
1. Generate a self-signed CA key + cert (Node `forge` or `openssl` subprocess).
2. Start Hono server with TLS using the CA.
3. Start ngrok TLS tunnel pointing to the local HTTPS port.
4. Generate QR code containing a JSON payload:

```
{
  "url": "https://<random>.ngrok-free.app",
  "cert": "<base64 PKCS12 client cert + key>",
  "ca": "<base64 CA cert>",
  "pin": "<CA public key SHA-256 pin>"
}
```

5. Display QR code in the terminal (`qrcode-terminal`).

**New device pairing**:
- Run `farm pair` → generates a new client cert, displays QR.
- Each client cert has a unique CN (device name + timestamp).
- `farm revoke <fingerprint>` revokes a cert.

**mTLS enforcement**:
- Bun's `Bun.serve()` supports TLS options including `requestCert` and `ca` for client cert verification.
- Every request (REST + WebSocket upgrade) must present a valid client cert signed by the farm CA.
- The `/auth/qr` endpoint is the ONE exception: it binds to `127.0.0.1` or LAN IP only (a separate listener, not exposed through ngrok).

---

### 2. Flutter mobile app

#### 2.1 Core architecture

**State management**: Riverpod (mature, good async support)
**Networking**: `dio` (HTTP with mTLS) + `web_socket_channel` (WebSocket)
**Local storage**: `flutter_secure_storage` (cert + key), `hive` (session cache)

#### 2.2 Screens

**Pairing screen** (shown once):
- Camera opens for QR scan (`mobile_scanner` package).
- Parses JSON payload, extracts PKCS12 cert and CA.
- Stores cert in secure storage.
- Tests connection to orchestrator.
- On success, transitions to dashboard.

**Dashboard** (main screen):
- Top bar: connection status indicator, + button to spawn new session.
- Left rail (iPad) / bottom tabs (phone): session list with status badges.
  - Green dot: running
  - Orange dot: awaiting permission
  - Grey dot: idle/stopped
- Centre: selected session's output (scrollable, monospace).
- Floating action area: permission badge count.

**Permission queue** (slide-over panel or dedicated tab):
- Chronological list of pending `PermissionRequest`s.
- Each card shows: session name, tool name, input summary (truncated), timestamp.
- Tap to expand full input details.
- Swipe right to approve, swipe left to deny (with haptics).
- Batch approve/deny for same-tool-type requests.
- Auto-dismiss after 30s timeout → deny (configurable).

**Session detail**:
- Full streaming output (ANSI rendered via a terminal widget or simplified markdown).
- Input bar at bottom to send follow-up prompts.
- Toolbar: kill, restart, view config.
- Status header with model, CWD, token usage.

**New session dialog**:
- Fields: name, working directory (picker from known projects), prompt, model dropdown, allowed tools (multi-select chips).
- Templates: save/load common configurations.

#### 2.3 Voice I/O (future)

- **Speech-to-text**: Platform STT (`speech_to_text` package) for prompt dictation.
- **Text-to-speech**: Platform TTS (`flutter_tts`) for reading session output / permission summaries.
- **Hands-free mode**: Wake word → listen → send as session input. Permission prompts read aloud, approve/deny by voice command.
- Implementation deferred to a later phase; architecture supports it via the same API (POST /sessions/:id/input, POST /permissions/:id).

#### 2.4 mTLS client implementation

Flutter's `SecurityContext` supports client certificates:
```dart
SecurityContext context = SecurityContext();
context.setTrustedCertificatesBytes(caCertBytes);
context.useCertificateChainBytes(clientCertBytes);
context.usePrivateKeyBytes(clientKeyBytes);
```

This context is passed to `HttpClient` (used by `dio`) and the WebSocket connection. Certificate bytes are loaded from `flutter_secure_storage` after pairing.

---

### 3. CLI (`farm` command)

The orchestrator doubles as a CLI for local management:

```
farm start           Start the orchestrator daemon
farm stop            Stop the daemon
farm pair            Generate QR code for new device
farm revoke <fp>     Revoke client cert
farm sessions        List active sessions
farm spawn <opts>    Spawn a session from CLI
farm kill <id>       Kill a session
farm logs <id>       Tail session output
farm status          Daemon + tunnel + session summary
```

Implemented as subcommands in the same Bun binary. `farm start` runs the server in foreground (use `&` or a launchd plist for background).

---

## Data flow: permission request lifecycle

```
1. Agent SDK session N calls Bash("rm -rf /tmp/old")
       │
2. Session Manager intercepts tool call
   (SDK callback / permission handler)
       │
3. Manager creates PermissionRequest, adds to global queue
       │
4. WebSocket push → {type: "permission_request", ...}
       │
5. Flutter app receives, shows in permission queue
   (notification badge increments)
       │
6. User swipes to approve
       │
7. Flutter sends POST /permissions/:id {approved: true}
       │
8. API resolves the held promise in Session Manager
       │
9. SDK resumes execution of Bash tool
       │
10. Output streams back → WebSocket → Flutter
```

---

## Data flow: QR pairing

```
1. User runs `farm pair` on Mac terminal
       │
2. Server generates new client key + cert, signed by Farm CA
       │
3. Server encodes {ngrok_url, pkcs12, ca_cert, pin} as JSON → QR
       │
4. QR displayed in terminal
       │
5. Flutter app scans QR with camera
       │
6. App extracts PKCS12, imports to secure storage
       │
7. App connects to ngrok_url with client cert
       │
8. Server validates client cert against CA → mTLS handshake succeeds
       │
9. App transitions to dashboard
```

---

## Key dependencies

### Server (Bun)
- `@anthropic-ai/claude-agent-sdk` — Agent sessions
- `hono` — HTTP/WebSocket framework
- `node-forge` — Certificate generation (or shell out to `openssl`)
- `qrcode-terminal` — QR display in CLI
- `ngrok` — Tunnel management (`@ngrok/ngrok` npm package)

### Flutter
- `dio` — HTTP client with mTLS support
- `web_socket_channel` — WebSocket
- `mobile_scanner` — QR code scanning
- `flutter_secure_storage` — Cert storage
- `flutter_riverpod` — State management
- `google_fonts` — Monospace font for terminal output
- `speech_to_text` / `flutter_tts` — Voice I/O (future)

---

## File structure (server)

```
claude-farm/
├── src/
│   ├── index.ts              Entry point, CLI arg parsing
│   ├── server.ts             Hono app setup, TLS config
│   ├── sessions/
│   │   ├── manager.ts        Session lifecycle, permission queue
│   │   ├── session.ts        Single session wrapper around SDK query()
│   │   └── types.ts          Session, PermissionRequest, Event types
│   ├── auth/
│   │   ├── ca.ts             CA generation, client cert signing
│   │   ├── qr.ts             QR payload generation + display
│   │   └── mtls.ts           mTLS middleware for Hono
│   ├── tunnel/
│   │   └── ngrok.ts          ngrok tunnel setup + URL management
│   ├── api/
│   │   ├── sessions.ts       REST routes for /sessions
│   │   ├── permissions.ts    REST routes for /permissions
│   │   └── ws.ts             WebSocket handler
│   └── cli/
│       ├── start.ts          farm start
│       ├── pair.ts           farm pair
│       └── commands.ts       Other CLI subcommands
├── package.json
├── tsconfig.json
└── bunfig.toml
```

## File structure (Flutter)

```
claude_farm_app/
├── lib/
│   ├── main.dart
│   ├── app.dart                  MaterialApp, routing
│   ├── config/
│   │   └── theme.dart
│   ├── models/
│   │   ├── session.dart          Session data model
│   │   └── permission.dart       PermissionRequest model
│   ├── services/
│   │   ├── api_client.dart       dio + mTLS setup
│   │   ├── ws_client.dart        WebSocket connection + reconnect
│   │   ├── cert_store.dart       Secure storage for certs
│   │   └── audio_service.dart    Voice I/O (future)
│   ├── providers/
│   │   ├── connection.dart       Connection state
│   │   ├── sessions.dart         Session list + detail
│   │   └── permissions.dart      Permission queue
│   ├── screens/
│   │   ├── pairing_screen.dart   QR scan + onboarding
│   │   ├── dashboard_screen.dart Main session grid
│   │   ├── session_screen.dart   Single session detail
│   │   └── permissions_screen.dart Permission queue
│   └── widgets/
│       ├── session_card.dart
│       ├── permission_card.dart
│       ├── terminal_output.dart  Monospace scrollable output
│       └── status_badge.dart
├── pubspec.yaml
└── analysis_options.yaml
```

---

## Security considerations

- **QR is the trust root**: The PKCS12 in the QR code contains the client's private key. QR should only be displayed on the local terminal, never served over the network.
- **Cert revocation**: Server maintains a revocation list in `~/.claude-farm/revoked.json`. Checked on every TLS handshake.
- **ngrok URL rotation**: Each `farm start` gets a new ngrok URL (unless using a reserved domain). Old URLs stop working immediately.
- **No secrets in transit**: The mTLS channel encrypts everything. The ngrok tunnel adds another TLS layer (double encryption).
- **Session isolation**: Each SDK session runs in its own async context. A killed session cannot affect others.
- **Permission timeout**: Unanswered permission requests auto-deny after a configurable timeout (default 5 minutes) to prevent sessions from hanging indefinitely.

---

## Verification

1. **Server standalone**: `farm start` → confirm TLS listener + ngrok tunnel + QR display.
2. **Pairing**: Scan QR from Flutter app → confirm mTLS handshake succeeds.
3. **Session spawn**: POST /sessions from app → confirm SDK session starts, output streams via WebSocket.
4. **Permission flow**: Trigger a tool that needs approval → confirm it appears in app → approve → confirm session resumes.
5. **Multi-session**: Spawn 3+ sessions → confirm all outputs stream independently, permissions queue correctly.
6. **Kill/restart**: Kill a session from app → confirm clean shutdown, no orphaned processes.
7. **Cert revocation**: Revoke a cert → confirm app can no longer connect.
