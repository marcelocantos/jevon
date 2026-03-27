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

// New creates or opens a transcript memory store.
func New(dbPath, projectDir string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, err
	}

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
		CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
			text, role, project, session_id,
			content=messages,
			content_rowid=id
		);
		CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
			INSERT INTO messages_fts(rowid, text, role, project, session_id)
			VALUES (new.id, new.text, new.role, new.project, new.session_id);
		END;
		CREATE TABLE IF NOT EXISTS ingest_state (
			path TEXT PRIMARY KEY,
			offset INTEGER NOT NULL
		);
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create tables: %w", err)
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
func (s *Store) Search(query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`
		SELECT m.session_id, m.project, m.role, m.text, m.timestamp,
		       rank
		FROM messages_fts f
		JOIN messages m ON m.id = f.rowid
		WHERE messages_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, query, limit)
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

// Stats returns index statistics.
func (s *Store) Stats() (sessions int, messages int, err error) {
	err = s.db.QueryRow("SELECT COUNT(DISTINCT session_id), COUNT(*) FROM messages").Scan(&sessions, &messages)
	return
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

	stmt, err := tx.Prepare(`INSERT INTO messages (session_id, project, role, text, timestamp, type) VALUES (?, ?, ?, ?, ?, ?)`)
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

		switch content := msg["content"].(type) {
		case string:
			if content != "" {
				stmt.Exec(sessionID, project, role, content, ts, typ)
				count++
			}
		case []any:
			for _, c := range content {
				cm, _ := c.(map[string]any)
				if cm["type"] == "text" {
					if text, _ := cm["text"].(string); text != "" {
						stmt.Exec(sessionID, project, role, text, ts, typ)
						count++
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
		slog.Info("memory: ingested", "file", filepath.Base(path), "messages", count)
	}
	return nil
}
