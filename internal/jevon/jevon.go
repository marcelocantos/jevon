package jevon

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/marcelocantos/jevon/internal/claude"
)

// Config holds Jevon configuration.
type Config struct {
	WorkDir  string // working directory (contains CLAUDE.md and .mcp.json)
	Model    string // model for Jevon (e.g. "opus", "sonnet")
	ClaudeID string // restored claude session ID for --resume (unused with persistent process)
}

// OutputFunc receives text from Jevon to stream to the user.
type OutputFunc func(text string)

// StatusFunc receives Jevon status changes.
type StatusFunc func(state string)

// RawLogFunc receives raw NDJSON lines from the Claude process.
type RawLogFunc func(line []byte)

// ClaudeIDFunc is called when Jevon's claude session ID changes.
type ClaudeIDFunc func(id string)

// Jevon coordinates between the user and a persistent Claude Code process.
type Jevon struct {
	cfg      Config
	onOutput OutputFunc
	onStatus StatusFunc
	onRawLog RawLogFunc
	onClaudeID ClaudeIDFunc

	mu      sync.Mutex
	proc    *claude.Process
	waiting bool // true while waiting for a response
}

// New creates a Jevon with the given config.
func New(cfg Config) *Jevon {
	return &Jevon{cfg: cfg}
}

// SetOutput sets the callback for Jevon text output.
func (j *Jevon) SetOutput(fn OutputFunc) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.onOutput = fn
}

// SetStatus sets the callback for Jevon status changes.
func (j *Jevon) SetStatus(fn StatusFunc) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.onStatus = fn
}

// SetRawLog sets the callback for raw NDJSON lines from the Claude process.
func (j *Jevon) SetRawLog(fn RawLogFunc) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.onRawLog = fn
}

// SetClaudeIDCallback sets the callback for when Jevon's claude
// session ID is first captured.
func (j *Jevon) SetClaudeIDCallback(fn ClaudeIDFunc) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.onClaudeID = fn
}

// Run starts the persistent Claude Code process and blocks until ctx
// is cancelled.
func (j *Jevon) Run(ctx context.Context) {
	slog.Info("jevon starting persistent claude", "workdir", j.cfg.WorkDir)

	proc, err := claude.Start(claude.Config{
		WorkDir: j.cfg.WorkDir,
		Model:   j.cfg.Model,
		ExtraArgs: []string{
			"--disallowedTools", "AskUserQuestion,EnterPlanMode,ExitPlanMode," +
				"Agent,TeamCreate,TeamDelete,SendMessage," +
				"TaskCreate,TaskUpdate,TaskList,TaskGet,TaskOutput,TaskStop," +
				"EnterWorktree,Skill,NotebookEdit",
		},
	})
	if err != nil {
		slog.Error("jevon: failed to start claude", "err", err)
		return
	}

	j.mu.Lock()
	j.proc = proc
	j.mu.Unlock()

	slog.Info("jevon started", "session", proc.SessionID())

	// Notify the claude ID callback.
	j.mu.Lock()
	if j.onClaudeID != nil {
		j.onClaudeID(proc.SessionID())
	}
	j.mu.Unlock()

	// Handle events from the JSONL.
	proc.OnEvent(func(ev claude.Event) {
		j.mu.Lock()
		onOutput := j.onOutput
		onRawLog := j.onRawLog
		wasWaiting := j.waiting
		j.mu.Unlock()

		// Forward raw log.
		if onRawLog != nil {
			onRawLog(ev.Raw)
		}

		switch ev.Type {
		case "assistant":
			if ev.Text != "" && onOutput != nil {
				onOutput(ev.Text)
			}
		case "system":
			// Turn complete.
			if wasWaiting {
				j.mu.Lock()
				j.waiting = false
				j.mu.Unlock()
				j.emitStatus("idle")
			}
		}
	})

	// Wait for context cancellation.
	<-ctx.Done()
	slog.Info("jevon stopping")
	proc.Stop()
}

// Enqueue adds an event to Jevon. For user messages, it sends directly
// to the persistent Claude process.
func (j *Jevon) Enqueue(ev Event) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}

	slog.Info("jevon enqueue", "kind", ev.Kind, "text", ev.Text)

	if ev.Kind != EventUserMessage {
		return
	}

	j.mu.Lock()
	proc := j.proc
	j.mu.Unlock()

	if proc == nil {
		slog.Warn("jevon: message received before claude started")
		return
	}

	j.mu.Lock()
	j.waiting = true
	j.mu.Unlock()
	j.emitStatus("thinking")

	slog.Info("jevon sending to claude", "text", ev.Text)

	if err := proc.Send(ev.Text); err != nil {
		slog.Error("jevon: send failed", "err", err)
		j.mu.Lock()
		j.waiting = false
		j.mu.Unlock()
		j.emitStatus("idle")
	}
}

func (j *Jevon) emitStatus(state string) {
	j.mu.Lock()
	fn := j.onStatus
	j.mu.Unlock()
	if fn != nil {
		fn(state)
	}
}
