// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package db provides SQLite-backed persistent storage for the
// voice learning model, conversation history, and configuration.
package db

import (
	"time"

	"github.com/marcelocantos/sqlpipe/go/sqlpipe"
)

// TranscriptEntry is a single turn in the conversation log.
type TranscriptEntry struct {
	Role      string
	Text      string
	CreatedAt time.Time
}

// DB wraps a SQLite database connection.
type DB struct {
	sdb *sqlpipe.Database
}

// Open opens (or creates) a SQLite database at path and ensures the
// schema exists.
func Open(path string) (*DB, error) {
	sdb, err := sqlpipe.OpenDatabase(path)
	if err != nil {
		return nil, err
	}
	if err := sdb.Exec("PRAGMA journal_mode=WAL"); err != nil {
		sdb.Close()
		return nil, err
	}
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS transcript (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			role       TEXT NOT NULL,
			text       TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS kv (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS raw_log (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			source     TEXT NOT NULL,
			line       TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	} {
		if err := sdb.Exec(ddl); err != nil {
			sdb.Close()
			return nil, err
		}
	}
	return &DB{sdb: sdb}, nil
}

// AppendTranscript inserts a transcript entry.
func (d *DB) AppendTranscript(role, text string) error {
	return d.sdb.Exec(
		`INSERT INTO transcript (role, text) VALUES (?, ?)`,
		role, text,
	)
}

// LoadTranscript returns all transcript entries in chronological order.
func (d *DB) LoadTranscript() ([]TranscriptEntry, error) {
	var entries []TranscriptEntry
	for row := range d.sdb.Rows(
		`SELECT role, text, created_at FROM transcript ORDER BY id`,
	) {
		if row.Err() != nil {
			return nil, row.Err()
		}
		entries = append(entries, TranscriptEntry{
			Role: row.Text(0),
			Text: row.Text(1),
			// Parse created_at as time.Time. SQLite returns it as text.
			CreatedAt: parseTime(row.Text(2)),
		})
	}
	return entries, nil
}

// AppendRawLog inserts a raw NDJSON line from a Claude process.
func (d *DB) AppendRawLog(source, line string) error {
	return d.sdb.Exec(
		`INSERT INTO raw_log (source, line) VALUES (?, ?)`,
		source, line,
	)
}

// Get returns a value from the kv table, or "" if not found.
func (d *DB) Get(key string) string {
	qr, err := d.sdb.Query(`SELECT value FROM kv WHERE key = ?`, key)
	if err != nil || len(qr.Rows) == 0 {
		return ""
	}
	if s, ok := qr.Rows[0][0].(string); ok {
		return s
	}
	return ""
}

// Set upserts a value in the kv table.
func (d *DB) Set(key, value string) error {
	return d.sdb.Exec(
		`INSERT INTO kv (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
}

// SqlpipeDB returns the underlying sqlpipe Database.
func (d *DB) SqlpipeDB() *sqlpipe.Database { return d.sdb }

// Close closes the database connection.
func (d *DB) Close() error {
	return d.sdb.Close()
}

// parseTime parses a SQLite timestamp string into time.Time.
func parseTime(s string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		time.RFC3339Nano,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
