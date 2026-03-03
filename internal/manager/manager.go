// Package manager provides multi-session lifecycle management,
// creating and tracking concurrent Claude Code sessions.
package manager

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/marcelocantos/jevon/internal/db"
	"github.com/marcelocantos/jevon/internal/discovery"
	"github.com/marcelocantos/jevon/internal/session"
)

// Half-life for session relevance decay (1 day).
var decayLambda = math.Ln2 / (24 * 60 * 60) // per second

// SessionEvent wraps a session event with its source session ID.
type SessionEvent struct {
	SessionID string
	Event     session.Event
}

// DefaultListLimit is the maximum number of sessions returned by List
// when not requesting all sessions.
const DefaultListLimit = 20

// SessionSummary is a lightweight view of a session for listing.
type SessionSummary struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Status    session.Status `json:"status"`
	WorkDir   string         `json:"workdir,omitempty"`
	ModTime   time.Time      `json:"mod_time,omitempty"`
	Active    bool           `json:"active,omitempty"`
	Score     float64        `json:"score"`
}

// CreateConfig holds parameters for creating a new session.
type CreateConfig struct {
	Name    string // human-readable name
	WorkDir string // working directory (default: ".")
	Model   string // model override (empty = manager default)
}

// Manager manages multiple concurrent Claude Code sessions.
type Manager struct {
	defaultModel string
	defaultDir   string
	db           *db.DB
	scanner      *discovery.Scanner

	mu       sync.RWMutex
	sessions map[string]*session.Session // keyed by UUID
}

// New creates a Manager with default configuration.
func New(defaultModel, defaultDir string, database *db.DB, scanner *discovery.Scanner) *Manager {
	return &Manager{
		defaultModel: defaultModel,
		defaultDir:   defaultDir,
		db:           database,
		scanner:      scanner,
		sessions:     make(map[string]*session.Session),
	}
}

// Create creates a new session by running claude to establish a session ID.
// The JSONL file Claude creates becomes the persistent record.
func (m *Manager) Create(cfg CreateConfig) (*session.Session, error) {
	model := cfg.Model
	if model == "" {
		model = m.defaultModel
	}
	workDir := cfg.WorkDir
	if workDir == "" {
		workDir = m.defaultDir
	}
	name := cfg.Name
	if name == "" {
		name = workDir
	}

	// Create a temporary session without a UUID — Run() will capture
	// the session ID from the init event.
	tmpID := fmt.Sprintf("creating-%d", time.Now().UnixNano())
	s := session.New(session.Config{
		ID:      tmpID,
		Name:    name,
		WorkDir: workDir,
		Model:   model,
	})
	s.SetRawLog(m.rawLogFunc(tmpID))

	events, err := s.Run(context.Background(), "Ready.")
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// Drain events to find the init event with the session UUID.
	var uuid string
	for ev := range events {
		if ev.Type == session.EventInit && ev.SessionID != "" {
			uuid = ev.SessionID
		}
	}

	if uuid == "" {
		return nil, fmt.Errorf("no session ID received from claude")
	}

	// Re-register under the real UUID.
	m.mu.Lock()
	s.SetRawLog(m.rawLogFunc(uuid))
	m.sessions[uuid] = s
	m.mu.Unlock()

	slog.Info("session created", "uuid", uuid, "name", name, "model", model)
	return s, nil
}

