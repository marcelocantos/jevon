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

// SetRegistry attaches the agent registry for the /api/agents endpoint.
func (s *Server) SetRegistry(reg *claude.Registry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registry = reg
}

// handleListAgents returns all registered agents with their status.
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	reg := s.registry
	s.mu.RUnlock()

	if reg == nil {
		json.NewEncoder(w).Encode([]any{})
		return
	}

	defs := reg.List()
	type agentInfo struct {
		Name    string `json:"name"`
		WorkDir string `json:"workdir"`
		Parent  string `json:"parent,omitempty"`
		Status  string `json:"status"`
	}

	agents := make([]agentInfo, len(defs))
	for i, d := range defs {
		status := "stopped"
		if proc := reg.Get(d.Name); proc != nil && proc.Alive() {
			status = "running"
		}
		agents[i] = agentInfo{
			Name:    d.Name,
			WorkDir: d.WorkDir,
			Parent:  d.Parent,
			Status:  status,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(agents)
}

// handleChat is a direct WebSocket ↔ Claude PTY bridge.
// Client sends plain text messages, server sends raw JSONL lines.
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

	// Send JSONL history as raw lines.
	sendHistory(conn, ctx, proc.JSONLPath())

	// Subscribe to live JSONL events from the Claude process.
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

	// Server → Client: forward raw JSONL lines.
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

// sendHistory reads the JSONL file and sends each line as a raw WebSocket message.
func sendHistory(conn *websocket.Conn, ctx context.Context, path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn.Write(writeCtx, websocket.MessageText, line)
		cancel()
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
