// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"

	"github.com/coder/websocket"
	"github.com/fsnotify/fsnotify"
)

// DevServer serves static files from a directory and notifies
// connected clients to reload when files change.
type DevServer struct {
	dir     string
	handler http.Handler

	mu        sync.Mutex
	reloadChs []chan struct{}
}

// NewDevServer creates a dev server for the given directory.
func NewDevServer(dir string) *DevServer {
	return &DevServer{
		dir:     dir,
		handler: http.FileServer(http.Dir(dir)),
	}
}

// RegisterRoutes adds the static file server and reload WebSocket to the mux.
// Must be called before any other routes are registered — the file server
// acts as the fallback for unmatched paths.
func (ds *DevServer) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ws/reload", ds.handleReload)

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(ds.dir, "index.html"))
	})
}

// Watch starts watching the directory for changes and triggers reloads.
// Blocks until the watcher is closed.
func (ds *DevServer) Watch() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	if err := watcher.Add(ds.dir); err != nil {
		return err
	}
	slog.Info("dev server watching", "dir", ds.dir)

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				slog.Info("file changed, triggering reload", "file", event.Name)
				ds.triggerReload()
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			slog.Error("watcher error", "err", err)
		}
	}
}

func (ds *DevServer) handleReload(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	ch := make(chan struct{}, 1)
	ds.mu.Lock()
	ds.reloadChs = append(ds.reloadChs, ch)
	ds.mu.Unlock()

	defer func() {
		ds.mu.Lock()
		for i, c := range ds.reloadChs {
			if c == ch {
				ds.reloadChs = append(ds.reloadChs[:i], ds.reloadChs[i+1:]...)
				break
			}
		}
		ds.mu.Unlock()
	}()

	ctx := r.Context()
	select {
	case <-ctx.Done():
	case <-ch:
		conn.Write(ctx, websocket.MessageText, []byte("reload"))
	}
}

func (ds *DevServer) triggerReload() {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	for _, ch := range ds.reloadChs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	// Clear — each reload signal is consumed once, new connections
	// get the next one.
	ds.reloadChs = nil
}
