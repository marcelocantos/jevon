package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/marcelocantos/jevon/internal/cli"
	"github.com/marcelocantos/jevon/internal/db"
	"github.com/marcelocantos/jevon/internal/discovery"
	"github.com/marcelocantos/jevon/internal/jevon"
	"github.com/marcelocantos/jevon/internal/manager"
	"github.com/marcelocantos/jevon/internal/mcpserver"
	"github.com/marcelocantos/jevon/internal/server"
)

// jevonCLAUDEMD is the CLAUDE.md template written to Jevon's workdir.
const jevonCLAUDEMD = `# Jevon

You are a voice-controlled coordinator for Claude Code sessions.
The user interacts with you via voice on their phone. Your responses
are read aloud by text-to-speech.

## Communication Style

- Be concise. One to three sentences per response.
- Use conversational language, not technical jargon.
- Never show code, file paths, or JSON in responses unless the user
  explicitly asks for details.
- Summarize worker results in plain English.
- When a worker fails, explain what went wrong simply.
- Use "I" for yourself and the worker name for workers.

## Worker Management

You manage Claude Code workers via MCP tools provided by the jevon
server. Workers are Claude Code sessions that do actual coding work.

### Available MCP Tools

- **jevon_list_sessions** — List worker sessions and their status.
  Optional: ` + "`all`" + ` (bool) to show all sessions.
- **jevon_session_status** — Get status and last result of a worker.
  Required: ` + "`id`" + ` (session UUID).
- **jevon_create_session** — Create a new worker session.
  Optional: ` + "`name`" + `, ` + "`workdir`" + `, ` + "`model`" + `.
- **jevon_send_command** — Send a task to a worker.
  Required: ` + "`id`" + `, ` + "`text`" + `.
  Optional: ` + "`wait`" + ` (bool, default true). When true, blocks until
  the worker finishes and returns the result. Set false for
  long-running tasks — you will be notified when the worker finishes.
- **jevon_kill_session** — Terminate a worker session.
  Required: ` + "`id`" + `.

### Guidelines

- For simple questions (math, general knowledge), answer directly
  without creating workers.
- For coding tasks, create a worker with a clear, descriptive name.
- One task per worker. Create multiple workers for parallel work.
- Use ` + "`jevon_send_command`" + ` with ` + "`wait: false`" + ` for tasks
  that will take a while. The system will notify you when the worker
  finishes.
- Use ` + "`jevon_send_command`" + ` with ` + "`wait: true`" + ` (default)
  when you need the result immediately for a quick task.
- When a worker finishes, summarize the result conversationally.
- If the user's request is vague, ask a clarifying question before
  creating a worker. Always ask questions as plain conversational text
  in your response — never use structured prompts or tool calls to
  request input.

## Event Format

Messages arrive in this format:

` + "```" + `
[USER] what the user said
[WORKER name (id)] Completed: summary of what happened
[WORKER name (id)] Failed: error description
` + "```" + `

Respond to all events that need a response. If multiple events arrive
together, address them in a natural conversational flow.

## Directory Layout

All repos live under ` + "`~/work/github.com/<org>/<repo>`" + `. For example:
- ` + "`~/work/github.com/marcelocantos/jevon`" + `
- ` + "`~/work/github.com/squz/multimaze`" + `
- ` + "`~/work/github.com/minicadesmobile/kart-stars`" + `

When creating workers for a repo, set the workdir accordingly.

## Tool Restrictions

You may ONLY use the jevon MCP tools listed above.
Do not use Bash. Do not read or write files.
Do not run other commands.
`

