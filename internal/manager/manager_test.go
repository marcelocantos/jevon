package manager

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/jevon/internal/db"
	"github.com/marcelocantos/jevon/internal/discovery"
	"github.com/marcelocantos/jevon/internal/session"
)

func testDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func testScanner(t *testing.T) *discovery.Scanner {
	t.Helper()
	return discovery.NewScanner(t.TempDir())
}

func TestGetInMemory(t *testing.T) {
	m := New("opus", "/tmp", testDB(t), testScanner(t))

	// Inject a session directly.
	s := session.New(session.Config{
		ID:       "test-uuid-0000-0000-0000-000000000001",
		Name:     "test session",
		WorkDir:  "/tmp",
		Model:    "opus",
		ClaudeID: "test-uuid-0000-0000-0000-000000000001",
	})
	m.sessions["test-uuid-0000-0000-0000-000000000001"] = s

	got := m.Get("test-uuid-0000-0000-0000-000000000001")
	if got != s {
		t.Error("Get returned different session")
	}
}

func TestGetFromDiscovery(t *testing.T) {
	// Set up discovery directory with a session JSONL file.
	base := t.TempDir()
	projDir := filepath.Join(base, "-Users-test-project")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	uuid := "1b55f3b5-f771-42ea-883f-aa8a683ddf75"
	jsonl := `{"cwd":"/Users/test/project","gitBranch":"master"}` + "\n"
	if err := os.WriteFile(filepath.Join(projDir, uuid+".jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}

	scanner := discovery.NewScanner(base)
	m := New("opus", "/tmp", testDB(t), scanner)

	// Get should discover and lazily activate.
	got := m.Get(uuid)
	if got == nil {
		t.Fatal("Get returned nil for discoverable session")
	}
	if got.ID() != uuid {
		t.Errorf("ID = %q, want %q", got.ID(), uuid)
	}
	if got.Name() != "/Users/test/project" {
		t.Errorf("Name = %q, want %q", got.Name(), "/Users/test/project")
	}

	// Second Get should return same instance.
	got2 := m.Get(uuid)
	if got2 != got {
		t.Error("second Get returned different session")
	}
}

func TestGetNotFound(t *testing.T) {
	m := New("opus", "/tmp", testDB(t), testScanner(t))
	got := m.Get("ffffffff-ffff-ffff-ffff-ffffffffffff")
	if got != nil {
		t.Error("expected nil for unknown UUID")
	}
}

func TestGetNonUUID(t *testing.T) {
	m := New("opus", "/tmp", testDB(t), testScanner(t))
	got := m.Get("not-a-uuid")
	if got != nil {
		t.Error("expected nil for non-UUID ID")
	}
}

func TestListMergesDiscoveredAndActive(t *testing.T) {
	base := t.TempDir()
	projDir := filepath.Join(base, "-Users-test-project")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	uuid1 := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	uuid2 := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	for _, uuid := range []string{uuid1, uuid2} {
		jsonl := `{"cwd":"/Users/test/project","gitBranch":"master"}` + "\n"
		if err := os.WriteFile(filepath.Join(projDir, uuid+".jsonl"), []byte(jsonl), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	scanner := discovery.NewScanner(base)
	m := New("opus", "/tmp", testDB(t), scanner)

	// Activate one session.
	m.Get(uuid1)

	list := m.List(true)
	if len(list) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(list))
	}
}

func TestKill(t *testing.T) {
	m := New("opus", "/tmp", testDB(t), testScanner(t))

	s := session.New(session.Config{
		ID:       "deadbeef-dead-beef-dead-beefdeadbeef",
		Name:     "doomed",
		WorkDir:  "/tmp",
		ClaudeID: "deadbeef-dead-beef-dead-beefdeadbeef",
	})
	m.sessions["deadbeef-dead-beef-dead-beefdeadbeef"] = s

	if err := m.Kill("deadbeef-dead-beef-dead-beefdeadbeef"); err != nil {
		t.Fatalf("kill failed: %v", err)
	}
	if s.Status() != session.StatusStopped {
		t.Errorf("expected status %q, got %q", session.StatusStopped, s.Status())
	}
	if m.Get("deadbeef-dead-beef-dead-beefdeadbeef") != nil {
		// Should re-discover from disk if scanner has it, but our test scanner is empty.
	}
}

func TestKillNotFound(t *testing.T) {
	m := New("opus", "/tmp", testDB(t), testScanner(t))
	if err := m.Kill("nonexistent"); err == nil {
		t.Error("expected error killing nonexistent session")
	}
}

func TestSessionScore(t *testing.T) {
	// Larger files score higher at the same age.
	small := sessionScore(1024, 0)
	large := sessionScore(1024*1024, 0)
	if large <= small {
		t.Errorf("large (%f) should score higher than small (%f)", large, small)
	}

	// Log dampens size: 1MB vs 10MB should be much closer than 10x.
	s1MB := sessionScore(1024*1024, 0)
	s10MB := sessionScore(10*1024*1024, 0)
	ratio := s10MB / s1MB
	if ratio > 1.2 {
		t.Errorf("10MB/1MB score ratio = %f, expected < 1.2 (log dampening)", ratio)
	}

	// Recent scores higher than old at the same size.
	recent := sessionScore(10000, 0)
	old := sessionScore(10000, 7*24*time.Hour)
	if old >= recent {
		t.Errorf("old (%f) should score lower than recent (%f)", old, recent)
	}

	// After 1 day, score should be ~half (half-life = 1 day).
	oneDay := sessionScore(10000, 24*time.Hour)
	halfRatio := oneDay / recent
	if math.Abs(halfRatio-0.5) > 0.01 {
		t.Errorf("1-day decay ratio = %f, expected ~0.5", halfRatio)
	}

	// Zero-size files score 0.
	if s := sessionScore(0, 0); s != 0 {
		t.Errorf("zero-size score = %f, expected 0", s)
	}
}

func TestListSortedByScore(t *testing.T) {
	base := t.TempDir()
	projDir := filepath.Join(base, "-Users-test-project")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a large recent session and a small old session.
	recentUUID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	oldUUID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

	// Recent: large file.
	recentData := `{"cwd":"/Users/test/project"}` + "\n" + strings.Repeat("x", 10000)
	if err := os.WriteFile(filepath.Join(projDir, recentUUID+".jsonl"), []byte(recentData), 0o644); err != nil {
		t.Fatal(err)
	}

	// Old: small file, 30 days ago.
	oldData := `{"cwd":"/Users/test/project"}` + "\n"
	oldPath := filepath.Join(projDir, oldUUID+".jsonl")
	if err := os.WriteFile(oldPath, []byte(oldData), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(oldPath, oldTime, oldTime)

	scanner := discovery.NewScanner(base)
	m := New("opus", "/tmp", testDB(t), scanner)

	list := m.List(true)
	if len(list) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(list))
	}
	if list[0].ID != recentUUID {
		t.Errorf("expected recent session first, got %s", list[0].ID)
	}
	if list[0].Score <= list[1].Score {
		t.Errorf("first score (%f) should be > second (%f)", list[0].Score, list[1].Score)
	}
}

func TestListDefaultLimit(t *testing.T) {
	base := t.TempDir()
	projDir := filepath.Join(base, "-Users-test-project")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create more sessions than the default limit.
	for i := 0; i < DefaultListLimit+10; i++ {
		uuid := fmt.Sprintf("%08x-0000-0000-0000-%012x", i, i)
		data := `{"cwd":"/Users/test/project"}` + "\n" + strings.Repeat("x", 1000)
		if err := os.WriteFile(filepath.Join(projDir, uuid+".jsonl"), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	scanner := discovery.NewScanner(base)
	m := New("opus", "/tmp", testDB(t), scanner)

	// Default (not all) should be capped.
	list := m.List(false)
	if len(list) != DefaultListLimit {
		t.Errorf("default list: got %d, want %d", len(list), DefaultListLimit)
	}

	// All should return everything.
	listAll := m.List(true)
	if len(listAll) != DefaultListLimit+10 {
		t.Errorf("all list: got %d, want %d", len(listAll), DefaultListLimit+10)
	}
}
