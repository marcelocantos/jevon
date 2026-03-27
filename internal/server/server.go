// Package server implements the HTTP/WebSocket server for daisd,
// handling remote client connections and routing messages through
// the Jevon coordinator. Multiple clients can connect simultaneously
// and all observe the same session.
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"os"
	"path/filepath"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/marcelocantos/jevon/internal/claude"
	"github.com/marcelocantos/jevon/internal/db"
	"github.com/marcelocantos/jevon/internal/jevon"
	"github.com/marcelocantos/jevon/internal/manager"
	"github.com/marcelocantos/jevon/internal/ui"
)

// TranscriptEntry is a single turn in the conversation log.
type TranscriptEntry struct {
	Role      string    `json:"role"`      // "user" or "jevon"
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

// remoteWriter abstracts over WebSocket and tern relay connections.
type remoteWriter interface {
	WriteText(ctx context.Context, data []byte) error
	WriteBinary(ctx context.Context, data []byte) error
	Close() error
}

// wsWriter wraps a coder/websocket.Conn.
type wsWriter struct{ conn *websocket.Conn }

func (w wsWriter) WriteText(ctx context.Context, data []byte) error {
	return w.conn.Write(ctx, websocket.MessageText, data)
}
func (w wsWriter) WriteBinary(ctx context.Context, data []byte) error {
	return w.conn.Write(ctx, websocket.MessageBinary, data)
}
func (w wsWriter) Close() error { return w.conn.CloseNow() }

type remoteConn struct {
	writer remoteWriter
	ctx    context.Context
}

// Server is the daisd HTTP/WebSocket server.
type Server struct {
	jevon   *jevon.Jevon
	mgr     *manager.Manager
	db      *db.DB
	version string

	mu         sync.RWMutex
	remoteSeq  int
	remotes    map[int]remoteConn
	transcript []TranscriptEntry
	turnBuf    string // accumulates Jevon text for current turn

	luaRT     *ui.LuaRuntime
	viewState *ui.ViewState

	lastScreenshot string
	screenshotCh   chan string

	openAIKey     string // set via SetOpenAIKey
	proc          *claude.Process
	registry      *claude.Registry
	chatListeners []chan string
}

// New creates a Server with the given Jevon instance, manager, database, version string,
// Lua runtime, and view state. The Lua runtime and view state may be nil if the
// server-driven UI is not yet active.
func New(jev *jevon.Jevon, mgr *manager.Manager, database *db.DB, version string, luaRT *ui.LuaRuntime, vs *ui.ViewState) *Server {
	s := &Server{
		jevon:     jev,
		mgr:       mgr,
		db:        database,
		version:   version,
		remotes:   make(map[int]remoteConn),
		luaRT:     luaRT,
		viewState: vs,
	}

	// Load persisted transcript.
	if entries, err := database.LoadTranscript(); err != nil {
		slog.Error("failed to load transcript", "err", err)
	} else {
		for _, e := range entries {
			s.transcript = append(s.transcript, TranscriptEntry{
				Role:      e.Role,
				Text:      e.Text,
				Timestamp: e.CreatedAt,
			})
		}
		if len(s.transcript) > 0 {
			slog.Info("loaded transcript from database", "entries", len(s.transcript))
		}

		// Populate view state with persisted transcript.
		if vs != nil {
			for _, e := range entries {
				vs.AddMessage(e.Role, e.Text)
			}
			vs.SetConnected(version, os.Getenv("HOME"))
		}
	}

	// Wire Jevon callbacks once — they broadcast to all connected clients.
	jev.SetRawLog(func(line []byte) {
		if err := s.db.AppendRawLog("jevon", string(line)); err != nil {
			slog.Error("failed to persist raw log", "err", err)
		}
	})
	jev.SetOutput(func(text string) {
		s.mu.Lock()
		s.turnBuf += text
		s.mu.Unlock()

		s.Broadcast(map[string]any{
			"type":    "text",
			"content": text,
		})


		if s.viewState != nil {
			s.viewState.UpdateStreamingText(text)
			s.PushView()
		}
	})
	jev.SetStatus(func(state string) {
		if state == "idle" {
			s.mu.Lock()
			turnText := s.turnBuf
			if turnText != "" {
				s.transcript = append(s.transcript, TranscriptEntry{
					Role:      "jevon",
					Text:      turnText,
					Timestamp: time.Now(),
				})
				s.turnBuf = ""
			}
			s.mu.Unlock()

			if turnText != "" {
				if err := s.db.AppendTranscript("jevon", turnText); err != nil {
					slog.Error("failed to persist jevon turn", "err", err)
				}
			}

			if s.viewState != nil {
				s.viewState.FlushStreaming()
			}
		}

		s.Broadcast(map[string]any{
			"type":  "status",
			"state": state,
		})


		if s.viewState != nil {
			s.viewState.SetStatus(state)
			s.PushView()
		}
	})

	return s
}


// BroadcastBinary sends a binary WebSocket message to all connected clients.
func (s *Server) BroadcastBinary(data []byte) {
	if len(data) == 0 {
		return
	}

	s.mu.RLock()
	remotes := make([]remoteConn, 0, len(s.remotes))
	for _, rc := range s.remotes {
		remotes = append(remotes, rc)
	}
	s.mu.RUnlock()

	for _, rc := range remotes {
		writeCtx, cancel := context.WithTimeout(rc.ctx, 5*time.Second)
		if err := rc.writer.WriteBinary(writeCtx, data); err != nil {
			slog.Debug("binary broadcast write failed", "err", err)
		}
		cancel()
	}
}


// RegisterRoutes adds HTTP and WebSocket routes to the mux.
// Additional routes (e.g. MCP server) should be registered separately.
// Static file serving is handled by DevServer.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("/ws/chat", s.handleChat)
	mux.HandleFunc("/ws/remote", s.handleRemote)
	mux.HandleFunc("GET /api/agents", s.handleListAgents)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("POST /api/sessions/{id}/kill", s.handleKillSession)
	mux.HandleFunc("POST /api/realtime/token", s.handleRealtimeToken)
}

