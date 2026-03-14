# Convergence Targets

<!-- last-evaluated: 35cb8b6 -->

## Active

### 🎯T5 Authentication implemented

- **Value**: 8
- **Cost**: 13
- **Weight**: 0.6 (value 8 / cost 13)
- **Status**: identified
- **Discovered**: 2026-03-08

**Desired state:** mTLS with QR-based device provisioning secures all
surfaces. The `internal/auth` package is fully implemented.

**Acceptance criteria:**
- mTLS is enforced on all jevond endpoints (HTTP, WebSocket, MCP).
- QR-based device provisioning flow works end-to-end (scan QR on phone,
  device gets a client certificate).
- `internal/auth` package has tests covering the provisioning and
  verification paths.
- Unauthenticated requests are rejected.

### 🎯T6 Permission model enforced

- **Value**: 5
- **Cost**: 8
- **Weight**: 0.6 (value 5 / cost 8)
- **Status**: identified
- **Discovered**: 2026-03-08

**Desired state:** Neither Jevon nor workers run with blanket permission
bypass. Permission tiers from the trust model (🎯T4) are enforced.

**Acceptance criteria:**
- `--permission-mode bypassPermissions` is removed from Jevon's
  invocation in `internal/jevon/jevon.go`.
- `--dangerously-skip-permissions` is removed from worker spawning.
- Confirmation requests from Claude Code are routed to the user via
  the WebSocket protocol.
- Tests verify that permission-requiring actions trigger confirmation.

### 🎯T8 Target-driven session infrastructure

- **Value**: 47
- **Cost**: 47
- **Weight**: 1.0 (value 47 / cost 47)
- **Status**: identified — vision doc written (`docs/vision-v2.md`), decomposed into sub-targets
- **Discovered**: 2026-03-14

**Desired state:** Jevon is a session runtime where every agent is a
session with targets, capabilities, and provenance. cworkers and doit are
absorbed. Work is submitted via `jwork` and routed by the daemon to the
best session (existing idle, active related, or new). No rigid hierarchy
— structure emerges from the work.

**Acceptance criteria:**
- All sub-targets achieved.
- cworkers and doit repos archived after absorption complete.

**Design:** `docs/vision-v2.md`

#### 🎯T8.1 Session model foundation

- **Value**: 13
- **Cost**: 13
- **Weight**: 1.0 (value 13 / cost 13)
- **Parent**: 🎯T8
- **Gates**: 🎯T8.2, 🎯T8.3, 🎯T8.4, 🎯T8.5
- **Status**: identified
- **Discovered**: 2026-03-14

**Desired state:** Sessions are the universal primitive with targets,
capabilities, provenance, and directory context. The session registry
lives in SQLite. `jwork` MCP tool submits targets (initially routes to
new sessions only).

**Acceptance criteria:**
- `internal/session/` and `internal/manager/` rewritten around
  session-as-universal-primitive model.
- Session struct has: target(s), transcript ref, directory context,
  capabilities, provenance.
- Session registry in SQLite (not just transcript/KV).
- `jwork` MCP tool accepts a target and creates/routes to a session.
- "Jevon is a special coordinator session" concept removed from
  `internal/jevon/` — it becomes just another session.

#### 🎯T8.2 cworkers primitives absorbed

- **Value**: 13
- **Cost**: 13
- **Weight**: 1.0 (value 13 / cost 13)
- **Parent**: 🎯T8
- **Depends on**: 🎯T8.1
- **Status**: identified
- **Discovered**: 2026-03-14

**Desired state:** Pool, shadow, and dispatch infrastructure from
cworkers is integrated into jevond's session runtime.

**Acceptance criteria:**
- Session pool: pre-warmed `claude -p` processes, self-replenishing.
- Shadow registry: transcript tailing for context injection.
- Progress throttle: MCP heartbeats from worker output.
- SSE event hub: session lifecycle events broadcast to dashboard.
- Svelte dashboard adapted for session model (sessions, not workers).

#### 🎯T8.3 Execution safety absorbed (doit)

- **Value**: 8
- **Cost**: 8
- **Weight**: 1.0 (value 8 / cost 8)
- **Parent**: 🎯T8
- **Depends on**: 🎯T8.1
- **Status**: identified
- **Discovered**: 2026-03-14

**Desired state:** doit's policy engine operates as an execution safety
layer between sessions and the OS.