func main() {
	port := flag.Int("port", 8080, "listen port")
	workDir := flag.String("workdir", ".", "default working directory for worker sessions")
	model := flag.String("model", "", "default model for worker sessions")
	jevonModel := flag.String("jevon-model", "", "model for Jevon (default: same as --model)")
	debug := flag.Bool("debug", false, "enable debug logging")
	showVersion := flag.Bool("version", false, "print version and exit")
	helpAgent := flag.Bool("help-agent", false, "print agent guide and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("jevond", cli.Version)
		os.Exit(0)
	}
	if *helpAgent {
		flag.PrintDefaults()
		fmt.Println()
		fmt.Print(cli.AgentGuide)
		os.Exit(0)
	}

	logLevel := slog.LevelInfo
	if *debug {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	})))

	// Resolve Jevon model.
	jevModel := *jevonModel
	if jevModel == "" {
		jevModel = *model
	}

	// Set up Jevon workdir with CLAUDE.md.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		slog.Error("cannot determine home directory", "err", err)
		os.Exit(1)
	}
	jevDir := filepath.Join(homeDir, ".jevon", "jevon")
	if err := os.MkdirAll(jevDir, 0o755); err != nil {
		slog.Error("cannot create jevon workdir", "err", err)
		os.Exit(1)
	}
	// Build Jevon CLAUDE.md, injecting managed-repos if available.
	jevContent := jevonCLAUDEMD
	reposFile := filepath.Join(homeDir, ".claude", "managed-repos.md")
	if data, err := os.ReadFile(reposFile); err == nil {
		jevContent += "\n## User's Repositories\n\n" + string(data)
	}
	claudeMD := filepath.Join(jevDir, "CLAUDE.md")
	if err := os.WriteFile(claudeMD, []byte(jevContent), 0o644); err != nil {
		slog.Error("cannot write jevon CLAUDE.md", "err", err)
		os.Exit(1)
	}

	// Write .mcp.json for Jevon to discover the MCP server.
	mcpJSON := fmt.Sprintf(
		`{"mcpServers":{"jevon":{"url":"http://localhost:%d/mcp"}}}`, *port)
	mcpJSONPath := filepath.Join(jevDir, ".mcp.json")
	if err := os.WriteFile(mcpJSONPath, []byte(mcpJSON), 0o644); err != nil {
		slog.Error("cannot write .mcp.json", "err", err)
		os.Exit(1)
	}

	// Open database.
	dbPath := filepath.Join(homeDir, ".jevon", "jevon.db")
	database, err := db.Open(dbPath)
	if err != nil {
		slog.Error("cannot open database", "path", dbPath, "err", err)
		os.Exit(1)
	}
	defer database.Close()

	// Create components.
	scanner := discovery.NewScanner(filepath.Join(homeDir, ".claude", "projects"))
	mgr := manager.New(*model, *workDir, database, scanner)

	jev := jevon.New(jevon.Config{
		WorkDir:  jevDir,
		Model:    jevModel,
		ClaudeID: database.Get("jevon_claude_id"),
	})
	jev.SetClaudeIDCallback(func(id string) {
		if err := database.Set("jevon_claude_id", id); err != nil {
			slog.Error("failed to persist jevon claude ID", "err", err)
		}
	})

	srv := server.New(jev, database, cli.Version)

	// Wire MCP server with Jevon event callback.
	mcpSrv := mcpserver.New(mgr, *workDir, func(workerID, workerName, result string, failed bool) {
		kind := jevon.EventWorkerCompleted
		if failed {
			kind = jevon.EventWorkerFailed
		}
		jev.Enqueue(jevon.Event{
			Kind:       kind,
			WorkerID:   workerID,
			WorkerName: workerName,
			Detail:     result,
		})
	})

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	mcpSrv.RegisterRoutes(mux)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start Jevon event loop.
	go jev.Run(ctx)

	listenAddr := fmt.Sprintf(":%d", *port)
	httpSrv := &http.Server{Addr: listenAddr, Handler: mux}

	// Graceful shutdown on signal.
	go func() {
		sig := <-sigCh
		slog.Info("shutting down", "signal", sig)
		cancel()
		httpSrv.Close()
	}()

	slog.Info("jevond starting", "addr", listenAddr, "version", cli.Version,
		"jevon_model", jevModel, "worker_model", *model)
	if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}
