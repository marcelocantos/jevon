// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/marcelocantos/tern"
)

// ternWriter adapts a tern.Conn to the remoteWriter interface.
type ternWriter struct{ conn *tern.Conn }

func (w ternWriter) WriteText(ctx context.Context, data []byte) error {
	return w.conn.Send(ctx, data)
}
func (w ternWriter) WriteBinary(ctx context.Context, data []byte) error {
	return w.conn.Send(ctx, data)
}
func (w ternWriter) Close() error { return w.conn.Close() }

// ConnectRelay registers with a tern relay server and bridges traffic.
// Returns the instance ID.
func (s *Server) ConnectRelay(ctx context.Context, relayURL, token string) (string, error) {
	slog.Info("connecting to relay", "url", relayURL)

	var opts []tern.Option
	opts = append(opts, tern.WithTLS(&tls.Config{InsecureSkipVerify: true}))
	if token != "" {
		opts = append(opts, tern.WithToken(token))
	}

	conn, err := tern.Register(ctx, relayURL, opts...)
	if err != nil {
		return "", err
	}

	instanceID := conn.InstanceID()
	slog.Info("registered with relay", "instance_id", instanceID)

	// Register as a virtual remote client.
	s.mu.Lock()
	s.remoteSeq++
	remoteID := s.remoteSeq
	s.remotes[remoteID] = remoteConn{writer: ternWriter{conn: conn}, ctx: ctx}
	s.mu.Unlock()

	// Send init + history + scripts.
	s.sendJSON(ctx, conn, map[string]any{
		"type":    "init",
		"version": s.version,
		"home":    os.Getenv("HOME"),
	})

	s.mu.RLock()
	hist := make([]TranscriptEntry, len(s.transcript))
	copy(hist, s.transcript)
	s.mu.RUnlock()

	if len(hist) > 0 {
		s.sendJSON(ctx, conn, map[string]any{
			"type":    "history",
			"entries": hist,
		})
	}

	if s.luaRT != nil {
		if source, err := s.luaRT.Scripts(); err != nil {
			slog.Error("relay: failed to read lua scripts", "err", err)
		} else if source != "" {
			s.sendJSON(ctx, conn, map[string]any{
				"type":   "scripts",
				"source": source,
			})
		}
	}

	// Read loop: process messages from the relay.
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.remotes, remoteID)
			s.mu.Unlock()
			conn.Close()
			slog.Info("relay connection closed", "instance_id", instanceID)
		}()

		for {
			data, err := conn.Recv(ctx)
			if err != nil {
				if ctx.Err() == nil {
					slog.Warn("relay read error", "err", err)
				}
				return
			}

			// Try JSON text message first.
			var msg struct {
				Type   string `json:"type"`
				Action string `json:"action"`
				Value  string `json:"value"`
				Text   string `json:"text"`
			}
			if err := json.Unmarshal(data, &msg); err != nil {
				// Binary frame (sqlpipe sync).
				if s.syncMgr != nil {
					if resp, err := s.syncMgr.HandleMessage(data); err != nil {
						slog.Error("relay: sync receive failed", "err", err)
					} else if len(resp) > 0 {
						sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
						conn.Send(sendCtx, resp)
						cancel()
					}
				}
				continue
			}

			switch msg.Type {
			case "action":
				s.HandleAction(msg.Action, msg.Value)
			case "user_message":
				s.HandleUserMessage(msg.Text)
			}
		}
	}()

	return instanceID, nil
}

func (s *Server) sendJSON(ctx context.Context, conn *tern.Conn, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		slog.Error("relay: marshal failed", "err", err)
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := conn.Send(sendCtx, data); err != nil {
		slog.Error("relay: send failed", "err", err)
	}
}
