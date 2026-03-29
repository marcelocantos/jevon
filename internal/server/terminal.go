// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
)

// handleAgentTerminal streams PTY output for a named agent via WebSocket.
// Query params: name (required), cols/rows (optional, for resize).
func (s *Server) handleAgentTerminal(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	reg := s.registry
	s.mu.RUnlock()

	if reg == nil {
		http.Error(w, "no registry", http.StatusServiceUnavailable)
		return
	}

	proc := reg.Get(name)
	if proc == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Error("terminal: accept failed", "err", err)
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()
	slog.Info("terminal connected", "agent", name)

	// Resize if cols/rows provided.
	if cols := r.URL.Query().Get("cols"); cols != "" {
		if rows := r.URL.Query().Get("rows"); rows != "" {
			c, _ := strconv.Atoi(cols)
			r2, _ := strconv.Atoi(rows)
			if c > 0 && r2 > 0 {
				proc.Resize(uint16(c), uint16(r2))
			}
		}
	}

	// Subscribe to terminal output.
	history, ch := proc.SubscribeTerminal()
	defer proc.UnsubscribeTerminal(ch)

	// Send buffered history first.
	if len(history) > 0 {
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn.Write(writeCtx, websocket.MessageBinary, history)
		cancel()
	}

	// Read loop: handle resize messages from client.
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var msg struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" && msg.Cols > 0 && msg.Rows > 0 {
				proc.Resize(uint16(msg.Cols), uint16(msg.Rows))
			}
		}
	}()

	// Stream PTY output to client.
	for {
		select {
		case <-ctx.Done():
			slog.Info("terminal disconnected", "agent", name)
			return
		case data, ok := <-ch:
			if !ok {
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := conn.Write(writeCtx, websocket.MessageBinary, data); err != nil {
				cancel()
				return
			}
			cancel()
		}
	}
}
