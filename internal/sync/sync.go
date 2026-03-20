// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package sync manages bidirectional sqlpipe replication between jevond
// and connected iOS clients. Both sides use sqlpipe Peer mode — the
// server approves client ownership requests and owns the complement.
package sync

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	gosync "sync"
	"time"

	"github.com/marcelocantos/sqlpipe/go/sqlpipe"
)

// Request is a decoded row from the client-owned requests table.
type Request struct {
	ID        int64
	Type      string
	Payload   string
	CreatedAt string
}

// SessionData holds session info for upserting into the sessions table.
type SessionData struct {
	ID      string
	Name    string
	Status  string
	WorkDir string
	Active  bool
	Score   float64
	ModTime string
}

// SyncManager coordinates sqlpipe Peer replication over a shared SQLite
// database. The server Peer approves client ownership requests and owns
// all tables not claimed by the client.
type SyncManager struct {
	sdb  *sqlpipe.Database
	peer *sqlpipe.Peer

	mu        gosync.Mutex
	onRequest func(Request)

	// Track last-seen request ID so we only fire callback for new rows.
	lastRequestID int64
}

// NewSyncManager opens (or reuses) the database, creates the sync schema,
// and initialises a Peer for bidirectional replication.
func NewSyncManager(dbPath string, onRequest func(Request)) (*SyncManager, error) {
	sdb, err := sqlpipe.OpenDatabase(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// Enable WAL mode.
	if err := sdb.Exec("PRAGMA journal_mode=WAL"); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}

	sm := &SyncManager{
		sdb:       sdb,
		onRequest: onRequest,
	}

	// Seed the last-seen request ID so we don't replay old rows on startup.
	qr, err := sdb.Query(`SELECT MAX(id) FROM requests`)
	if err == nil && len(qr.Rows) > 0 && qr.Rows[0][0] != nil {
		if v, ok := qr.Rows[0][0].(int64); ok {
			sm.lastRequestID = v
		}
	}

	logCb := func(level sqlpipe.LogLevel, msg string) {
		switch level {
		case sqlpipe.LogDebug:
			slog.Debug("sqlpipe", "msg", msg)
		case sqlpipe.LogInfo:
			slog.Info("sqlpipe", "msg", msg)
		case sqlpipe.LogWarn:
			slog.Warn("sqlpipe", "msg", msg)
		default:
			slog.Error("sqlpipe", "msg", msg)
		}
	}

	peer, err := sqlpipe.NewPeer(sdb, sqlpipe.PeerConfig{
		ApproveOwnership: func(requested map[string]bool) bool {
			return true // server approves all client ownership requests
		},
		OnConflict: func(ct sqlpipe.ConflictType, ce sqlpipe.ChangeEvent) sqlpipe.ConflictAction {
			slog.Warn("sqlpipe conflict", "type", ct, "table", ce.Table)
			return sqlpipe.ConflictReplace
		},
		OnLog: logCb,
	})
	if err != nil {
		sdb.Close()
		return nil, fmt.Errorf("new peer: %w", err)
	}
	sm.peer = peer

	return sm, nil
}

// DB returns the underlying sqlpipe Database for use by other packages
// that need direct database access.
func (sm *SyncManager) DB() *sqlpipe.Database { return sm.sdb }

// SeedTranscript copies messages from the legacy transcript table into
// sync_transcript if sync_transcript is empty. Called once at startup.
func (sm *SyncManager) SeedTranscript(legacyDB *sqlpipe.Database) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	qr, err := sm.sdb.Query("SELECT COUNT(*) FROM sync_transcript")
	if err != nil {
		return err
	}
	if len(qr.Rows) > 0 {
		if v, ok := qr.Rows[0][0].(int64); ok && v > 0 {
			return nil // already seeded
		}
	}

	for row := range legacyDB.Rows("SELECT role, text, created_at FROM transcript ORDER BY rowid") {
		if row.Err() != nil {
			return row.Err()
		}
		if err := sm.sdb.Exec(
			"INSERT INTO sync_transcript (role, content, timestamp) VALUES (?, ?, ?)",
			row.Text(0), row.Text(1), row.Text(2),
		); err != nil {
			return err
		}
	}
	return nil
}

// ── Wire framing ────────────────────────────────────────────────
//
// Over the WebSocket we send binary frames containing one or more
// PeerMessages. Each PeerMessage is length-prefixed (4 bytes LE)
// with a role tag, matching sqlpipe's native PeerMessage wire format.

// serializePeerMessages serializes a slice of PeerMessages into wire bytes.
func serializePeerMessages(msgs []sqlpipe.PeerMessage) []byte {
	if len(msgs) == 0 {
		return nil
	}
	var out []byte
	for _, msg := range msgs {
		out = append(out, sqlpipe.SerializePeer(msg)...)
	}
	return out
}

// DecodePeerMessages splits a binary WebSocket payload into PeerMessages.
func DecodePeerMessages(data []byte) ([]sqlpipe.PeerMessage, error) {
	var msgs []sqlpipe.PeerMessage
	pos := 0
	for pos < len(data) {
		if pos+4 > len(data) {
			return nil, fmt.Errorf("truncated frame at offset %d", pos)
		}
		msgLen := binary.LittleEndian.Uint32(data[pos:])
		total := 4 + int(msgLen)
		if pos+total > len(data) {
			return nil, fmt.Errorf("truncated message at offset %d", pos)
		}
		pm, err := sqlpipe.DeserializePeer(data[pos : pos+total])
		if err != nil {
			return nil, fmt.Errorf("deserialize at offset %d: %w", pos, err)
		}
		msgs = append(msgs, pm)
		pos += total
	}
	return msgs, nil
}

// ── Handshake ───────────────────────────────────────────────────

