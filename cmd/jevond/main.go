package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/marcelocantos/sqlpipe/go/sqlpipe"

	"github.com/marcelocantos/jevon/internal/cli"
	"github.com/marcelocantos/jevon/internal/db"
	"github.com/marcelocantos/jevon/internal/discovery"
	"github.com/marcelocantos/jevon/internal/jevon"
	"github.com/marcelocantos/jevon/internal/manager"
	"github.com/marcelocantos/jevon/internal/mcpserver"
	"github.com/marcelocantos/tern/qr"
	"github.com/marcelocantos/jevon/internal/server"
	"github.com/marcelocantos/jevon/internal/session"
	jvsync "github.com/marcelocantos/jevon/internal/sync"
	"github.com/marcelocantos/jevon/internal/transcript"
	"github.com/marcelocantos/jevon/internal/ui"
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
	port := flag.Int("port", 13705, "listen port")
	relayURL := flag.String("relay", "", "relay URL to register with (e.g. wss://tern.fly.dev)")
	relayToken := flag.String("relay-token", "", "bearer token for relay authentication (or set TERN_TOKEN env var)")
	workDir := flag.String("workdir", ".", "default working directory for worker sessions")
	model := flag.String("model", "", "default model for worker sessions")
	jevonModel := flag.String("jevon-model", "", "model for Jevon (default: same as --model)")
	debug := flag.Bool("debug", false, "enable debug logging")
	setOpenAIKey := flag.String("set-openai-key", "", "store OpenAI API key in macOS Keychain and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	helpAgent := flag.Bool("help-agent", false, "print agent guide and exit")
	flag.Parse()

	if *setOpenAIKey != "" {
		if err := storeKeychainKey("openai-api-key", *setOpenAIKey); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to store key: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("OpenAI API key stored in macOS Keychain.")
		os.Exit(0)
	}

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

	// Set up sqlpipe sync database (separate from main DB to avoid
	// preupdate hook conflicts).
	//
	// The onRequest callback needs the Server, which doesn't exist yet.
	// srvRef is assigned after server.New() below; the closure captures it
	// by pointer so the nil check works at call time.
	var srvRef *server.Server
	syncDBPath := filepath.Join(homeDir, ".jevon", "sync.db")
	var syncMgr *jvsync.SyncManager
	if syncDB, err := sqlpipe.OpenDatabase(syncDBPath); err != nil {
		slog.Error("sqlpipe: cannot open sync db — running without sync", "err", err)
	} else if err := db.CreateSyncSchema(syncDB); err != nil {
		slog.Error("sqlpipe: schema creation failed — running without sync", "err", err)
		syncDB.Close()
	} else {
		syncDB.Close() // SyncManager opens its own dedicated connections.

		sm, err := jvsync.NewSyncManager(syncDBPath, func(req jvsync.Request) {
			if srvRef == nil {
				return
			}
			switch req.Type {
			case "message":
				srvRef.HandleUserMessage(req.Payload)
			case "action":
				srvRef.HandleAction(req.Payload, "")
			}
		})
		if err != nil {
			slog.Error("sqlpipe: init failed — running without sync", "err", err)
		} else {
			syncMgr = sm
			defer syncMgr.Close()
			if err := syncMgr.SetVersion(cli.Version); err != nil {
				slog.Warn("sqlpipe: failed to set version", "err", err)
			}
			// Seed sync_transcript from legacy transcript table.
			if err := syncMgr.SeedTranscript(database.SqlpipeDB()); err != nil {
				slog.Warn("sqlpipe: transcript seeding failed", "err", err)
			}
			// Flush seed data so it's available for the first client handshake.
			if _, err := syncMgr.Flush(); err != nil {
				slog.Warn("sqlpipe: post-seed flush failed", "err", err)
			}
		}
	}

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

	// Set up Lua view runtime.
	luaViewsDir := filepath.Join(jevDir, "..", "lua", "views")
	if err := os.MkdirAll(luaViewsDir, 0o755); err != nil {
		slog.Error("cannot create lua views dir", "err", err)
		os.Exit(1)
	}
	luaRT, err := ui.NewLuaRuntime(luaViewsDir)
	if err != nil {
		slog.Error("cannot create lua runtime", "err", err)
		os.Exit(1)
	}
	defer luaRT.Close()

	vs := ui.NewViewState()
	vs.SetConnected(cli.Version, os.Getenv("HOME"))

	srv := server.New(jev, mgr, database, cli.Version, luaRT, vs)
	srvRef = srv // Wire the forward reference for syncMgr's onRequest callback.
	if syncMgr != nil {
		srv.SetSyncManager(syncMgr)
		slog.Info("sqlpipe sync enabled")
	}

	// Load OpenAI API key from Keychain (fall back to env var).
	if key, err := loadKeychainKey("openai-api-key"); err == nil && key != "" {
		srv.SetOpenAIKey(key)
		slog.Info("OpenAI API key loaded from Keychain")
	} else if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		srv.SetOpenAIKey(key)
		slog.Info("OpenAI API key loaded from environment")
	}

	// Transcript reader for Lua access to Claude session transcripts.
	transcriptReader := transcript.NewReader(filepath.Join(homeDir, ".claude", "projects"))

	// Timer state — named timers that fire actions through the Lua runtime.
	var (
		timersMu sync.Mutex
		timers   = make(map[string]func()) // name → cancel func
	)
	cancelTimer := func(name string) {
		timersMu.Lock()
		defer timersMu.Unlock()
		if cancel, ok := timers[name]; ok {
			cancel()
			delete(timers, name)
		}
	}

	// File I/O sandbox root.
	sandboxRoot := filepath.Join(homeDir, ".jevon")

	// validateSandbox ensures a path is under ~/.jevon/.
	validateSandbox := func(path string) (string, error) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("invalid path: %w", err)
		}
		// Resolve symlinks to prevent escaping via symlink.
		real, err := filepath.EvalSymlinks(filepath.Dir(abs))
		if err != nil {
			// Dir doesn't exist yet — check the parent chain.
			real = abs
		} else {
			real = filepath.Join(real, filepath.Base(abs))
		}
		if !strings.HasPrefix(real, sandboxRoot) {
			return "", fmt.Errorf("path %q is outside sandbox %q", path, sandboxRoot)
		}
		return abs, nil
	}

	// Register Lua capabilities — Go functions callable from Lua action handlers.
	luaRT.RegisterCapabilities(ui.Capabilities{
		JevonEnqueue: func(text string) {
			srv.HandleUserMessage(text)
		},
		JevonReset: func() {
			if err := database.Set("jevon_claude_id", ""); err != nil {
				slog.Error("failed to reset jevon claude ID", "err", err)
			}
		},
		SessionList: func(all bool) []map[string]any {
			summaries := mgr.List(all)
			result := make([]map[string]any, len(summaries))
			for i, s := range summaries {
				result[i] = map[string]any{
					"id":      s.ID,
					"name":    s.Name,
					"status":  string(s.Status),
					"workdir": s.WorkDir,
					"active":  s.Active,
				}
			}
			return result
		},
		SessionKill: func(id string) error {
			return mgr.Kill(id)
		},
		SessionCreate: func(name, workdir, model string) (string, error) {
			s, err := mgr.Create(manager.CreateConfig{
				Name:    name,
				WorkDir: workdir,
				Model:   model,
			})
			if err != nil {
				return "", err
			}
			return s.ID(), nil
		},
		SessionSend: func(id, text string, wait bool) (string, error) {
			s := mgr.Get(id)
			if s == nil {
				return "", fmt.Errorf("session %q not found", id)
			}
			events, err := s.Run(context.Background(), text)
			if err != nil {
				return "", err
			}
			if !wait {
				go func() {
					for range events {
					}
				}()
				return "command sent", nil
			}
			var result string
			for ev := range events {
				if ev.Type == session.EventText {
					result += ev.Content
				}
			}
			if r := s.LastResult(); r != "" {
				result = r
			}
			return result, nil
		},
		DBGet: func(key string) string {
			return database.Get(key)
		},
		DBSet: func(key, value string) error {
			return database.Set(key, value)
		},
		PushSessions: func() {
			srv.PushSessions()
		},
		PushScripts: func() {
			srv.PushScripts()
		},
		Broadcast: func(msg map[string]any) {
			srv.Broadcast(msg)
		},

		// Transcript access.
		TranscriptRead: func(sessionID string) ([]map[string]any, error) {
			return transcriptReader.Read(sessionID)
		},
		TranscriptTruncate: func(sessionID string, keepTurns int) error {
			return transcriptReader.Truncate(sessionID, keepTurns)
		},
		TranscriptFork: func(sessionID string, keepTurns int) (string, error) {
			return transcriptReader.Fork(sessionID, keepTurns)
		},

		// File I/O (sandboxed to ~/.jevon/).
		FileRead: func(path string) (string, error) {
			abs, err := validateSandbox(path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
		FileWrite: func(path, content string) error {
			abs, err := validateSandbox(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return err
			}
			return os.WriteFile(abs, []byte(content), 0o644)
		},
		FileList: func(dir string) ([]string, error) {
			abs, err := validateSandbox(dir)
			if err != nil {
				return nil, err
			}
			entries, err := os.ReadDir(abs)
			if err != nil {
				return nil, err
			}
			names := make([]string, len(entries))
			for i, e := range entries {
				names[i] = e.Name()
			}
			return names, nil
		},

		// Timers.
		SetTimeout: func(name string, delayMs int, action string) {
			cancelTimer(name)
			timer := time.AfterFunc(time.Duration(delayMs)*time.Millisecond, func() {
				slog.Debug("timer fired", "name", name, "action", action)
				timersMu.Lock()
				delete(timers, name)
				timersMu.Unlock()
				srv.HandleAction(action, "")
			})
			timersMu.Lock()
			timers[name] = func() { timer.Stop() }
			timersMu.Unlock()
		},
		SetInterval: func(name string, intervalMs int, action string) {
			cancelTimer(name)
			ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
			done := make(chan struct{})
			go func() {
				for {
					select {
					case <-ticker.C:
						slog.Debug("interval fired", "name", name, "action", action)
						srv.HandleAction(action, "")
					case <-done:
						ticker.Stop()
						return
					}
				}
			}()
			timersMu.Lock()
			timers[name] = func() { close(done) }
			timersMu.Unlock()
		},
		CancelTimer: cancelTimer,

		// Notifications.
		Notify: func(title, body string) {
			srv.Broadcast(map[string]any{
				"type":  "notification",
				"title": title,
				"body":  body,
			})
		},
	})

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
	}, func() error {
		if err := luaRT.Reload(); err != nil {
			return err
		}
		srv.PushScripts()
		return nil
	}, func(code string) {
		srv.Broadcast(map[string]any{
			"type":   "control",
			"action": "exec_lua",
			"code":   code,
		})
	}, func() (string, error) {
		return srv.RequestScreenshot(10 * time.Second)
	}, &mcpserver.TranscriptOps{
		Read: func(sessionID string) ([]map[string]any, error) {
			tr := transcript.NewReader(filepath.Join(homeDir, ".claude", "projects"))
			return tr.Read(sessionID)
		},
		Truncate: func(sessionID string, keepTurns int) error {
			tr := transcript.NewReader(filepath.Join(homeDir, ".claude", "projects"))
			return tr.Truncate(sessionID, keepTurns)
		},
		ResetID: func() {
			database.Set("jevon_claude_id", "")
		},
		GetID: func() string {
			return database.Get("jevon_claude_id")
		},
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

	// Connect to relay if specified, otherwise print direct QR code.
	if *relayURL != "" {
		token := *relayToken
		if token == "" {
			token = os.Getenv("TERN_TOKEN")
		}
		instanceID, err := srv.ConnectRelay(ctx, *relayURL, token)
		if err != nil {
			slog.Error("relay connection failed", "err", err)
			os.Exit(1)
		}
		// Replace localhost with LAN IP so the QR code works for devices.
		relayWSURL := *relayURL + "/ws/" + instanceID
		relayWSURL = strings.Replace(relayWSURL, "localhost", qr.LanIP(), 1)
		relayWSURL = strings.Replace(relayWSURL, "127.0.0.1", qr.LanIP(), 1)
		qr.Print(os.Stderr, relayWSURL)

		// Write relay URL to a well-known file for programmatic access.
		relayFile := filepath.Join(os.TempDir(), ".tern-relay")
		if err := os.WriteFile(relayFile, []byte(relayWSURL+"\n"), 0o644); err != nil {
			slog.Warn("failed to write relay URL file", "path", relayFile, "err", err)
		} else {
			slog.Info("relay URL written", "path", relayFile)
		}
	} else {
		directURL := fmt.Sprintf("jevon://%s:%d", qr.LanIP(), *port)
		qr.Print(os.Stderr, directURL)
	}

	if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}

// storeKeychainKey stores a value in the macOS Keychain under the "jevon" account.
func storeKeychainKey(service, value string) error {
	// Delete any existing entry first (add fails if it exists).
	exec.Command("security", "delete-generic-password",
		"-a", "jevon", "-s", service).Run()
	return exec.Command("security", "add-generic-password",
		"-a", "jevon", "-s", service, "-w", value).Run()
}

// loadKeychainKey retrieves a value from the macOS Keychain.
func loadKeychainKey(service string) (string, error) {
	out, err := exec.Command("security", "find-generic-password",
		"-a", "jevon", "-s", service, "-w").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
