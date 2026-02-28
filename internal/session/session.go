// Package session manages individual Claude Code sessions,
// spawning headless claude processes and parsing their streaming output.
package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

// EventType identifies the kind of session event.
type EventType string

const (
	EventInit    EventType = "init"
	EventText    EventType = "text"
	EventToolUse EventType = "tool_use"
	EventResult  EventType = "result"
	EventError   EventType = "error"
)

// Event is a parsed event from the Claude stream-json output.
type Event struct {
	Type       EventType
	Content    string  // text content or result text
	ToolName   string  // for tool_use events
	ToolInput  string  // for tool_use events (JSON string)
	ToolID     string  // for tool_use events
	SessionID  string  // for init events
	DurationMs float64 // for result events
	CostUSD    float64 // for result events
	IsError    bool    // true if result is an error
	ErrorMsg   string  // error message if IsError
}

// Status represents a session's lifecycle state.
type Status string

const (
	StatusIdle    Status = "idle"
	StatusRunning Status = "running"
	StatusError   Status = "error"
	StatusStopped Status = "stopped"
)

// Config holds the configuration for creating a session.
type Config struct {
	ID      string // unique session ID (ULID)
	Name    string // human-readable name
	WorkDir string // working directory for claude process
	Model   string // model name (e.g. "opus", "sonnet"); empty = default
}

// Session wraps a single Claude Code conversation.
type Session struct {
	id      string
	name    string
	workDir string
	model   string

	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
	status Status
}

// New creates a Session from a Config.
func New(cfg Config) *Session {
	return &Session{
		id:      cfg.ID,
		name:    cfg.Name,
		workDir: cfg.WorkDir,
		model:   cfg.Model,
		status:  StatusIdle,
	}
}

// ID returns the session's unique identifier.
func (s *Session) ID() string { return s.id }

// Name returns the session's human-readable name.
func (s *Session) Name() string { return s.name }

// Status returns the current session status.
func (s *Session) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Run sends a prompt to the session and returns a channel of events.
// The channel is closed when the process exits.
func (s *Session) Run(ctx context.Context, prompt string) (<-chan Event, error) {
	s.mu.Lock()
	if s.status == StatusRunning {
		s.mu.Unlock()
		return nil, fmt.Errorf("session %s is busy", s.id)
	}
	if s.status == StatusStopped {
		s.mu.Unlock()
		return nil, fmt.Errorf("session %s is stopped", s.id)
	}
	s.status = StatusRunning
	s.mu.Unlock()

	cmdCtx, cancel := context.WithCancel(ctx)

	args := []string{
		"-p",
		"--verbose",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--dangerously-skip-permissions",
	}
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	args = append(args, prompt)

	slog.Debug("spawning claude", "session", s.id, "args", args)

	cmd := exec.CommandContext(cmdCtx, "claude", args...)
	cmd.Dir = s.workDir

	// Unset CLAUDECODE to avoid nested session detection.
	cmd.Env = filterEnv(os.Environ(), "CLAUDECODE")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		s.mu.Lock()
		s.status = StatusError
		s.mu.Unlock()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		s.mu.Lock()
		s.status = StatusError
		s.mu.Unlock()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		s.mu.Lock()
		s.status = StatusError
		s.mu.Unlock()
		return nil, fmt.Errorf("start claude: %w", err)
	}

	s.mu.Lock()
	s.cmd = cmd
	s.cancel = cancel
	s.mu.Unlock()

	ch := make(chan Event, 16)

	// Log stderr in a separate goroutine.
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 256*1024), 256*1024)
		for scanner.Scan() {
			slog.Debug("claude stderr", "session", s.id, "line", scanner.Text())
		}
	}()

	// Parse stdout NDJSON and send events.
	go func() {
		defer close(ch)
		defer func() {
			if err := cmd.Wait(); err != nil {
				slog.Warn("claude process exited with error", "session", s.id, "error", err)
			} else {
				slog.Debug("claude process exited cleanly", "session", s.id)
			}
			cancel()
			s.mu.Lock()
			s.cmd = nil
			s.cancel = nil
			if s.status == StatusRunning {
				s.status = StatusIdle
			}
			s.mu.Unlock()
		}()

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			events := ParseLine(line)
			for _, ev := range events {
				select {
				case ch <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return ch, nil
}

// Cancel sends SIGINT to the running claude process.
func (s *Session) Cancel() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	return s.cmd.Process.Signal(syscall.SIGINT)
}

// Stop terminates the session permanently.
func (s *Session) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.status = StatusStopped
	if s.cancel != nil {
		s.cancel()
	}
}

// ParseLine parses a single NDJSON line from claude's stream-json output
// and returns zero or more Events.
func ParseLine(line []byte) []Event {
	var base struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(line, &base); err != nil {
		return nil
	}

	switch base.Type {
	case "system":
		return parseSystem(line)
	case "assistant":
		return parseAssistant(line)
	case "result":
		return parseResult(line)
	default:
		return nil
	}
}

func parseSystem(line []byte) []Event {
	var msg struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		return nil
	}
	return []Event{{
		Type:      EventInit,
		SessionID: msg.SessionID,
	}}
}

func parseAssistant(line []byte) []Event {
	var msg struct {
		Message struct {
			Content []json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		return nil
	}

	var events []Event
	for _, raw := range msg.Message.Content {
		var block struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			continue
		}

		switch block.Type {
		case "text":
			if block.Text != "" {
				events = append(events, Event{
					Type:    EventText,
					Content: block.Text,
				})
			}
		case "tool_use":
			inputStr := ""
			if block.Input != nil {
				inputStr = string(block.Input)
			}
			events = append(events, Event{
				Type:      EventToolUse,
				ToolID:    block.ID,
				ToolName:  block.Name,
				ToolInput: inputStr,
			})
		}
	}
	return events
}

func parseResult(line []byte) []Event {
	var msg struct {
		Subtype      string  `json:"subtype"`
		Result       string  `json:"result"`
		DurationMs   float64 `json:"duration_ms"`
		TotalCostUSD float64 `json:"total_cost_usd"`
		Errors       []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		return nil
	}

	if msg.Subtype == "success" {
		return []Event{{
			Type:       EventResult,
			Content:    msg.Result,
			DurationMs: msg.DurationMs,
			CostUSD:    msg.TotalCostUSD,
		}}
	}

	// Error result
	var errMsgs []string
	for _, e := range msg.Errors {
		errMsgs = append(errMsgs, e.Message)
	}
	return []Event{{
		Type:     EventError,
		IsError:  true,
		ErrorMsg: strings.Join(errMsgs, "; "),
	}}
}

func filterEnv(env []string, exclude string) []string {
	prefix := exclude + "="
	var result []string
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			result = append(result, e)
		}
	}
	return result
}