// Hello returns initial data to send to a newly connected client.
// The server peer does NOT call Start() — the client initiates the
// handshake. The server just flushes any pending changes so the
// client's first HandleMessage can process them.
func (sm *SyncManager) Hello() ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Reset the peer for the new connection.
	sm.peer.Reset()

	// Flush any pending changes so they're ready when the client
	// sends its Start messages.
	return sm.flushLocked()
}

// ── Message handling ────────────────────────────────────────────

// HandleMessage processes an incoming binary WebSocket frame from a client.
// Returns response bytes to send back, if any.
func (sm *SyncManager) HandleMessage(data []byte) ([]byte, error) {
	peerMsgs, err := DecodePeerMessages(data)
	if err != nil {
		return nil, err
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	var out []byte
	for _, pm := range peerMsgs {
		hr, err := sm.peer.HandleMessage(pm)
		if err != nil {
			slog.Error("peer handle error", "err", err)
			continue
		}
		out = append(out, serializePeerMessages(hr.Messages)...)

		// Check for new requests after processing client messages.
		if pm.SenderRole == sqlpipe.RoleAsMaster {
			sm.processNewRequests()
		}
	}

	return out, nil
}

// processNewRequests scans the requests table for rows with id > lastRequestID
// and fires the onRequest callback. Must be called under sm.mu.
func (sm *SyncManager) processNewRequests() {
	if sm.onRequest == nil {
		return
	}
	for row := range sm.sdb.Rows(
		`SELECT id, type, payload, created_at FROM requests WHERE id > ? ORDER BY id`,
		sm.lastRequestID,
	) {
		if row.Err() != nil {
			slog.Error("query new requests", "err", row.Err())
			return
		}
		r := Request{
			ID:        row.Int64(0),
			Type:      row.Text(1),
			Payload:   row.Text(2),
			CreatedAt: row.Text(3),
		}
		sm.lastRequestID = r.ID
		sm.onRequest(r)
	}
}

// ── State writes (server-owned tables) ──────────────────────────

// Flush extracts pending Peer changes and returns wire bytes to
// broadcast to all connected clients.
func (sm *SyncManager) Flush() ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	return sm.flushLocked()
}

// WriteTranscript inserts a message into sync_transcript and flushes.
func (sm *SyncManager) WriteTranscript(role, content string) ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if err := sm.sdb.Exec(
		`INSERT INTO sync_transcript (role, content) VALUES (?, ?)`,
		role, content,
	); err != nil {
		return nil, fmt.Errorf("insert transcript: %w", err)
	}
	return sm.flushLocked()
}

// WriteServerState updates the server_state singleton and flushes.
func (sm *SyncManager) WriteServerState(status, streamingText string) ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if err := sm.sdb.Exec(
		`UPDATE server_state SET status = ?, streaming_text = ? WHERE id = 1`,
		status, streamingText,
	); err != nil {
		return nil, fmt.Errorf("update server_state: %w", err)
	}
	return sm.flushLocked()
}

// AppendStreamingText appends to streaming_text (for incremental output).
func (sm *SyncManager) AppendStreamingText(text string) ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if err := sm.sdb.Exec(
		`UPDATE server_state SET streaming_text = streaming_text || ? WHERE id = 1`,
		text,
	); err != nil {
		return nil, fmt.Errorf("append streaming_text: %w", err)
	}
	return sm.flushLocked()
}

// ClearStreamingText resets streaming_text to empty.
func (sm *SyncManager) ClearStreamingText() ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if err := sm.sdb.Exec(
		`UPDATE server_state SET streaming_text = '' WHERE id = 1`,
	); err != nil {
		return nil, fmt.Errorf("clear streaming_text: %w", err)
	}
	return sm.flushLocked()
}

// WriteSessions upserts all session rows and flushes.
func (sm *SyncManager) WriteSessions(sessions []SessionData) ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, s := range sessions {
		modTime := s.ModTime
		if modTime == "" {
			modTime = now
		}
		active := 0
		if s.Active {
			active = 1
		}
		if err := sm.sdb.Exec(
			`INSERT INTO sessions (id, name, status, workdir, active, score, mod_time)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET
				name     = excluded.name,
				status   = excluded.status,
				workdir  = excluded.workdir,
				active   = excluded.active,
				score    = excluded.score,
				mod_time = excluded.mod_time`,
			s.ID, s.Name, s.Status, s.WorkDir, active, s.Score, modTime,
		); err != nil {
			return nil, fmt.Errorf("upsert session %s: %w", s.ID, err)
		}
	}
	return sm.flushLocked()
}

// WriteScripts upserts a Lua script and flushes.
func (sm *SyncManager) WriteScripts(name, source string) ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if err := sm.sdb.Exec(
		`INSERT INTO scripts (name, source, updated_at)
		 VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		 ON CONFLICT(name) DO UPDATE SET
			source     = excluded.source,
			updated_at = excluded.updated_at`,
		name, source,
	); err != nil {
		return nil, fmt.Errorf("upsert script %s: %w", name, err)
	}
	return sm.flushLocked()
}

// SetVersion sets the version in server_state.
func (sm *SyncManager) SetVersion(version string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	return sm.sdb.Exec(
		`UPDATE server_state SET version = ? WHERE id = 1`, version)
}

// flushLocked flushes the peer and returns wire bytes. Caller must hold sm.mu.
func (sm *SyncManager) flushLocked() ([]byte, error) {
	msgs, err := sm.peer.Flush()
	if err != nil {
		return nil, fmt.Errorf("flush: %w", err)
	}
	return serializePeerMessages(msgs), nil
}

// Close releases all resources.
func (sm *SyncManager) Close() error {
	if sm.peer != nil {
		sm.peer.Close()
	}
	if sm.sdb != nil {
		return sm.sdb.Close()
	}
	return nil
}