**Acceptance criteria:**
- Engine API (`Evaluate`, `Execute`) wired into session command execution.
- L1/L2/L3 policy chain operational.
- Hash-chained audit log integrated with session tracking.
- Capability registry configured.
- `jwork` results include policy decisions in metadata.

#### 🎯T8.4 Intelligent routing

- **Value**: 8
- **Cost**: 8
- **Weight**: 1.0 (value 8 / cost 8)
- **Parent**: 🎯T8
- **Depends on**: 🎯T8.1, 🎯T8.2
- **Status**: identified
- **Discovered**: 2026-03-14

**Desired state:** The daemon routes incoming work to the best session
based on context, scope overlap, and recency.

**Acceptance criteria:**
- Session metadata (target descriptions, scope, recency) indexed for
  routing decisions.
- Routing logic: match incoming targets to existing sessions.
- Reactivation: spin up `claude -p` against existing transcripts.
- Continuation support: related work routed to sessions with context.
- Foreman emergence: detect busy scopes, spawn coordinators.

#### 🎯T8.5 Metrics and analysis

- **Value**: 5
- **Cost**: 5
- **Weight**: 1.0 (value 5 / cost 5)
- **Parent**: 🎯T8
- **Depends on**: 🎯T8.1
- **Status**: identified
- **Discovered**: 2026-03-14

**Desired state:** Rich data capture enables model tier optimisation
and system performance analysis.

**Acceptance criteria:**
- Per-session token counts, activation counts, outcomes recorded.
- Routing decision logging and quality assessment.
- Cost aggregation per target.
- Dashboard analytics views.
- Foundation data for haiku introduction decision.

### 🎯T9 Server-driven UI for mobile app

- **Value**: 13
- **Cost**: 13
- **Weight**: 1.0 (value 13 / cost 13)
- **Tags**: visual
- **Status**: converging — server-side Lua rendering works end-to-end (current commit). Pivoting to client-side Lua execution.
- **Discovered**: 2026-03-14

**Desired state:** The iOS app is a programmable thin client. Lua view
scripts run on the device, rendering native SwiftUI from local state.
jevond pushes script updates and state changes; the phone renders
locally. Jevon (the AI agent) can modify scripts at runtime to reshape
the UI without app rebuilds or redeployment.

**Architecture:**
- **Client-side Lua.** The iOS app embeds a Lua runtime (C Lua, ~25KB).
  View scripts run on device against local state, producing view trees
  that the generic SwiftUI renderer displays. No server round-trip per
  render.
- **Script distribution.** jevond holds canonical scripts. On connect
  (or on change), pushes scripts to connected clients. Clients cache
  scripts locally for offline use.
- **State protocol.** Server sends structured state updates over
  WebSocket (new message, status change, session list diff). Client
  merges into local state and re-renders via Lua. Replaces streaming
  full view trees.
- **Primitives, not components.** View schema has fine-grained primitives
  (text, vstack, hstack, spacer, image, padding, background, etc.).
  "Chat bubble" is a composition defined in Lua, not a hardcoded client
  component.
- **Inline assets.** Images via SF Symbols (by name), data URIs (inline
  SVG/PNG), or bundled assets. Jevon can create novel icons without app
  bundling.
- **Dev flow.** Jevon edits scripts on the server → pushes draft to
  device → user previews → approves → promotes to live. Testing before
  releasing.
- **Reserved: `embed` component** for future ge wire protocol integration
  (game content rendered inline within server-driven UI).

**What exists (current commit):**
- Go: Lua runtime (gopher-lua), view state manager, view schema, MCP
  reload tool (`jevon_reload_views`)
- iOS: generic recursive SwiftUI renderer (`ServerView`), view/dismiss
  message handling
- Lua: screen scripts for chat, connect, sessions, session detail
- Server renders Lua → streams JSON view trees. This works but is the
  wrong architecture — should be client-side Lua.

**Remaining work:**
- Embed C Lua in iOS app (via SPM package or vendored source)
- Port view builder functions to Swift/C Lua bindings
- Change WebSocket protocol: server sends scripts + state updates,
  not rendered view trees
- Client runs Lua locally on state changes
- Add draft/preview/promote flow for script testing
- Smoke test: path abbreviation written by Jevon via conversation

**Acceptance criteria:**
- Lua runtime embedded in iOS app, running view scripts locally.
- jevond pushes script updates over WebSocket; client caches and
  executes them.
- Server sends state updates (messages, sessions, status), not view
  trees. Client renders locally from state.
- `jevon_reload_views` MCP tool pushes updated scripts to connected
  clients.
