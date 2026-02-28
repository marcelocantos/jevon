// Package server implements the HTTP/WebSocket server for daisd,
// handling API endpoints and real-time communication with remote
// clients across multiple Claude Code sessions.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/marcelocantos/dais/internal/manager"
	"github.com/marcelocantos/dais/internal/session"
)

// Server is the daisd HTTP/WebSocket server.
type Server struct {
	mgr     *manager.Manager
	version string

	mu     sync.Mutex
	remote *websocket.Conn
}

// New creates a Server with the given manager and version string.
func New(mgr *manager.Manager, version string) *Server {
	return &Server{
		mgr:     mgr,
		version: version,
	}
}

// RegisterRoutes adds all HTTP and WebSocket routes to the mux.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("/ws/remote", s.handleRemote)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"version": s.version,
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"sessions": s.mgr.List(),
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

	s.mu.Lock()
	if s.remote != nil {
		s.remote.Close(websocket.StatusGoingAway, "replaced by new connection")
	}
	s.remote = conn
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if s.remote == conn {
			s.remote = nil
		}
		s.mu.Unlock()
	}()

	slog.Info("remote connected")

	// Send init message.
	s.writeJSON(conn, r.Context(), map[string]any{
		"type":    "init",
		"version": s.version,
	})

	// Read loop: process messages from remote.
	ctx := r.Context()
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Info("remote disconnected")
			}
			return
		}
		if mt != websocket.MessageText {
			continue
		}

		var msg struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id,omitempty"`
			Text      string `json:"text,omitempty"`
			Name      string `json:"name,omitempty"`
			Model     string `json:"model,omitempty"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "create_session":
			s.handleCreateSession(ctx, conn, msg.Name, msg.Model)

		case "command":
			s.handleCommand(ctx, conn, msg.SessionID, msg.Text)

		case "cancel":
			s.handleCancel(msg.SessionID)

		case "kill_session":
			s.handleKillSession(ctx, conn, msg.SessionID)

		case "list_sessions":
			s.writeJSON(conn, ctx, map[string]any{
				"type":     "session_list",
				"sessions": s.mgr.List(),
			})
		}
	}
}

func (s *Server) handleCreateSession(ctx context.Context, conn *websocket.Conn, name, model string) {
	sess := s.mgr.Create(manager.CreateConfig{
		Name:  name,
		Model: model,
	})

	s.writeJSON(conn, ctx, map[string]any{
		"type":       "session_created",
		"session_id": sess.ID(),
		"name":       sess.Name(),
	})
}

func (s *Server) handleCommand(ctx context.Context, conn *websocket.Conn, sessionID, text string) {
	sess := s.mgr.Get(sessionID)
	if sess == nil {
		s.writeJSON(conn, ctx, map[string]any{
			"type":       "error",
			"session_id": sessionID,
			"message":    "session not found",
		})
		return
	}

	s.writeJSON(conn, ctx, map[string]any{
		"type":       "status",
		"session_id": sessionID,
		"state":      "running",
	})

	events, err := sess.Run(ctx, text)
	if err != nil {
		s.writeJSON(conn, ctx, map[string]any{
			"type":       "error",
			"session_id": sessionID,
			"message":    err.Error(),
		})
		s.writeJSON(conn, ctx, map[string]any{
			"type":       "status",
			"session_id": sessionID,
			"state":      "idle",
		})
		return
	}

	for ev := range events {
		switch ev.Type {
		case session.EventText:
			s.writeJSON(conn, ctx, map[string]any{
				"type":       "text",
				"session_id": sessionID,
				"content":    ev.Content,
			})
		case session.EventToolUse:
			s.writeJSON(conn, ctx, map[string]any{
				"type":       "tool_use",
				"session_id": sessionID,
				"id":         ev.ToolID,
				"name":       ev.ToolName,
				"input":      ev.ToolInput,
			})
		case session.EventResult:
			s.writeJSON(conn, ctx, map[string]any{
				"type":        "done",
				"session_id":  sessionID,
				"duration_ms": ev.DurationMs,
				"cost_usd":    ev.CostUSD,
			})
		case session.EventError:
			s.writeJSON(conn, ctx, map[string]any{
				"type":       "error",
				"session_id": sessionID,
				"message":    ev.ErrorMsg,
			})
		}
	}

	s.writeJSON(conn, ctx, map[string]any{
		"type":       "status",
		"session_id": sessionID,
		"state":      string(sess.Status()),
	})
}

func (s *Server) handleCancel(sessionID string) {
	sess := s.mgr.Get(sessionID)
	if sess == nil {
		return
	}
	if err := sess.Cancel(); err != nil {
		slog.Warn("cancel failed", "session", sessionID, "err", err)
	}
}

func (s *Server) handleKillSession(ctx context.Context, conn *websocket.Conn, sessionID string) {
	if err := s.mgr.Kill(sessionID); err != nil {
		s.writeJSON(conn, ctx, map[string]any{
			"type":       "error",
			"session_id": sessionID,
			"message":    err.Error(),
		})
		return
	}
	s.writeJSON(conn, ctx, map[string]any{
		"type":       "status",
		"session_id": sessionID,
		"state":      "stopped",
	})
}

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
