package jevon

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/marcelocantos/jevon/internal/session"
)

// Config holds Jevon configuration.
type Config struct {
	WorkDir  string // working directory (contains CLAUDE.md and .mcp.json)
	Model    string // model for Jevon (e.g. "opus", "sonnet")
	ClaudeID string // restored claude session ID for --resume
}

// OutputFunc receives text from Jevon to stream to the user.
type OutputFunc func(text string)

// StatusFunc receives Jevon status changes.
type StatusFunc func(state string)

// RawLogFunc receives raw NDJSON lines from the Claude process.
type RawLogFunc func(line []byte)

// Jevon coordinates between the user and Claude Code workers.
type Jevon struct {
	cfg        Config
	onOutput   OutputFunc
	onStatus   StatusFunc
	onRawLog   RawLogFunc
	onClaudeID ClaudeIDFunc

	mu       sync.Mutex
	queue    []Event
	notify   chan struct{}
	claudeID string // claude session ID for --resume
	running  bool
}

// ClaudeIDFunc is called when Jevon's claude session ID changes.
type ClaudeIDFunc func(id string)

// New creates a Jevon with the given config.
func New(cfg Config) *Jevon {
	return &Jevon{
		cfg:      cfg,
		claudeID: cfg.ClaudeID,
		notify:   make(chan struct{}, 1),
	}
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

// Enqueue adds an event to Jevon's queue. If Jevon is idle,
// it will be woken up to process the event.
func (j *Jevon) Enqueue(ev Event) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}

	j.mu.Lock()
	j.queue = append(j.queue, ev)
	j.mu.Unlock()

	select {
	case j.notify <- struct{}{}:
	default:
	}
}

// Run starts the Jevon event loop. It blocks until ctx is cancelled.
func (j *Jevon) Run(ctx context.Context) {
	slog.Info("jevon started", "workdir", j.cfg.WorkDir)

	for {
		select {
		case <-ctx.Done():
			slog.Info("jevon stopped")
			return
		case <-j.notify:
		}

		j.mu.Lock()
		if len(j.queue) == 0 {
			j.mu.Unlock()
			continue
		}
		batch := j.queue
		j.queue = nil
		j.running = true
		j.mu.Unlock()

		j.emitStatus("thinking")

		prompt := FormatPrompt(batch)
		slog.Debug("jevon invoking", "prompt", prompt)

		if err := j.invoke(ctx, prompt); err != nil {
			slog.Error("jevon invoke failed", "err", err)
		}

		j.mu.Lock()
		j.running = false
		hasMore := len(j.queue) > 0
		j.mu.Unlock()

		j.emitStatus("idle")

		if hasMore {
			select {
			case j.notify <- struct{}{}:
			default:
			}
		}
	}
}

func (j *Jevon) invoke(ctx context.Context, prompt string) error {
	invokeCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	args := []string{
		"-p",
		"--verbose",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--permission-mode", "bypassPermissions",
	}

	j.mu.Lock()
	claudeID := j.claudeID
	j.mu.Unlock()

	if claudeID != "" {
		args = append(args, "--resume", claudeID)
	}

	if j.cfg.Model != "" {
		args = append(args, "--model", j.cfg.Model)
	}

	// Pass prompt via stdin.
	slog.Debug("spawning jevon claude", "args", args)

	cmd := exec.CommandContext(invokeCtx, "claude", args...)
	cmd.Dir = j.cfg.WorkDir
	cmd.Stdin = strings.NewReader(prompt)

	// Remove CLAUDECODE to avoid nested session detection.
	cmd.Env = filterEnv(os.Environ(), "CLAUDECODE")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start claude: %w", err)
	}

	// Log stderr.
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 256*1024), 256*1024)
		for scanner.Scan() {
			slog.Debug("jevon stderr", "line", scanner.Text())
		}
	}()

	// Parse stdout NDJSON.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		j.mu.Lock()
		rawFn := j.onRawLog
		j.mu.Unlock()
		if rawFn != nil {
			rawFn(line)
		}

		events := session.ParseLine(line)
		for _, ev := range events {
			switch ev.Type {
			case session.EventInit:
				j.mu.Lock()
				isNew := j.claudeID == ""
				if isNew {
					j.claudeID = ev.SessionID
					slog.Info("jevon session established", "claude_id", ev.SessionID)
				}
				fn := j.onClaudeID
				j.mu.Unlock()
				if isNew && fn != nil {
					fn(ev.SessionID)
				}

			case session.EventText:
				j.mu.Lock()
				fn := j.onOutput
				j.mu.Unlock()
				if fn != nil {
					fn(ev.Content)
				}

			case session.EventToolUse:
				slog.Debug("jevon tool call",
					"tool", ev.ToolName, "input", ev.ToolInput)

			case session.EventResult:
				slog.Debug("jevon turn complete",
					"duration_ms", ev.DurationMs,
					"cost_usd", ev.CostUSD,
					"input_tokens", ev.Usage.InputTokens,
					"output_tokens", ev.Usage.OutputTokens,
					"cache_creation", ev.Usage.CacheCreationInputTokens,
					"cache_read", ev.Usage.CacheReadInputTokens)

			case session.EventError:
				slog.Warn("jevon error", "msg", ev.ErrorMsg)
				j.mu.Lock()
				fn := j.onOutput
				j.mu.Unlock()
				if fn != nil {
					fn("I encountered an error: " + ev.ErrorMsg)
				}
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("claude exited: %w", err)
	}
	return nil
}

func (j *Jevon) emitStatus(state string) {
	j.mu.Lock()
	fn := j.onStatus
	j.mu.Unlock()
	if fn != nil {
		fn(state)
	}
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
