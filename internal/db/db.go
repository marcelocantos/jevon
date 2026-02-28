// Package db provides SQLite-backed persistent storage for the
// voice learning model, conversation history, and configuration.
package db

import (
	"database/sql"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TranscriptEntry is a single turn in the conversation log.
type TranscriptEntry struct {
	Role      string
	Text      string
	CreatedAt time.Time
}

// DB wraps a SQLite database connection.
type DB struct {
	db *sql.DB
}

// Open opens (or creates) a SQLite database at path and ensures the
// schema exists.
func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	if _, err := sqlDB.Exec(`
		CREATE TABLE IF NOT EXISTS transcript (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			role       TEXT NOT NULL,
			text       TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return &DB{db: sqlDB}, nil
}

// AppendTranscript inserts a transcript entry.
func (d *DB) AppendTranscript(role, text string) error {
	_, err := d.db.Exec(
		`INSERT INTO transcript (role, text) VALUES (?, ?)`,
		role, text,
	)
	return err
}

// LoadTranscript returns all transcript entries in chronological order.
func (d *DB) LoadTranscript() ([]TranscriptEntry, error) {
	rows, err := d.db.Query(
		`SELECT role, text, created_at FROM transcript ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []TranscriptEntry
	for rows.Next() {
		var e TranscriptEntry
		if err := rows.Scan(&e.Role, &e.Text, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// Close closes the database connection.
func (d *DB) Close() error {
	return d.db.Close()
}