// Get returns a session by UUID. If the UUID is not in the active sessions
// map, it attempts to discover it from the filesystem and lazily activate it.
func (m *Manager) Get(id string) *session.Session {
	m.mu.RLock()
	if s, ok := m.sessions[id]; ok {
		m.mu.RUnlock()
		return s
	}
	m.mu.RUnlock()

	// Try to discover the session from the filesystem.
	if !discovery.IsUUID(id) {
		return nil
	}
	info, err := m.scanner.Get(id)
	if err != nil || info == nil {
		return nil
	}

	model := m.defaultModel
	s := session.New(session.Config{
		ID:       id,
		Name:     info.WorkDir,
		WorkDir:  info.WorkDir,
		Model:    model,
		ClaudeID: id,
	})
	s.SetRawLog(m.rawLogFunc(id))

	m.mu.Lock()
	// Double-check under write lock.
	if existing, ok := m.sessions[id]; ok {
		m.mu.Unlock()
		return existing
	}
	m.sessions[id] = s
	m.mu.Unlock()

	slog.Info("session activated from discovery", "uuid", id, "workdir", info.WorkDir)
	return s
}

// List returns a summary of all sessions, merging active in-memory sessions
// with discovered sessions from the filesystem, ranked by relevance.
// Score = log(size) * exp(-λ * age) with a 1-day half-life.
// Active sessions are pinned to the top. If all is false, results are
// capped at DefaultListLimit.
func (m *Manager) List(all bool) []SessionSummary {
	// Scan all sessions — scoring replaces the age filter.
	discovered, err := m.scanner.Scan(0)
	if err != nil {
		slog.Error("discovery scan failed", "err", err)
	}

	now := time.Now()

	m.mu.RLock()
	defer m.mu.RUnlock()

	// Build result from discovered sessions, overlaying active status.
	seen := make(map[string]bool)
	result := make([]SessionSummary, 0, len(discovered))

	for _, d := range discovered {
		seen[d.UUID] = true
		status := session.StatusIdle
		if s, ok := m.sessions[d.UUID]; ok {
			status = s.Status()
		}
		score := sessionScore(d.Size, now.Sub(d.ModTime))
		result = append(result, SessionSummary{
			ID:      d.UUID,
			Name:    d.WorkDir,
			Status:  status,
			WorkDir: d.WorkDir,
			ModTime: d.ModTime,
			Active:  d.Active,
			Score:   score,
		})
	}

	// Add any active sessions not found by discovery (e.g., just created).
	for id, s := range m.sessions {
		if seen[id] {
			continue
		}
		result = append(result, SessionSummary{
			ID:     id,
			Name:   s.Name(),
			Status: s.Status(),
			Score:  math.MaxFloat64, // just-created, pin high
		})
	}

	// Sort: active sessions first, then by score descending.
	sort.Slice(result, func(i, j int) bool {
		if result[i].Active != result[j].Active {
			return result[i].Active
		}
		return result[i].Score > result[j].Score
	})

	if !all && len(result) > DefaultListLimit {
		result = result[:DefaultListLimit]
	}

	return result
}

// sessionScore computes relevance as log(size) * exp(-λ * age).
func sessionScore(size int64, age time.Duration) float64 {
	if size <= 0 {
		return 0
	}
	return math.Log(float64(size)) * math.Exp(-decayLambda*age.Seconds())
}

func (m *Manager) rawLogFunc(sessionID string) session.RawLogFunc {
	return func(line []byte) {
		if err := m.db.AppendRawLog(sessionID, string(line)); err != nil {
			slog.Error("failed to persist raw log", "session", sessionID, "err", err)
		}
	}
}

// IsExternallyActive checks whether a session's JSONL file is currently
// open by another claude process (i.e., not managed by this jevond instance).
func (m *Manager) IsExternallyActive(id string) bool {
	// If we're managing this session and it's running, it's us — not external.
	m.mu.RLock()
	if s, ok := m.sessions[id]; ok && s.Status() == session.StatusRunning {
		m.mu.RUnlock()
		return false
	}
	m.mu.RUnlock()

	return m.scanner.IsActive(id)
}

// Kill stops a session and removes it from the active sessions map.
// The JSONL file remains on disk (session is still discoverable).
func (m *Manager) Kill(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session %q not found (not active)", id)
	}
	delete(m.sessions, id)
	m.mu.Unlock()

	s.Stop()
	slog.Info("session killed", "id", id)
	return nil
}
