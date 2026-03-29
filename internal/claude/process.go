// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package claude manages persistent Claude Code processes via PTY.
// Each Process spawns a Claude Code interactive session, writes user
// messages to stdin, and reads structured events from the session JSONL.
package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/google/uuid"
)

// Event is a parsed JSONL event from the Claude Code session transcript.
type Event struct {
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"` // the complete JSONL line

	// Populated for type == "assistant"
	Text string `json:"-"`

	// Populated for type == "progress"
	ProgressType string `json:"-"`
}

// EventFunc receives events from the session transcript.
type EventFunc func(Event)

// Config configures a Claude Code process.
type Config struct {
	WorkDir        string   // working directory
	SessionID      string   // persistent session ID (empty = new random session)
	Model          string   // model override (empty = default)
	PermissionMode string   // permission mode (default: bypassPermissions)
	MCPConfig      string   // path to MCP config JSON (empty = use default discovery)
	DisallowTools  string   // additional comma-separated tools to disallow
	ExtraArgs      []string // additional CLI args
}

// Process is a persistent Claude Code instance running in a PTY.
type Process struct {
	sessionID string
	jsonlPath string
	ptmx      *os.File
	cmd       *exec.Cmd

	mu       sync.Mutex
	alive    bool
	onEvent  EventFunc
	stopOnce sync.Once
}

// Start spawns a new Claude Code process in a PTY.
func Start(cfg Config) (*Process, error) {
	if cfg.WorkDir == "" {
		cfg.WorkDir = "."
	}
	workDir, _ := filepath.Abs(cfg.WorkDir)

	if cfg.PermissionMode == "" {
		cfg.PermissionMode = "bypassPermissions"
	}

	sessionID := cfg.SessionID
	if sessionID == "" {
		sessionID = uuid.New().String()
	}
	jsonlDir := projectDir(workDir)
	jsonlPath := filepath.Join(jsonlDir, sessionID+".jsonl")

	// If the JSONL already exists, this is a resume — use --resume.
	_, statErr := os.Stat(jsonlPath)
	resuming := statErr == nil

	// All agents spawned by jevond are forbidden from creating their own
	// sub-agents. Jevond owns the process lifecycle exclusively. Agents
	// request new workers via jevond's MCP tools.
	disallowed := "Agent,TeamCreate,TeamDelete,SendMessage,EnterWorktree"
	if cfg.DisallowTools != "" {
		disallowed += "," + cfg.DisallowTools
	}

	args := []string{
		"--permission-mode", cfg.PermissionMode,
		"--disallowedTools", disallowed,
	}
	if resuming {
		args = append(args, "--resume", sessionID)
	} else {
		args = append(args, "--session-id", sessionID)
	}
	if cfg.MCPConfig != "" {
		args = append(args, "--mcp-config", cfg.MCPConfig)
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	args = append(args, cfg.ExtraArgs...)

	cmd := exec.Command("claude", args...)
	cmd.Dir = workDir

	ptmx, pts, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("pty.Open: %w", err)
	}

	cmd.Stdin = pts
	cmd.Stdout = pts
	cmd.Stderr = pts
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}

	if err := cmd.Start(); err != nil {
		ptmx.Close()
		pts.Close()
		return nil, fmt.Errorf("start claude: %w", err)
	}
	pts.Close()

	p := &Process{
		sessionID: sessionID,
		jsonlPath: jsonlPath,
		ptmx:      ptmx,
		cmd:       cmd,
		alive:     true,
	}

	// Drain PTY output — log first chunk to verify claude started.
	go func() {
		buf := make([]byte, 4096)
		first := true
		for {
			n, err := ptmx.Read(buf)
			if n > 0 && first {
				slog.Info("pty first output", "bytes", n)
				first = false
			}
			if err != nil {
				slog.Info("pty read done", "err", err)
				return
			}
		}
	}()

	// Monitor process exit.
	go func() {
		err := cmd.Wait()
		slog.Info("claude process exited", "session", sessionID, "err", err)
		p.mu.Lock()
		p.alive = false
		p.mu.Unlock()
	}()

	// Tail JSONL.
	go p.tailJSONL()

	return p, nil
}

