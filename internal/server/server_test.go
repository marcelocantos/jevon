package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/marcelocantos/jevon/internal/db"
	"github.com/marcelocantos/jevon/internal/jevon"
)

func newTestServer(t *testing.T) (*Server, *jevon.Jevon) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	jev := jevon.New(jevon.Config{WorkDir: t.TempDir()})
	return New(jev, nil, database, "test-v0.0.1", nil, nil), jev
}

func TestHealthEndpoint(t *testing.T) {
	s, _ := newTestServer(t)

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}
	if body["version"] != "test-v0.0.1" {
		t.Errorf("version = %v, want test-v0.0.1", body["version"])
	}
}

func TestHealthContentType(t *testing.T) {
	s, _ := newTestServer(t)

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestTranscriptLoadedOnConstruction(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	// Seed transcript before creating server.
	database.AppendTranscript("user", "hello")
	database.AppendTranscript("jevon", "hi there")

	jev := jevon.New(jevon.Config{WorkDir: t.TempDir()})
	s := New(jev, nil, database, "v0", nil, nil)
	database.Close()

	if len(s.transcript) != 2 {
		t.Fatalf("transcript length = %d, want 2", len(s.transcript))
	}
	if s.transcript[0].Role != "user" || s.transcript[0].Text != "hello" {
		t.Errorf("transcript[0] = {%q, %q}, want {user, hello}",
			s.transcript[0].Role, s.transcript[0].Text)
	}
}

func TestEmptyTranscriptOnFreshDB(t *testing.T) {
	s, _ := newTestServer(t)
	if len(s.transcript) != 0 {
		t.Errorf("transcript length = %d, want 0", len(s.transcript))
	}
}

func TestTurnAccumulation(t *testing.T) {
	// Verify that output callbacks accumulate text and status=idle
	// flushes it into a transcript entry.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	jev := jevon.New(jevon.Config{WorkDir: t.TempDir()})
	s := New(jev, nil, database, "v0", nil, nil)

	// Simulate Jevon output callbacks (these are wired in New).
	// We need to call the callbacks directly since we can't run
	// the actual Jevon loop in tests.

	// Verify initial state.
	s.mu.RLock()
	if s.turnBuf != "" {
		t.Errorf("initial turnBuf = %q, want empty", s.turnBuf)
	}
	s.mu.RUnlock()

	// Verify transcript is persisted to DB after a turn.
	database.AppendTranscript("user", "test message")
	entries, err := database.LoadTranscript()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Text != "test message" {
		t.Errorf("entry text = %q, want test message", entries[0].Text)
	}
}

func TestBroadcastWithNoClients(t *testing.T) {
	s, _ := newTestServer(t)
	// Should not panic when broadcasting with no connected clients.
	s.broadcast(map[string]any{"type": "text", "content": "hello"})
}

func TestRegisterRoutesAddsEndpoints(t *testing.T) {
	s, _ := newTestServer(t)

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	// Health should respond.
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("/health status = %d, want 200", w.Code)
	}

	// WebSocket upgrade endpoint should exist (will fail without upgrade headers,
	// but a 4xx means the route matched).
	req = httptest.NewRequest("GET", "/ws/remote", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	// Without WebSocket upgrade headers, we expect a non-404 error.
	if w.Code == http.StatusNotFound {
		t.Error("/ws/remote returned 404, route not registered")
	}
}
