// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
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

	// Send history from the session JSONL.
	history := readJSONLHistory(proc.JSONLPath())
	s.writeJSON(conn, ctx, map[string]any{
		"type":    "init",
		"version": s.version,
		"history": history,
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
		if strings.EqualFold(msg, "stop") {
			slog.Info("chat: interrupt")
			if err := proc.Interrupt(); err != nil {
				slog.Error("chat: interrupt failed", "err", err)
			}
			continue
		}

		slog.Info("chat: received", "msg", msg)
		if err := proc.Send(msg); err != nil {
			slog.Error("chat: send to claude failed", "err", err)
		}
	}
}

// readJSONLHistory reads user and assistant messages from the session JSONL.
func readJSONLHistory(path string) []map[string]any {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var history []map[string]any
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		typ, _ := entry["type"].(string)
		switch typ {
		case "user", "assistant":
			msg, _ := entry["message"].(map[string]any)
			if msg == nil {
				continue
			}
			role := "jevon"
			if typ == "user" {
				role = "user"
			}
			// User messages: content is a plain string.
			// Assistant messages: content is an array of {type, text} objects.
			switch content := msg["content"].(type) {
			case string:
				if content != "" {
					history = append(history, map[string]any{"role": role, "text": content})
				}
			case []any:
				for _, c := range content {
					cm, _ := c.(map[string]any)
					if cm["type"] == "text" {
						if text, _ := cm["text"].(string); text != "" {
							history = append(history, map[string]any{"role": role, "text": text})
						}
					}
				}
			}
		}
	}
	return history
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
