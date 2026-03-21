// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Command relay is a WebSocket relay that bridges jevond instances
// with iOS clients. Each jevond registers on startup and gets an
// instance ID. Clients connect to the instance by ID.
//
// Endpoints:
//
//	GET /health             — health check
//	GET /register           — jevond connects here (WebSocket upgrade)
//	GET /ws/<instance-id>   — iOS client connects here (WebSocket upgrade)
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"sync"

	"github.com/coder/websocket"
)

type instance struct {
	id   string
	conn *websocket.Conn
	ctx  context.Context
	mu   sync.Mutex
}

type relay struct {
	mu        sync.RWMutex
	instances map[string]*instance
}

func newRelay() *relay {
	return &relay{instances: make(map[string]*instance)}
}

func (r *relay) register(inst *instance) {
	r.mu.Lock()
	r.instances[inst.id] = inst
	r.mu.Unlock()
}

func (r *relay) unregister(id string) {
	r.mu.Lock()
	delete(r.instances, id)
	r.mu.Unlock()
}

func (r *relay) get(id string) *instance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.instances[id]
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	r := newRelay()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// jevond registers here. The relay assigns an instance ID and sends
	// it back as the first text message. Then the connection stays open
	// for bidirectional bridging with clients.
	mux.HandleFunc("GET /register", func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{
			OriginPatterns: []string{"*"},
		})
		if err != nil {
			slog.Error("register: accept failed", "err", err)
			return
		}
		defer conn.CloseNow()

		ctx := req.Context()
		id := generateID()

		// Send the instance ID back to jevond.
		if err := conn.Write(ctx, websocket.MessageText, []byte(id)); err != nil {
			return
		}

		inst := &instance{id: id, conn: conn, ctx: ctx}
		r.register(inst)
		defer r.unregister(id)

		slog.Info("instance registered", "id", id)

		// Keep alive until jevond disconnects. Server→client forwarding
		// is handled by the client bridge goroutine reading from this conn.
		<-ctx.Done()
		slog.Info("instance disconnected", "id", id)
	})

	// iOS client connects here to reach a specific jevond instance.
	mux.HandleFunc("GET /ws/{id}", func(w http.ResponseWriter, req *http.Request) {
		instanceID := req.PathValue("id")
		inst := r.get(instanceID)
		if inst == nil {
			http.Error(w, `{"error":"instance not found"}`, http.StatusNotFound)
			return
		}

		clientConn, err := websocket.Accept(w, req, &websocket.AcceptOptions{
			OriginPatterns: []string{"*"},
		})
		if err != nil {
			slog.Error("client: accept failed", "err", err)
			return
		}
		defer clientConn.CloseNow()

		ctx := req.Context()
		slog.Info("client connected", "instance", instanceID)

		// Bridge: bidirectional forwarding between client and jevond.

		// jevond → client
		go func() {
			for {
				mt, data, err := inst.conn.Read(inst.ctx)
				if err != nil {
					clientConn.Close(websocket.StatusGoingAway, "instance disconnected")
					return
				}
				if err := clientConn.Write(ctx, mt, data); err != nil {
					return
				}
			}
		}()

		// client → jevond
		for {
			mt, data, err := clientConn.Read(ctx)
			if err != nil {
				slog.Info("client disconnected", "instance", instanceID)
				return
			}
			inst.mu.Lock()
			err = inst.conn.Write(inst.ctx, mt, data)
			inst.mu.Unlock()
			if err != nil {
				slog.Warn("forward to instance failed", "instance", instanceID, "err", err)
				return
			}
		}
	})

	addr := ":" + port
	slog.Info("relay starting", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("relay failed", "err", err)
		os.Exit(1)
	}
}