// SessionID returns the Claude Code session ID.
func (p *Process) SessionID() string { return p.sessionID }

// JSONLPath returns the path to the session JSONL file.
func (p *Process) JSONLPath() string { return p.jsonlPath }

// Alive reports whether the Claude process is still running.
func (p *Process) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.alive
}

// OnEvent sets the callback for JSONL events.
func (p *Process) OnEvent(fn EventFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onEvent = fn
}

// Interrupt sends Esc to the Claude process to cancel the current turn.
func (p *Process) Interrupt() error {
	p.mu.Lock()
	alive := p.alive
	p.mu.Unlock()
	if !alive {
		return fmt.Errorf("claude process not running")
	}
	_, err := p.ptmx.Write([]byte("\x1b"))
	return err
}

// Send writes a user message to the Claude process.
func (p *Process) Send(msg string) error {
	p.mu.Lock()
	alive := p.alive
	p.mu.Unlock()
	if !alive {
		return fmt.Errorf("claude process not running")
	}
	data := []byte(msg + "\n")
	n, err := p.ptmx.Write(data)
	slog.Info("pty write", "bytes", n, "err", err, "data", string(data))
	return err
}

// WaitForResponse blocks until the next assistant response is complete
// (signalled by a "system" event in the JSONL). Returns the assistant text.
func (p *Process) WaitForResponse(ctx context.Context) (string, error) {
	ch := make(chan string, 1)
	var text strings.Builder

	oldFn := p.onEvent
	p.OnEvent(func(ev Event) {
		if oldFn != nil {
			oldFn(ev)
		}
		switch ev.Type {
		case "assistant":
			if ev.Text != "" {
				text.WriteString(ev.Text)
			}
		case "system":
			select {
			case ch <- text.String():
			default:
			}
		}
	})

	defer p.OnEvent(oldFn) // restore

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-ch:
		return result, nil
	}
}

// Stop terminates the Claude process.
func (p *Process) Stop() {
	p.stopOnce.Do(func() {
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
		time.Sleep(time.Second)
		p.cmd.Process.Kill()
		p.ptmx.Close()
	})
}

func (p *Process) tailJSONL() {
	// Wait for file to be created.
	for {
		if _, err := os.Stat(p.jsonlPath); err == nil {
			break
		}
		if !p.Alive() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	slog.Info("JSONL found", "session", p.sessionID)

	f, err := os.Open(p.jsonlPath)
	if err != nil {
		slog.Error("open JSONL failed", "session", p.sessionID, "err", err)
		return
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if !p.Alive() {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		ev := parseEvent(line)

		p.mu.Lock()
		fn := p.onEvent
		p.mu.Unlock()

		if fn != nil {
			fn(ev)
		}
	}
}

func parseEvent(line string) Event {
	var ev Event
	ev.Raw = json.RawMessage(line)

	var entry map[string]any
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		ev.Type = "unknown"
		return ev
	}
	ev.Type, _ = entry["type"].(string)

	switch ev.Type {
	case "assistant":
		if msg, ok := entry["message"].(map[string]any); ok {
			if content, ok := msg["content"].([]any); ok {
				var texts []string
				for _, c := range content {
					if cm, ok := c.(map[string]any); ok && cm["type"] == "text" {
						if t, ok := cm["text"].(string); ok {
							texts = append(texts, t)
						}
					}
				}
				ev.Text = strings.Join(texts, "\n")
			}
		}
	case "progress":
		if data, ok := entry["data"].(map[string]any); ok {
			ev.ProgressType, _ = data["type"].(string)
		}
	}

	return ev
}

// projectDir returns the Claude Code project directory for a workdir.
func projectDir(workDir string) string {
	var escaped strings.Builder
	for _, r := range workDir {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			escaped.WriteRune(r)
		} else {
			escaped.WriteByte('-')
		}
	}
	return filepath.Join(os.Getenv("HOME"), ".claude", "projects", escaped.String())
}