// handleRealtimeToken proxies an ephemeral token request to the OpenAI
// Realtime API. The API key stays on the server; the client gets a
// short-lived token for direct WebSocket connection to OpenAI.
func (s *Server) handleRealtimeToken(w http.ResponseWriter, r *http.Request) {
	apiKey := s.openAIKey
	if apiKey == "" {
		http.Error(w, `{"error":"OPENAI_API_KEY not configured (set env var or store in macOS Keychain: security add-generic-password -a jevon -s openai-api-key -w YOUR_KEY)"}`, http.StatusServiceUnavailable)
		return
	}

	body := `{"model":"gpt-4o-transcribe","voice":"alloy"}`
	req, err := http.NewRequestWithContext(r.Context(), "POST",
		"https://api.openai.com/v1/realtime/sessions", strings.NewReader(body))
	if err != nil {
		http.Error(w, `{"error":"request creation failed"}`, http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("openai realtime session failed", "err", err)
		http.Error(w, `{"error":"openai request failed"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// SetOpenAIKey sets the OpenAI API key for Realtime API token proxying.
func (s *Server) SetOpenAIKey(key string) { s.openAIKey = key }

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"version": s.version,
	})
}

func (s *Server) handleRemote(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Error("remote accept failed", "err", err)
		return
	}
	defer conn.CloseNow()

	conn.SetReadLimit(1 << 20) // 1 MB

	ctx := r.Context()

	// Register this connection.
	s.mu.Lock()
	s.remoteSeq++
	remoteID := s.remoteSeq
	s.remotes[remoteID] = remoteConn{writer: wsWriter{conn: conn}, ctx: ctx}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.remotes, remoteID)
		s.mu.Unlock()
	}()

	t0 := time.Now()
	slog.Info("remote connected", "clients", len(s.remotes))

	// Send init + history.
	s.mu.RLock()
	hist := make([]TranscriptEntry, len(s.transcript))
	copy(hist, s.transcript)
	s.mu.RUnlock()
	slog.Info("history gathered", "entries", len(hist), "elapsed", time.Since(t0))

	s.writeJSON(conn, ctx, map[string]any{
		"type":    "init",
		"version": s.version,
		"history": hist,
	})
	slog.Info("init sent", "elapsed", time.Since(t0))

	// Read loop: process messages from remote.
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Info("remote disconnected", "clients", len(s.remotes)-1)
			}
			return
		}

		// Skip binary messages.
		if mt == websocket.MessageBinary {
			continue
		}

		if mt != websocket.MessageText {
			continue
		}

		var msg struct {
			Type   string `json:"type"`
			Text   string `json:"text,omitempty"`
			Action string `json:"action,omitempty"`
			Value  string `json:"value,omitempty"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "control":
			s.handleControl(conn, ctx, msg.Action, msg.Value)

		case "message":
			s.HandleUserMessage(msg.Text)

		case "action":
			s.HandleAction(msg.Action, msg.Value)
		}
	}
}

// Broadcast sends a JSON message to all connected remote clients.
func (s *Server) Broadcast(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		slog.Error("marshal failed", "err", err)
		return
	}

	s.mu.RLock()
	remotes := make([]remoteConn, 0, len(s.remotes))
	for _, rc := range s.remotes {
		remotes = append(remotes, rc)
	}
	s.mu.RUnlock()

	for _, rc := range remotes {
		writeCtx, cancel := context.WithTimeout(rc.ctx, 5*time.Second)
		if err := rc.writer.WriteText(writeCtx, data); err != nil {
			slog.Debug("broadcast write failed", "err", err)
		}
		cancel()
	}
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "true"
	sessions := s.mgr.List(all)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess := s.mgr.Get(id)
	if sess == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{"error": "not found"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"id":          sess.ID(),
		"name":        sess.Name(),
		"status":      sess.Status(),
		"workdir":     sess.WorkDir(),
		"last_result": sess.LastResult(),
	})
}

