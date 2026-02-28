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

	"github.com/marcelocantos/dais/internal/ctlapi"
	"github.com/marcelocantos/dais/internal/db"
	"github.com/marcelocantos/dais/internal/manager"
	"github.com/marcelocantos/dais/internal/server"
	"github.com/marcelocantos/dais/internal/shepherd"
)

var version = "dev"

// shepherdCLAUDEMD is the CLAUDE.md template written to the shepherd's workdir.
const shepherdCLAUDEMD = `# Dais Shepherd

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

You manage Claude Code workers via the ` + "`dais-ctl`" + ` command.
Workers are Claude Code sessions that do actual coding work.

### Commands

` + "```bash" + `
# Create a new worker
dais-ctl create --name "descriptive name"

# List all workers
dais-ctl list

# Send a command to a worker (returns immediately)
dais-ctl command <worker-id> "the task description"

# Wait for a worker to finish and get the result
dais-ctl wait <worker-id>

# Check worker status and last result
dais-ctl status <worker-id>

# Kill a worker
dais-ctl kill <worker-id>
` + "```" + `

### Guidelines

- For simple questions (math, general knowledge), answer directly
  without creating workers.
- For coding tasks, create a worker with a clear, descriptive name.
- One task per worker. Create multiple workers for parallel work.
- Use ` + "`dais-ctl command`" + ` for tasks that will take a while.
  The system will notify you when the worker finishes.
- Use ` + "`dais-ctl command <id> ... && dais-ctl wait <id>`" + ` when
  you need the result immediately for a quick task.
- When a worker finishes, summarize the result conversationally.
- If the user's request is vague, ask a clarifying question before
  creating a worker.

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
- ` + "`~/work/github.com/marcelocantos/dais`" + `
- ` + "`~/work/github.com/squz/multimaze`" + `
- ` + "`~/work/github.com/minicadesmobile/kart-stars`" + `

When creating workers for a repo, set the workdir accordingly.

## Tool Restrictions

You may ONLY use the Bash tool to run ` + "`dais-ctl`" + ` commands.
Do not use Bash for anything else. Do not read or write files.
Do not run other commands.
`

func main() {
	port := flag.Int("port", 8080, "listen port")
	workDir := flag.String("workdir", ".", "default working directory for worker sessions")
	model := flag.String("model", "", "default model for worker sessions")
	shepherdModel := flag.String("shepherd-model", "", "model for the shepherd (default: same as --model)")
	debug := flag.Bool("debug", false, "enable debug logging")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("daisd", version)
		os.Exit(0)
	}

	logLevel := slog.LevelInfo
	if *debug {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	})))

	// Resolve shepherd model.
	shepModel := *shepherdModel
	if shepModel == "" {
		shepModel = *model
	}

	// Set up shepherd workdir with CLAUDE.md.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		slog.Error("cannot determine home directory", "err", err)
		os.Exit(1)
	}
	shepDir := filepath.Join(homeDir, ".dais", "shepherd")
	if err := os.MkdirAll(shepDir, 0o755); err != nil {
		slog.Error("cannot create shepherd workdir", "err", err)
		os.Exit(1)
	}
	// Build shepherd CLAUDE.md, injecting managed-repos if available.
	shepContent := shepherdCLAUDEMD
	reposFile := filepath.Join(homeDir, ".claude", "managed-repos.md")
	if data, err := os.ReadFile(reposFile); err == nil {
		shepContent += "\n## User's Repositories\n\n" + string(data)
	}
	claudeMD := filepath.Join(shepDir, "CLAUDE.md")
	if err := os.WriteFile(claudeMD, []byte(shepContent), 0o644); err != nil {
		slog.Error("cannot write shepherd CLAUDE.md", "err", err)
		os.Exit(1)
	}

	// Find dais-ctl binary (next to daisd).
	exe, _ := os.Executable()
	ctlBin := filepath.Join(filepath.Dir(exe), "dais-ctl")

	addr := fmt.Sprintf("http://localhost:%d", *port)

	// Open database.
	dbPath := filepath.Join(homeDir, ".dais", "dais.db")
	database, err := db.Open(dbPath)
	if err != nil {
		slog.Error("cannot open database", "path", dbPath, "err", err)
		os.Exit(1)
	}
	defer database.Close()

	// Create components.
	mgr := manager.New(*model, *workDir)

	shep := shepherd.New(shepherd.Config{
		WorkDir: shepDir,
		Model:   shepModel,
		CtlAddr: addr,
		CtlBin:  ctlBin,
	})

	srv := server.New(shep, database, version)

	// Wire ctlapi with shepherd event callback.
	ctl := ctlapi.New(mgr, *workDir, func(workerID, workerName, result string, failed bool) {
		kind := shepherd.EventWorkerCompleted
		if failed {
			kind = shepherd.EventWorkerFailed
		}
		shep.Enqueue(shepherd.Event{
			Kind:       kind,
			WorkerID:   workerID,
			WorkerName: workerName,
			Detail:     result,
		})
	})

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ctl.RegisterRoutes(mux)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start shepherd event loop.
	go shep.Run(ctx)

	listenAddr := fmt.Sprintf(":%d", *port)
	httpSrv := &http.Server{Addr: listenAddr, Handler: mux}

	// Graceful shutdown on signal.
	go func() {
		sig := <-sigCh
		slog.Info("shutting down", "signal", sig)
		cancel()
		httpSrv.Close()
	}()

	slog.Info("daisd starting", "addr", listenAddr, "version", version,
		"shepherd_model", shepModel, "worker_model", *model)
	if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}
