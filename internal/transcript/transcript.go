// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package transcript provides read, truncate, and fork operations on
// Claude Code session transcript JSONL files.
package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/marcelocantos/jevon/internal/discovery"
)

// Turn represents a single user→assistant exchange extracted from a transcript.
type Turn struct {
	Number int    `json:"turn_number"`
	Role   string `json:"role"` // "user" or "assistant"
	Text   string `json:"text"`
}

// Reader provides transcript operations backed by the Claude Code projects directory.
type Reader struct {
	projectsDir string // ~/.claude/projects
}

// NewReader creates a Reader rooted at the given Claude Code projects directory.
func NewReader(projectsDir string) *Reader {
	return &Reader{projectsDir: projectsDir}
}

// Read parses a session transcript and returns turns grouped by user message boundaries.
// A turn boundary is a JSONL line with type "user" whose content is a plain string
// (not a tool_result array).
func (r *Reader) Read(sessionID string) ([]map[string]any, error) {
	path, err := r.findJSONL(sessionID)
	if err != nil {
		return nil, err
	}

	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}

	turns := extractTurns(lines)
	result := make([]map[string]any, len(turns))
	for i, t := range turns {
		result[i] = map[string]any{
			"turn_number": t.Number,
			"role":        t.Role,
			"text":        t.Text,
		}
	}
	return result, nil
}

// Truncate rewrites a session transcript to keep only the first keepTurns
// user→assistant exchanges. Lines before the first user turn (metadata,
// snapshots) are always preserved.
func (r *Reader) Truncate(sessionID string, keepTurns int) error {
	path, err := r.findJSONL(sessionID)
	if err != nil {
		return err
	}

	lines, err := readLines(path)
	if err != nil {
		return err
	}

	kept := truncateLines(lines, keepTurns)
	return writeLines(path, kept)
}

// Fork creates a copy of a session transcript truncated to keepTurns,
// returning the new session UUID. The original transcript is untouched.
func (r *Reader) Fork(sessionID string, keepTurns int) (string, error) {
	path, err := r.findJSONL(sessionID)
	if err != nil {
		return "", err
	}

	lines, err := readLines(path)
	if err != nil {
		return "", err
	}

	kept := truncateLines(lines, keepTurns)

	newUUID := uuid.New().String()
	dir := filepath.Dir(path)
	newPath := filepath.Join(dir, newUUID+".jsonl")

	if err := writeLines(newPath, kept); err != nil {
		return "", err
	}

	slog.Info("forked transcript", "from", sessionID, "to", newUUID, "turns", keepTurns)
	return newUUID, nil
}

// findJSONL locates the JSONL file for a session UUID by scanning project directories.
func (r *Reader) findJSONL(sessionID string) (string, error) {
	if !discovery.IsUUID(sessionID) {
		return "", fmt.Errorf("invalid session ID: %q", sessionID)
	}

	entries, err := os.ReadDir(r.projectsDir)
	if err != nil {
		return "", fmt.Errorf("read projects dir: %w", err)
	}

	for _, e := range entries {
		if !e.IsDir() || e.Name() == "memory" {
			continue
		}
		path := filepath.Join(r.projectsDir, e.Name(), sessionID+".jsonl")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("transcript not found for session %q", sessionID)
}

// jsonlLine holds the raw bytes and parsed type/role for a single JSONL line.
type jsonlLine struct {
	raw  string
	typ  string // "user", "assistant", "progress", "file-history-snapshot", etc.
	role string // from message.role if present
	// isUserTurn is true for type="user" lines where content is a string (not tool_result).
	isUserTurn bool
}

// readLines reads and parses all JSONL lines from a transcript file.
func readLines(path string) ([]jsonlLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	var lines []jsonlLine
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1MB line buffer

	for scanner.Scan() {
		raw := scanner.Text()
		if strings.TrimSpace(raw) == "" {
			continue
		}

		var envelope struct {
			Type    string `json:"type"`
			Message *struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			// Keep unparseable lines as-is.
			lines = append(lines, jsonlLine{raw: raw})
			continue
		}

		line := jsonlLine{
			raw: raw,
			typ: envelope.Type,
		}

		if envelope.Message != nil {
			line.role = envelope.Message.Role

			// A "user turn" is a user message where content is a plain string,
			// not a tool_result array. This marks a turn boundary.
			if envelope.Type == "user" && envelope.Message.Content != nil {
				content := strings.TrimSpace(string(envelope.Message.Content))
				if len(content) > 0 && content[0] == '"' {
					line.isUserTurn = true
				}
			}
		}

		lines = append(lines, line)
	}
	return lines, scanner.Err()
}

// extractTurns groups JSONL lines into user→assistant turns.
func extractTurns(lines []jsonlLine) []Turn {
	var turns []Turn
	turnNum := 0
	var userText, assistantText string
	inTurn := false

	for _, l := range lines {
		if l.isUserTurn {
			// Flush previous turn.
			if inTurn {
				turns = append(turns, Turn{Number: turnNum, Role: "user", Text: userText})
				if assistantText != "" {
					turns = append(turns, Turn{Number: turnNum, Role: "assistant", Text: assistantText})
				}
			}
			turnNum++
			inTurn = true
			userText = extractText(l.raw)
			assistantText = ""
		} else if inTurn && l.typ == "assistant" {
			text := extractAssistantText(l.raw)
			if text != "" {
				if assistantText != "" {
					assistantText += "\n"
				}
				assistantText += text
			}
		}
	}

	// Flush last turn.
	if inTurn {
		turns = append(turns, Turn{Number: turnNum, Role: "user", Text: userText})
		if assistantText != "" {
			turns = append(turns, Turn{Number: turnNum, Role: "assistant", Text: assistantText})
		}
	}

	return turns
}

// extractText pulls the user's text from a user-turn JSONL line.
func extractText(raw string) string {
	var envelope struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return ""
	}
	return envelope.Message.Content
}

// extractAssistantText pulls text content from an assistant JSONL line.
func extractAssistantText(raw string) string {
	var envelope struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return ""
	}

	// Content is an array of blocks.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(envelope.Message.Content, &blocks); err != nil {
		return ""
	}

	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// truncateLines keeps all lines up to and including the Nth user turn,
// plus all subsequent non-user-turn lines (assistant responses, tool results,
// progress) until the next user turn. Metadata lines before the first user
// turn are always preserved.
func truncateLines(lines []jsonlLine, keepTurns int) []string {
	var kept []string
	turnsSeen := 0

	for _, l := range lines {
		if l.isUserTurn {
			turnsSeen++
			if turnsSeen > keepTurns {
				break
			}
		}
		kept = append(kept, l.raw)
	}

	return kept
}

// writeLines writes raw JSONL lines to a file, one per line.
func writeLines(path string, lines []string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create transcript: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	for _, line := range lines {
		if _, err := w.WriteString(line); err != nil {
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			return err
		}
	}
	return w.Flush()
}