func (s *Server) handleKillSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.mgr.Kill(id); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// HandleUserMessage processes a text message from a remote client.
func (s *Server) HandleUserMessage(text string) {
	if text == "" {
		return
	}

	s.mu.Lock()
	now := time.Now()
	s.transcript = append(s.transcript, TranscriptEntry{
		Role:      "user",
		Text:      text,
		Timestamp: now,
	})
	s.mu.Unlock()

	if err := s.db.AppendTranscript("user", text); err != nil {
		slog.Error("failed to persist user message", "err", err)
	}


	s.Broadcast(map[string]any{
		"type":      "user_message",
		"text":      text,
		"timestamp": now,
	})

	if s.viewState != nil {
		s.viewState.AddMessage("user", text)
		s.PushView()
	}

	s.jevon.Enqueue(jevon.Event{
		Kind: jevon.EventUserMessage,
		Text: text,
	})
}

// HandleAction processes a UI action from a remote client or a timer callback.
func (s *Server) HandleAction(action, value string) {
	if action == "" {
		return
	}
	slog.Debug("action received", "action", action, "value", value)

	// send_message always goes through HandleUserMessage for persistence
	// and broadcast, regardless of Lua handling.
	if action == "send_message" {
		s.HandleUserMessage(value)
		return
	}

	// Try Lua handler first.
	if s.luaRT != nil {
		if err := s.luaRT.CallAction(action, value); err == nil {
			return
		}
		// If Lua doesn't have handle_action or it errors, fall through to Go.
	}

	// Go fallback.
	switch {
	case action == "send_message":
		s.HandleUserMessage(value)

	case action == "show_sessions":
		s.PushSessions()

	case action == "dismiss_sheet":
		// Client handles dismiss locally when using client-side Lua.
		// Still support server-side fallback.
		if s.viewState != nil {
			s.viewState.SetSheet("")
			s.broadcastDismiss("sheet")
		}

	case action == "disconnect":
		slog.Info("disconnect requested via action")

	case len(action) > 13 && action[:13] == "kill_session:":
		sessionID := action[13:]
		if err := s.mgr.Kill(sessionID); err != nil {
			slog.Warn("kill session failed", "id", sessionID, "err", err)
		} else {
			s.PushSessions()
		}

	case action == "reload_views":
		if s.luaRT != nil {
			if err := s.luaRT.Reload(); err != nil {
				slog.Error("lua reload failed", "err", err)
			} else {
				s.PushScripts()
			}
		}

	default:
		slog.Warn("unknown action", "action", action)
	}
}

