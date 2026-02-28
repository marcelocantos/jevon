// Package server implements the HTTP/WebSocket server for daisd,
// handling the remote client connection and routing messages through
// the shepherd coordinator.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/marcelocantos/dais/internal/db"
	"github.com/marcelocantos/dais/internal/shepherd"
)

// TranscriptEntry is a single turn in the conversation log.
type TranscriptEntry struct {
	Role string `json:"role"` // "user" or "shepherd"
	Text string `json:"text"`
}

// Server is the daisd HTTP/WebSocket server.
type Server struct {
	shepherd *shepherd.Shepherd
	db       *db.DB
	version  string

	mu         sync.Mutex
	remote     *websocket.Conn
	transcript []TranscriptEntry
	turnBuf    string // accumulates shepherd text for current turn
}

// New creates a Server with the given shepherd, database, and version string.
// It loads any existing transcript from the database.
func New(shep *shepherd.Shepherd, database *db.DB, version string) *Server {
	s := &Server{
		shepherd: shep,
		db:       database,
		version:  version,
	}

	// Load persisted transcript.
	if entries, err := database.LoadTranscript(); err != nil {
		slog.Error("failed to load transcript", "err", err)
	} else {
		for _, e := range entries {
			s.transcript = append(s.transcript, TranscriptEntry{
				Role: e.Role,
				Text: e.Text,
			})
		}
		if len(s.transcript) > 0 {
			slog.Info("loaded transcript from database", "entries", len(s.transcript))
		}
	}

	return s
}

// RegisterRoutes adds HTTP and WebSocket routes to the mux.
// Additional routes (e.g. ctlapi) should be registered separately.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("/ws/remote", s.handleRemote)
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

	// Wire shepherd output to this connection.
	ctx := r.Context()
	s.shepherd.SetOutput(func(text string) {
		s.mu.Lock()
		s.turnBuf += text
		s.mu.Unlock()

		s.writeJSON(conn, ctx, map[string]any{
			"type":    "text",
			"content": text,
		})
	})
	s.shepherd.SetStatus(func(state string) {
		if state == "idle" {
			s.mu.Lock()
			turnText := s.turnBuf
			if turnText != "" {
				s.transcript = append(s.transcript, TranscriptEntry{
					Role: "shepherd",
					Text: turnText,
				})
				s.turnBuf = ""
			}
			s.mu.Unlock()

			if turnText != "" {
				if err := s.db.AppendTranscript("shepherd", turnText); err != nil {
					slog.Error("failed to persist shepherd turn", "err", err)
				}
			}
		}

		s.writeJSON(conn, ctx, map[string]any{
			"type":  "status",
			"state": state,
		})
	})

	// Send init message.
	s.writeJSON(conn, ctx, map[string]any{
		"type":    "init",
		"version": s.version,
	})

	// Send transcript history.
	s.mu.Lock()
	hist := make([]TranscriptEntry, len(s.transcript))
	copy(hist, s.transcript)
	s.mu.Unlock()

	if len(hist) > 0 {
		s.writeJSON(conn, ctx, map[string]any{
			"type":    "history",
			"entries": hist,
		})
	}

	// Read loop: process messages from remote.
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
			Type string `json:"type"`
			Text string `json:"text,omitempty"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "message":
			if msg.Text != "" {
				s.mu.Lock()
				s.transcript = append(s.transcript, TranscriptEntry{
					Role: "user",
					Text: msg.Text,
				})
				s.mu.Unlock()

				if err := s.db.AppendTranscript("user", msg.Text); err != nil {
					slog.Error("failed to persist user message", "err", err)
				}

				s.shepherd.Enqueue(shepherd.Event{
					Kind: shepherd.EventUserMessage,
					Text: msg.Text,
				})
			}
		}
	}
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
