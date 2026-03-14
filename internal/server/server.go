// Package server implements the HTTP/WebSocket server for daisd,
// handling remote client connections and routing messages through
// the Jevon coordinator. Multiple clients can connect simultaneously
// and all observe the same session.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
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

type remoteConn struct {
	conn *websocket.Conn
	ctx  context.Context
}

// Server is the daisd HTTP/WebSocket server.
type Server struct {
	jevon   *jevon.Jevon
	mgr     *manager.Manager
	db      *db.DB
	version string

	mu         sync.RWMutex
	remotes    map[*websocket.Conn]remoteConn
	transcript []TranscriptEntry
	turnBuf    string // accumulates Jevon text for current turn

	luaRT     *ui.LuaRuntime
	viewState *ui.ViewState
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
		remotes:   make(map[*websocket.Conn]remoteConn),
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
			vs.SetConnected(version)
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

// RegisterRoutes adds HTTP and WebSocket routes to the mux.
// Additional routes (e.g. MCP server) should be registered separately.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("/ws/remote", s.handleRemote)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("POST /api/sessions/{id}/kill", s.handleKillSession)
}

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
	s.remotes[conn] = remoteConn{conn: conn, ctx: ctx}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.remotes, conn)
		s.mu.Unlock()
	}()

	slog.Info("remote connected", "clients", len(s.remotes))

	// Send init message.
	s.writeJSON(conn, ctx, map[string]any{
		"type":    "init",
		"version": s.version,
	})

	// Send transcript history.
	s.mu.RLock()
	hist := make([]TranscriptEntry, len(s.transcript))
	copy(hist, s.transcript)
	s.mu.RUnlock()

	if len(hist) > 0 {
		s.writeJSON(conn, ctx, map[string]any{
			"type":    "history",
			"entries": hist,
		})
	}

	// Send Lua view scripts for client-side rendering (preferred),
	// or fall back to server-rendered view trees.
	if s.luaRT != nil {
		if source, err := s.luaRT.Scripts(); err != nil {
			slog.Error("failed to read lua scripts", "err", err)
		} else if source != "" {
			s.writeJSON(conn, ctx, map[string]any{
				"type":   "scripts",
				"source": source,
			})
		}
	} else {
		s.PushView()
	}

	// Read loop: process messages from remote.
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Info("remote disconnected", "clients", len(s.remotes)-1)
			}
			return
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
		if err := rc.conn.Write(writeCtx, websocket.MessageText, data); err != nil {
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
