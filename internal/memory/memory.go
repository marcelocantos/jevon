// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package memory provides a searchable transcript index across all
// Claude Code sessions. It ingests JSONL files from ~/.claude/projects/
// and maintains a realtime FTS5 index.
package memory

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	_ "github.com/mattn/go-sqlite3"
)

// Store is a searchable index of Claude Code transcripts.
type Store struct {
	db         *sql.DB
	projectDir string

	mu      sync.Mutex
	offsets map[string]int64 // file path → last read offset
}

// SearchResult is a single search hit.
type SearchResult struct {
	SessionID string  `json:"session_id"`
	Project   string  `json:"project"`
	Role      string  `json:"role"`
	Text      string  `json:"text"`
	Timestamp string  `json:"timestamp"`
	Rank      float64 `json:"rank"`
}

// SessionInfo is a summary of a transcript session.
type SessionInfo struct {
	SessionID       string `json:"session_id"`
	Project         string `json:"project"`
	SessionType     string `json:"session_type"`
	TotalMsgs       int    `json:"total_msgs"`
	SubstantiveMsgs int    `json:"substantive_msgs"`
	FirstMsg        string `json:"first_msg"`
	LastMsg         string `json:"last_msg"`
}

// TypeStats holds per-session-type statistics.
type TypeStats struct {
	SessionType     string `json:"session_type"`
	Sessions        int    `json:"sessions"`
	TotalMsgs       int    `json:"total_msgs"`
	SubstantiveMsgs int    `json:"substantive_msgs"`
	NoiseMsgs       int    `json:"noise_msgs"`
}

// StatsResult holds full memory statistics.
type StatsResult struct {
	TotalSessions int         `json:"total_sessions"`
	TotalMessages int         `json:"total_messages"`
	ByType        []TypeStats `json:"by_type"`
}

// sessionTypeSQL returns a SQL CASE expression for deriving session type.
// The col parameter is the qualified column name (e.g. "m.project" or "project").
func sessionTypeSQL(col string) string {
	return `CASE
	WHEN ` + col + ` = 'subagents' THEN 'subagent'
	WHEN ` + col + ` LIKE '%worktrees%' THEN 'worktree'
	WHEN ` + col + ` LIKE '%-private-tmp%' THEN 'ephemeral'
	ELSE 'interactive'
END`
}

// New creates or opens a transcript memory store.
func New(dbPath, projectDir string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, err
	}

	// Create base tables.
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			project TEXT NOT NULL,
			role TEXT NOT NULL,
			text TEXT NOT NULL,
			timestamp TEXT,
			type TEXT
		);
		CREATE TABLE IF NOT EXISTS ingest_state (
			path TEXT PRIMARY KEY,
			offset INTEGER NOT NULL
		);
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create tables: %w", err)
	}

	// Migrate: add is_noise column if missing.
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	// Ensure FTS table and trigger exist (idempotent).
	_, err = db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
			text, role, project, session_id,
			content=messages,
			content_rowid=id
		);
		DROP TRIGGER IF EXISTS messages_ai;
		CREATE TRIGGER messages_ai AFTER INSERT ON messages
		WHEN new.is_noise = 0
		BEGIN
			INSERT INTO messages_fts(rowid, text, role, project, session_id)
			VALUES (new.id, new.text, new.role, new.project, new.session_id);
		END;
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create FTS: %w", err)
	}

	// Create or replace the sessions view.
	_, err = db.Exec(`
		DROP VIEW IF EXISTS sessions;
		CREATE VIEW sessions AS
		SELECT
			session_id,
			project,
			` + sessionTypeSQL("project") + ` AS session_type,
			COUNT(*) AS total_msgs,
			SUM(CASE WHEN is_noise = 0 THEN 1 ELSE 0 END) AS substantive_msgs,
			MIN(timestamp) AS first_msg,
			MAX(timestamp) AS last_msg
		FROM messages
		GROUP BY session_id;
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create sessions view: %w", err)
	}

	s := &Store{
		db:         db,
		projectDir: projectDir,
		offsets:    make(map[string]int64),
	}

	rows, err := db.Query("SELECT path, offset FROM ingest_state")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var path string
			var offset int64
			rows.Scan(&path, &offset)
			s.offsets[path] = offset
		}
	}

	return s, nil
}

// migrate adds the is_noise column and backfills it if needed.
func migrate(db *sql.DB) error {
	// Check if is_noise column exists.
	var colCount int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'is_noise'
	`).Scan(&colCount)
	if err != nil {
		return err
	}
	if colCount > 0 {
		return nil // already migrated
	}

	slog.Info("memory: migrating — adding is_noise column and rebuilding FTS index")

	_, err = db.Exec(`ALTER TABLE messages ADD COLUMN is_noise INTEGER NOT NULL DEFAULT 0`)
	if err != nil {
		return fmt.Errorf("add column: %w", err)
	}

	// Backfill is_noise for existing rows.
	_, err = db.Exec(`
		UPDATE messages SET is_noise = 1
		WHERE text LIKE '%[Request interrupted%'
		   OR text LIKE '%Your task is to create a detailed summary%'
		   OR text IN ('Tool loaded.', 'Tool loaded')
		   OR text LIKE '%<local-command-caveat>%'
		   OR (text LIKE '%<command-name>%' AND LENGTH(text) < 200)
	`)
	if err != nil {
		return fmt.Errorf("backfill is_noise: %w", err)
	}

	// Rebuild FTS to exclude noise rows.
	// Drop and recreate since we can't partially rebuild content-sync FTS.
	_, err = db.Exec(`
		DROP TRIGGER IF EXISTS messages_ai;
		DROP TABLE IF EXISTS messages_fts;
		CREATE VIRTUAL TABLE messages_fts USING fts5(
			text, role, project, session_id,
			content=messages,
			content_rowid=id
		);
		INSERT INTO messages_fts(rowid, text, role, project, session_id)
			SELECT id, text, role, project, session_id FROM messages WHERE is_noise = 0;
	`)
	if err != nil {
		return fmt.Errorf("rebuild FTS: %w", err)
	}

	var noiseCount int
	db.QueryRow(`SELECT COUNT(*) FROM messages WHERE is_noise = 1`).Scan(&noiseCount)
	slog.Info("memory: migration complete", "noise_rows_flagged", noiseCount)

	return nil
}

