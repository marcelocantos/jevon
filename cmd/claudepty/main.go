// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// claudepty spawns a persistent Claude Code instance in a PTY and
// exposes it over a WebSocket. The PTY keeps claude alive in
// interactive mode; the session JSONL provides structured events.
//
// WebSocket protocol:
//
//	Client → Server: plain text user messages
//	Server → Client: JSONL event lines from the session transcript
//
// Also serves:
//
//	GET  /status  — JSON: {alive, sessionID, jsonl}
//	POST /stop    — kill the claude process
//
// Usage: claudepty [--port 9119] [--workdir .]
package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
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

	"github.com/coder/websocket"
	"github.com/creack/pty"
	"github.com/google/uuid"
)

type server struct {
	sessionID string
	jsonlPath string
	ptmx      *os.File
	cmd       *exec.Cmd

	mu        sync.Mutex
	alive     bool
	listeners []chan string
}

func main() {
	port := "9119"
	workdir := "."

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port":
			i++
			port = args[i]
		case "--workdir":
			i++
			workdir = args[i]
		default:
			workdir = args[i]
		}
	}
	workdir, _ = filepath.Abs(workdir)

	// Derive JSONL path.
	var escaped strings.Builder
	for _, r := range workdir {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			escaped.WriteRune(r)
		} else {
			escaped.WriteByte('-')
		}
	}
	sessionID := uuid.New().String()
	jsonlDir := filepath.Join(os.Getenv("HOME"), ".claude", "projects", escaped.String())
	jsonlPath := filepath.Join(jsonlDir, sessionID+".jsonl")

	slog.Info("starting", "workdir", workdir, "session", sessionID, "port", port)

	// Create a PTY pair manually. The child gets the slave as its
	// stdin/stdout/stderr. We keep the master for writing messages.
	ptmx, pts, err := pty.Open()
	if err != nil {
		slog.Error("pty.Open failed", "err", err)
		os.Exit(1)
	}

	cmd := exec.Command("claude",
		"--permission-mode", "bypassPermissions",
		"--session-id", sessionID,
	)
	cmd.Dir = workdir
	cmd.Env = filterEnv(os.Environ(), "CLAUDECODE")
	cmd.Stdin = pts
	cmd.Stdout = pts
	cmd.Stderr = pts
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}

	if err := cmd.Start(); err != nil {
		slog.Error("start failed", "err", err)
		os.Exit(1)
	}
	// Close slave side in parent — child owns it now.
	pts.Close()

	s := &server{
		sessionID: sessionID,
		jsonlPath: jsonlPath,
		ptmx:      ptmx,
		cmd:       cmd,
		alive:     true,
	}

	// Drain PTY master output (discard — we read JSONL instead).
	go io.Copy(io.Discard, ptmx)

	// Monitor process exit.
	go func() {
		err := cmd.Wait()
		slog.Info("claude exited", "err", err)
		s.mu.Lock()
		s.alive = false
		s.mu.Unlock()
	}()

	// Tail JSONL and broadcast to listeners.
	go s.tailJSONL()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", serveIndex)
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("POST /stop", s.handleStop)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("shutting down")
		// Signal the child's process group.
		syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		time.Sleep(time.Second)
		cmd.Process.Kill()
		ptmx.Close()
		os.Exit(0)
	}()

	addr := ":" + port
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server failed", "err", err)
	}
}

//go:embed index.html
var indexHTML []byte

func serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func (s *server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		slog.Error("ws accept failed", "err", err)
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()
	slog.Info("client connected")

	// Subscribe to JSONL events.
	ch := make(chan string, 256)
	s.mu.Lock()
	s.listeners = append(s.listeners, ch)
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		for i, l := range s.listeners {
			if l == ch {
				s.listeners = append(s.listeners[:i], s.listeners[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		slog.Info("client disconnected")
	}()

	// Server → Client: forward JSONL events.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case line := <-ch:
				writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				if err := conn.Write(writeCtx, websocket.MessageText, []byte(line)); err != nil {
					cancel()
					return
				}
				cancel()
			}
		}
	}()

	// Client → Server: read messages and write to PTY.
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			continue
		}
		slog.Info("received", "msg", msg)
		// PTY uses \r (carriage return) to submit, not \n.
		s.ptmx.Write([]byte(msg + "\r"))
	}
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	alive := s.alive
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"alive":     alive,
		"sessionID": s.sessionID,
		"jsonl":     s.jsonlPath,
	})
}

func (s *server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.cmd.Process.Signal(syscall.SIGTERM)
	fmt.Fprintf(w, "stopping\n")
}

func (s *server) broadcast(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.listeners {
		select {
		case ch <- line:
		default:
		}
	}
}

func (s *server) tailJSONL() {
	for {
		if _, err := os.Stat(s.jsonlPath); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	slog.Info("JSONL found", "path", s.jsonlPath)

	f, err := os.Open(s.jsonlPath)
	if err != nil {
		slog.Error("open JSONL failed", "err", err)
		return
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		prettyPrint(line)
		s.broadcast(line)
	}
}

// ANSI colour codes for JSON5 syntax highlighting.
const (
	cReset  = "\033[0m"
	cKey    = "\033[38;5;117m" // light blue
	cString = "\033[38;5;179m" // gold
	cNumber = "\033[38;5;150m" // green
	cBool   = "\033[38;5;204m" // pink
	cNull   = "\033[38;5;243m" // grey
	cBrace  = "\033[38;5;243m" // grey
)

func prettyPrint(line string) {
	var v any
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		fmt.Println(line)
		return
	}
	printValue(v)
	fmt.Println()
}

func printValue(v any) {
	switch val := v.(type) {
	case map[string]any:
		fmt.Printf("%s{%s", cBrace, cReset)
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		for i, k := range keys {
			fmt.Printf("%s%s%s: ", cKey, json5Key(k), cReset)
			printValue(val[k])
			if i < len(keys)-1 {
				fmt.Print(", ")
			}
		}
		fmt.Printf("%s}%s", cBrace, cReset)
	case []any:
		fmt.Printf("%s[%s", cBrace, cReset)
		for i, item := range val {
			printValue(item)
			if i < len(val)-1 {
				fmt.Print(", ")
			}
		}
		fmt.Printf("%s]%s", cBrace, cReset)
	case string:
		s := val
		if len(s) > 120 {
			s = s[:117] + "..."
		}
		fmt.Printf("%s%q%s", cString, s, cReset)
	case float64:
		if val == float64(int64(val)) {
			fmt.Printf("%s%d%s", cNumber, int64(val), cReset)
		} else {
			fmt.Printf("%s%g%s", cNumber, val, cReset)
		}
	case bool:
		fmt.Printf("%s%t%s", cBool, val, cReset)
	case nil:
		fmt.Printf("%snull%s", cNull, cReset)
	default:
		fmt.Printf("%v", val)
	}
}

func json5Key(k string) string {
	if len(k) == 0 {
		return fmt.Sprintf("%q", k)
	}
	for i, r := range k {
		if i == 0 {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || r == '$') {
				return fmt.Sprintf("%q", k)
			}
		} else {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '$') {
				return fmt.Sprintf("%q", k)
			}
		}
	}
	return k
}

func filterEnv(env []string, exclude string) []string {
	var out []string
	prefix := exclude + "="
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}