// handleControl processes control-channel messages that bypass the Lua layer.
// These are used for safe mode operations (rollback, version query, health).
func (s *Server) handleControl(conn *websocket.Conn, ctx context.Context, action, value string) {
	respond := func(v any) {
		data, err := json.Marshal(v)
		if err != nil {
			return
		}
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		conn.Write(writeCtx, websocket.MessageText, data)
	}

	switch action {
	case "health":
		respond(map[string]any{
			"type":   "control",
			"action": "health",
			"status": "ok",
		})

	case "list_snapshots":
		respond(map[string]any{
			"type":   "control",
			"action": "list_snapshots",
			"error":  "sync not available",
		})

	case "exec_lua":
		// Forward Lua code to all connected clients for execution.
		s.Broadcast(map[string]any{
			"type":   "control",
			"action": "exec_lua",
			"code":   value,
		})
		respond(map[string]any{
			"type":   "control",
			"action": "exec_lua",
			"status": "sent",
		})

	case "screenshot":
		// Forward screenshot request to all connected clients.
		s.Broadcast(map[string]any{
			"type":   "control",
			"action": "screenshot",
		})
		// Don't respond yet — the client will send screenshot_result.

	case "screenshot_result":
		// Client sent back a screenshot as base64 PNG in the value field.
		if value == "" {
			slog.Warn("screenshot_result: no data")
			return
		}
		imgData, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			slog.Error("screenshot_result: decode failed", "err", err)
			return
		}
		path := filepath.Join(os.TempDir(), "jevon-screenshot.png")
		if err := os.WriteFile(path, imgData, 0644); err != nil {
			slog.Error("screenshot_result: write failed", "err", err)
			return
		}
		slog.Info("screenshot saved", "path", path)
		s.mu.Lock()
		s.lastScreenshot = path
		if s.screenshotCh != nil {
			select {
			case s.screenshotCh <- path:
			default:
			}
		}
		s.mu.Unlock()

	case "rollback":
		respond(map[string]any{
			"type":   "control",
			"action": "rollback",
			"error":  "sync not available",
		})

	default:
		slog.Warn("unknown control action", "action", action)
	}
}

// RequestScreenshot sends a screenshot request to connected clients and waits
// for the result. Returns the file path of the saved PNG.
func (s *Server) RequestScreenshot(timeout time.Duration) (string, error) {
	ch := make(chan string, 1)
	s.mu.Lock()
	s.screenshotCh = ch
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.screenshotCh = nil
		s.mu.Unlock()
	}()

	s.Broadcast(map[string]any{
		"type":   "control",
		"action": "screenshot",
	})

	select {
	case path := <-ch:
		return path, nil
	case <-time.After(timeout):
		return "", fmt.Errorf("screenshot timeout")
	}
}

// PushView renders the current view state via Lua and broadcasts to all clients.
func (s *Server) PushView() {
	if s.luaRT == nil || s.viewState == nil {
		return
	}

	msgs, err := s.viewState.Render(s.luaRT)
	if err != nil {
		slog.Error("view render failed", "err", err)
		return
	}

	slog.Debug("pushView", "messages", len(msgs), "clients", len(s.remotes))
	for _, msg := range msgs {
		s.Broadcast(msg)
	}
}

// PushScripts broadcasts the Lua source to all connected clients for client-side rendering.
func (s *Server) PushScripts() {
	if s.luaRT == nil {
		return
	}
	source, err := s.luaRT.Scripts()
	if err != nil {
		slog.Error("failed to read lua scripts", "err", err)
		return
	}
	if source == "" {
		return
	}
	s.Broadcast(map[string]any{
		"type":   "scripts",
		"source": source,
	})
}

// PushSessions fetches the current session list and broadcasts it to all clients.
func (s *Server) PushSessions() {
	summaries := s.mgr.List(false)
	type sessionJSON struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Status  string `json:"status"`
		WorkDir string `json:"workdir"`
		Active  bool   `json:"active"`
	}
	entries := make([]sessionJSON, len(summaries))
	for i, sum := range summaries {
		entries[i] = sessionJSON{
			ID:      sum.ID,
			Name:    sum.Name,
			Status:  string(sum.Status),
			WorkDir: sum.WorkDir,
			Active:  sum.Active,
		}
	}
	s.Broadcast(map[string]any{
		"type":     "sessions",
		"sessions": entries,
	})
}

// refreshSessions fetches the current session list and updates the view state.
func (s *Server) refreshSessions() {
	if s.viewState == nil {
		return
	}
	summaries := s.mgr.List(false)
	entries := make([]ui.SessionEntry, len(summaries))
	for i, sum := range summaries {
		entries[i] = ui.SessionEntry{
			ID:      sum.ID,
			Name:    sum.Name,
			Status:  string(sum.Status),
			WorkDir: sum.WorkDir,
			Active:  sum.Active,
		}
	}
	s.viewState.SetSessions(entries)
}

// broadcastDismiss sends a dismiss message for the given slot.
func (s *Server) broadcastDismiss(slot string) {
	s.Broadcast(ui.DismissMessage{
		Type: "dismiss",
		Slot: slot,
	})
}

// writeJSON sends a JSON message to a single connection.
func (s *Server) writeJSON(conn *websocket.Conn, ctx context.Context, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		slog.Error("marshal failed", "err", err)
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := conn.Write(writeCtx, websocket.MessageText, data); err != nil {
		slog.Debug("write failed", "err", err)
	}
}
