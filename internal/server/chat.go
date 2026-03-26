// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/marcelocantos/jevon/internal/claude"
)

// SetProcess attaches the persistent Claude process for the /ws/chat endpoint.
func (s *Server) SetProcess(proc *claude.Process) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proc = proc
}

// handleChat is a direct WebSocket ↔ Claude PTY bridge.
// Client sends plain text messages, server sends raw JSONL events.
// No Lua, no sqlpipe, no viewstate — just the message exchange.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Error("chat: accept failed", "err", err)
		return
	}
	defer conn.CloseNow()

	conn.SetReadLimit(1 << 20)
	ctx := r.Context()
	slog.Info("chat client connected")

	s.mu.Lock()
	proc := s.proc
	s.mu.Unlock()

	if proc == nil || !proc.Alive() {
		conn.Close(websocket.StatusInternalError, "claude not running")
		return
	}

	// Send transcript history on connect.
	s.mu.RLock()
	hist := make([]TranscriptEntry, len(s.transcript))
	copy(hist, s.transcript)
	s.mu.RUnlock()

	s.writeJSON(conn, ctx, map[string]any{
		"type":    "init",
		"version": s.version,
		"history": hist,
	})

	// Subscribe to JSONL events from the Claude process.
	ch := make(chan string, 256)
	s.mu.Lock()
	s.chatListeners = append(s.chatListeners, ch)
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		for i, l := range s.chatListeners {
			if l == ch {
				s.chatListeners = append(s.chatListeners[:i], s.chatListeners[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		slog.Info("chat client disconnected")
	}()

	// Server → Client: forward JSONL events.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case line := <-ch:
				writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				if err := conn.Write(writeCtx, websocket.MessageText, []byte(line)); err != nil {
					cancel()
					return
				}
				cancel()
			}
		}
	}()

	// Client → Server: read messages and send to Claude PTY.
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			continue
		}
		slog.Info("chat: received", "msg", msg)

		// Persist to transcript.
		s.mu.Lock()
		s.transcript = append(s.transcript, TranscriptEntry{
			Role:      "user",
			Text:      msg,
			Timestamp: time.Now(),
		})
		s.mu.Unlock()

		if err := s.db.AppendTranscript("user", msg); err != nil {
			slog.Error("chat: persist failed", "err", err)
		}

		// Send to Claude.
		if err := proc.Send(msg); err != nil {
			slog.Error("chat: send to claude failed", "err", err)
		}
	}
}

// AppendTranscript adds an entry to the in-memory transcript and persists it.
func (s *Server) AppendTranscript(role, text string) {
	s.mu.Lock()
	s.transcript = append(s.transcript, TranscriptEntry{
		Role:      role,
		Text:      text,
		Timestamp: time.Now(),
	})
	s.mu.Unlock()
	if err := s.db.AppendTranscript(role, text); err != nil {
		slog.Error("persist transcript failed", "err", err)
	}
}

// BroadcastChat sends a JSONL line to all /ws/chat listeners.
func (s *Server) BroadcastChat(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.chatListeners {
		select {
		case ch <- line:
		default:
		}
	}
}