// Close closes the store.
func (s *Store) Close() error {
	return s.db.Close()
}

// IngestAll scans the project directory and ingests all JSONL files.
func (s *Store) IngestAll() error {
	return filepath.Walk(s.projectDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		return s.ingestFile(path)
	})
}

// Watch watches for new/modified JSONL files and ingests them in realtime.
func (s *Store) Watch() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	filepath.Walk(s.projectDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() {
			watcher.Add(path)
		}
		return nil
	})

	slog.Info("memory: watching for transcript changes", "dir", s.projectDir)

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if strings.HasSuffix(event.Name, ".jsonl") &&
				(event.Has(fsnotify.Write) || event.Has(fsnotify.Create)) {
				if err := s.ingestFile(event.Name); err != nil {
					slog.Error("memory: ingest failed", "file", event.Name, "err", err)
				}
			}
			if event.Has(fsnotify.Create) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					watcher.Add(event.Name)
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			slog.Error("memory: watcher error", "err", err)
		}
	}
}

// Search performs a full-text search and returns matching messages.
// sessionType filters by session type: "interactive" (default), "all",
// "subagent", "worktree", "ephemeral".
func (s *Store) Search(query string, limit int, sessionType string) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 20
	}
	if sessionType == "" {
		sessionType = "interactive"
	}

	var sqlQuery string
	var args []any

	if sessionType == "all" {
		sqlQuery = `
			SELECT m.session_id, m.project, m.role, m.text, m.timestamp, rank
			FROM messages_fts f
			JOIN messages m ON m.id = f.rowid
			WHERE messages_fts MATCH ?
			ORDER BY rank
			LIMIT ?
		`
		args = []any{query, limit}
	} else {
		sqlQuery = `
			SELECT m.session_id, m.project, m.role, m.text, m.timestamp, rank
			FROM messages_fts f
			JOIN messages m ON m.id = f.rowid
			WHERE messages_fts MATCH ?
			  AND (` + sessionTypeSQL("m.project") + `) = ?
			ORDER BY rank
			LIMIT ?
		`
		args = []any{query, sessionType, limit}
	}

	rows, err := s.db.Query(sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.SessionID, &r.Project, &r.Role, &r.Text, &r.Timestamp, &r.Rank); err != nil {
			continue
		}
		if len(r.Text) > 500 {
			r.Text = r.Text[:497] + "..."
		}
		results = append(results, r)
	}
	return results, nil
}

