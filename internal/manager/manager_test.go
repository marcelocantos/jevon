package manager

import (
	"testing"

	"github.com/marcelocantos/dais/internal/session"
)

func TestCreateAndGet(t *testing.T) {
	m := New("opus", "/tmp")
	s := m.Create(CreateConfig{Name: "test session"})

	if s.ID() == "" {
		t.Fatal("expected non-empty session ID")
	}
	if s.Name() != "test session" {
		t.Errorf("expected name %q, got %q", "test session", s.Name())
	}
	if s.Status() != session.StatusIdle {
		t.Errorf("expected status %q, got %q", session.StatusIdle, s.Status())
	}

	got := m.Get(s.ID())
	if got != s {
		t.Error("Get returned different session")
	}
}

func TestCreateDefaultName(t *testing.T) {
	m := New("opus", "/tmp")
	s := m.Create(CreateConfig{})
	if s.Name() == "" {
		t.Error("expected auto-generated name")
	}
}

func TestCreateMultiple(t *testing.T) {
	m := New("opus", "/tmp")
	s1 := m.Create(CreateConfig{Name: "first"})
	s2 := m.Create(CreateConfig{Name: "second"})

	if s1.ID() == s2.ID() {
		t.Error("sessions should have different IDs")
	}

	list := m.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(list))
	}
}

func TestKill(t *testing.T) {
	m := New("opus", "/tmp")
	s := m.Create(CreateConfig{Name: "doomed"})
	id := s.ID()

	if err := m.Kill(id); err != nil {
		t.Fatalf("kill failed: %v", err)
	}
	if s.Status() != session.StatusStopped {
		t.Errorf("expected status %q, got %q", session.StatusStopped, s.Status())
	}
	if m.Get(id) != nil {
		t.Error("session should be removed after kill")
	}
}

func TestKillNotFound(t *testing.T) {
	m := New("opus", "/tmp")
	if err := m.Kill("nonexistent"); err == nil {
		t.Error("expected error killing nonexistent session")
	}
}

func TestModelOverride(t *testing.T) {
	m := New("opus", "/tmp")
	s := m.Create(CreateConfig{Model: "haiku"})
	// The model is stored internally; verify session was created.
	if s.Status() != session.StatusIdle {
		t.Errorf("expected idle status")
	}
}

func TestList(t *testing.T) {
	m := New("opus", "/tmp")
	m.Create(CreateConfig{Name: "a"})
	m.Create(CreateConfig{Name: "b"})

	list := m.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(list))
	}

	names := map[string]bool{}
	for _, s := range list {
		names[s.Name] = true
	}
	if !names["a"] || !names["b"] {
		t.Errorf("expected sessions a and b, got %v", list)
	}
}