- Generic SwiftUI renderer maps Lua-produced view trees to native views.
- No business logic in Swift — all view logic in Lua scripts.
- Smoke test: Jevon writes path abbreviation via conversation, pushes
  script update, phone re-renders without app rebuild.

### 🎯T10 sqlpipe-based state sync

- **Value**: 13
- **Cost**: 13
- **Weight**: 1.0 (value 13 / cost 13)
- **Status**: identified
- **Discovered**: 2026-03-15

**Desired state:** All state synchronisation between jevond and the iOS
app flows through sqlpipe bidirectional peer sync over the existing
WebSocket. No application-level message protocol — the WebSocket is a
pure sqlpipe transport.

**Architecture:**
- **jevond is a sqlpipe Peer.** Server-owned tables: `transcript`
  (chat messages), `sessions` (worker list), `scripts` (Lua view
  source), `state` (server status, version). Writes trigger
  `flush()` → changeset streamed to client.
- **iOS app is a sqlpipe Peer.** Client-owned tables: `requests`
  (user messages, action triggers), `preferences` (client settings).
  Writes replicate to server → jevond processes them.
- **Diff sync on reconnect.** Client catches up via sqlpipe's
  hash-based diff protocol. No manual history replay needed.
- **Query subscriptions.** Client subscribes to queries; Lua scripts
  receive live query results as state. Re-render only when relevant
  data changes.
- **Lua state from queries.** Instead of a manually-built state dict,
  Lua screen functions receive query results directly from the
  replica's subscribed queries.
- **Local query + subscribe in Lua.** Lua scripts call `query(sql)`
  to read the local replica directly and `subscribe(sql)` to declare
  data dependencies. When subscribed queries' underlying tables change
  (via incoming sqlpipe changesets), the screen auto-re-renders. No
  polling, no manual refresh, no `push_sessions()` action. Data flow:
  changeset arrives → subscribed queries re-evaluate → Lua runs →
  SwiftUI renders.

**Integration:**
- sqlpipe Go wrapper (`go/sqlpipe/`) for jevond
- sqlpipe C++ API via bridging header for iOS (same as Lua vendoring)
- Replace all WebSocket message types (init, history, text, status,
  user_message, sessions, scripts, notification, view, dismiss, action)
  with table reads/writes
- jevond's existing SQLite DB becomes the sqlpipe master database

**Dependencies:** `marcelocantos/sqlpipe` (sibling repo)

**Acceptance criteria:**
- WebSocket carries only sqlpipe peer messages — no application-level
  JSON messages.
- Server writes to transcript/sessions/scripts/state tables; changes
  stream to client automatically.
- Client writes to requests table; server processes inserts as actions.
- Reconnect uses diff sync — no full state resend.
- Lua scripts render from query subscription results.
- Chat, sessions, and status all reflect server state reliably without
  manual push logic.

### 🎯T7 Mobile app for Jevon

- **Value**: 20
- **Cost**: 20
- **Weight**: 1.0 (value 20 / cost 20)
- **Tags**: visual
- **Status**: converging — Phase 1 (chat), Phase 2 (QR discovery), and Phase 3 (session list/management UI) done. Remaining: secure channel (depends on 🎯T5) and real-device testing on Pippa.
- **Discovered**: 2026-03-08

**Desired state:** A phone app provides a UI for interacting with
jevond — sending commands, viewing responses, and managing workers.

**Acceptance criteria:**
- Mobile app connects to jevond over a secure channel.
- User can send text commands and see streaming responses.
- User can view and manage worker sessions.
- App works on iOS (primary target: Pippa, iPad Air 5th gen).

## Achieved

### 🎯T4 Trust model defined for pre-1.0

Achieved. Trust model documented in `docs/trust-model.md` with three
permission tiers (autonomous, confirmed, prohibited) and WebSocket
confirmation flow. STABILITY.md updated to reference the design.


### 🎯T1 Jevon's tool surface is locked down [high]

Achieved in cf54767. All inappropriate tools disabled via `--disallowedTools`.

### 🎯T2 Conversational interaction model works end-to-end [high]

Achieved in 83cc4a4. AskUserQuestion disabled, CLAUDE.md template instructs
conversational questions.

### 🎯T3 Test coverage exists for core packages [medium]

Achieved in cf45460 + 6215164. 61 tests across 7 packages. All packages
with code have tests; untested packages (auth, cli, voice, worker) are
empty stubs.