// ListSessions returns session summaries, filtered and sorted.
func (s *Store) ListSessions(sessionType string, minMessages int, limit int, projectFilter string) ([]SessionInfo, error) {
	if sessionType == "" {
		sessionType = "interactive"
	}
	if minMessages <= 0 {
		minMessages = 6
	}
	if limit <= 0 {
		limit = 30
	}

	where := []string{"substantive_msgs >= ?"}
	args := []any{minMessages}

	if sessionType != "all" {
		where = append(where, "session_type = ?")
		args = append(args, sessionType)
	}
	if projectFilter != "" {
		where = append(where, "project LIKE ?")
		args = append(args, "%"+projectFilter+"%")
	}

	args = append(args, limit)

	q := `SELECT session_id, project, session_type, total_msgs, substantive_msgs, first_msg, last_msg
		FROM sessions
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY last_msg DESC
		LIMIT ?`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SessionInfo
	for rows.Next() {
		var si SessionInfo
		if err := rows.Scan(&si.SessionID, &si.Project, &si.SessionType,
			&si.TotalMsgs, &si.SubstantiveMsgs, &si.FirstMsg, &si.LastMsg); err != nil {
			continue
		}
		results = append(results, si)
	}
	return results, nil
}

// Stats returns detailed index statistics broken down by session type.
func (s *Store) Stats() (*StatsResult, error) {
	var result StatsResult

	err := s.db.QueryRow("SELECT COUNT(DISTINCT session_id), COUNT(*) FROM messages").
		Scan(&result.TotalSessions, &result.TotalMessages)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`
		SELECT
			` + sessionTypeSQL("project") + ` AS session_type,
			COUNT(DISTINCT session_id) AS sessions,
			COUNT(*) AS total_msgs,
			SUM(CASE WHEN is_noise = 0 THEN 1 ELSE 0 END) AS substantive_msgs,
			SUM(CASE WHEN is_noise = 1 THEN 1 ELSE 0 END) AS noise_msgs
		FROM messages
		GROUP BY session_type
		ORDER BY sessions DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var ts TypeStats
		if err := rows.Scan(&ts.SessionType, &ts.Sessions, &ts.TotalMsgs,
			&ts.SubstantiveMsgs, &ts.NoiseMsgs); err != nil {
			continue
		}
		result.ByType = append(result.ByType, ts)
	}
	return &result, nil
}

// Query runs a read-only SQL query and returns rows as maps.
func (s *Store) Query(query string) ([]map[string]any, error) {
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	var results []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			row[col] = vals[i]
		}
		results = append(results, row)
		if len(results) >= 100 {
			break
		}
	}
	return results, nil
}

// isNoise returns true if a message text matches noise patterns.
func isNoise(text string) bool {
	if strings.Contains(text, "[Request interrupted") {
		return true
	}
	if strings.Contains(text, "Your task is to create a detailed summary") {
		return true
	}
	if text == "Tool loaded." || text == "Tool loaded" {
		return true
	}
	if strings.Contains(text, "<local-command-caveat>") {
		return true
	}
	if strings.Contains(text, "<command-name>") && len(text) < 200 {
		return true
	}
	return false
}

func (s *Store) ingestFile(path string) error {
	s.mu.Lock()
	offset := s.offsets[path]
	s.mu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if offset > 0 {
		f.Seek(offset, 0)
	}

	sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	project := filepath.Base(filepath.Dir(path))

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)

	count := 0
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO messages (session_id, project, role, text, timestamp, type, is_noise) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}

		typ, _ := entry["type"].(string)
		if typ != "user" && typ != "assistant" {
			continue
		}

		msg, _ := entry["message"].(map[string]any)
		if msg == nil {
			continue
		}

		ts, _ := entry["timestamp"].(string)
		if ts == "" {
			ts = time.Now().Format(time.RFC3339)
		}

		role := typ

		insertMsg := func(text string) {
			noise := 0
			if isNoise(text) {
				noise = 1
			}
			stmt.Exec(sessionID, project, role, text, ts, typ, noise)
			count++
		}

		switch content := msg["content"].(type) {
		case string:
			if content != "" {
				insertMsg(content)
			}
		case []any:
			for _, c := range content {
				cm, _ := c.(map[string]any)
				if cm["type"] == "text" {
					if text, _ := cm["text"].(string); text != "" {
						insertMsg(text)
					}
				}
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	newOffset, _ := f.Seek(0, 1)
	s.mu.Lock()
	s.offsets[path] = newOffset
	s.mu.Unlock()

	s.db.Exec("INSERT OR REPLACE INTO ingest_state (path, offset) VALUES (?, ?)", path, newOffset)

	if count > 0 {
		slog.Debug("memory: ingested", "file", filepath.Base(path), "messages", count)
	}
	return nil
}
