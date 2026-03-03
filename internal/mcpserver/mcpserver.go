// Copyright 2025 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package mcpserver exposes jevon worker management as MCP tools,
// replacing the jevon-ctl CLI binary with an in-process MCP server.
package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/marcelocantos/jevon/internal/manager"
	"github.com/marcelocantos/jevon/internal/session"
)

// EventCallback is called when a worker finishes a command.
type EventCallback func(workerID, workerName, result string, failed bool)

// Server wraps an MCP server that provides worker management tools.
type Server struct {
	mgr       *manager.Manager
	workerWD  string
	onDone    EventCallback
	transport *server.StreamableHTTPServer
}

// New creates an MCP server with 5 jevon tools wired to the given manager.
func New(mgr *manager.Manager, workerWD string, onDone EventCallback) *Server {
	s := &Server{
		mgr:      mgr,
		workerWD: workerWD,
		onDone:   onDone,
	}

	mcpSrv := server.NewMCPServer("jevon", "1.0.0")

	mcpSrv.AddTool(
		mcp.NewTool("jevon_list_sessions",
			mcp.WithDescription("List worker sessions and their status. Returns the most relevant sessions by default."),
			mcp.WithBoolean("all", mcp.Description("Show all sessions, not just the most relevant")),
		),
		s.handleListSessions,
	)

	mcpSrv.AddTool(
		mcp.NewTool("jevon_session_status",
			mcp.WithDescription("Get detailed status and last result of a worker session."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Worker session ID (UUID)")),
		),
		s.handleSessionStatus,
	)

	mcpSrv.AddTool(
		mcp.NewTool("jevon_create_session",
			mcp.WithDescription("Create a new worker session for a coding task."),
			mcp.WithString("name", mcp.Description("Human-readable name for the session")),
			mcp.WithString("workdir", mcp.Description("Working directory (defaults to the coordinator's default)")),
			mcp.WithString("model", mcp.Description("Model override (e.g. 'opus', 'sonnet')")),
		),
		s.handleCreateSession,
	)

	mcpSrv.AddTool(
		mcp.NewTool("jevon_send_command",
			mcp.WithDescription("Send a command to a worker session. By default waits for completion and returns the result."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Worker session ID (UUID)")),
			mcp.WithString("text", mcp.Required(), mcp.Description("The task or prompt to send")),
			mcp.WithBoolean("wait", mcp.Description("Wait for completion (default true). Set false to return immediately.")),
		),
		s.handleSendCommand,
	)

	mcpSrv.AddTool(
		mcp.NewTool("jevon_kill_session",
			mcp.WithDescription("Terminate a worker session."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Worker session ID (UUID)")),
		),
		s.handleKillSession,
	)

	s.transport = server.NewStreamableHTTPServer(mcpSrv, server.WithStateLess(true))
	return s
}

// RegisterRoutes adds the MCP endpoint to the given mux.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("/mcp", s.transport)
}

// --- tool handlers ---

func (s *Server) handleListSessions(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	all, _ := args["all"].(bool)
	sessions := s.mgr.List(all)

	if len(sessions) == 0 {
		return mcp.NewToolResultText("No sessions found."), nil
	}

	var b strings.Builder
	for _, sess := range sessions {
		status := string(sess.Status)
		if sess.Active {
			status = "ACTIVE"
		}
		name := sess.WorkDir
		if name == "" {
			name = sess.Name
		}
		fmt.Fprintf(&b, "%-38s  %-8s  %s\n", sess.ID, status, name)
	}
	return mcp.NewToolResultText(b.String()), nil
}

func (s *Server) handleSessionStatus(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	id, _ := args["id"].(string)
	if id == "" {
		return mcp.NewToolResultError("missing required parameter: id"), nil
	}

	sess := s.mgr.Get(id)
	if sess == nil {
		return mcp.NewToolResultError(fmt.Sprintf("worker %s not found", id)), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Worker: %s (%s)\n", sess.Name(), sess.ID())
	fmt.Fprintf(&b, "Status: %s\n", sess.Status())
	if lr := sess.LastResult(); lr != "" {
		fmt.Fprintf(&b, "Last result:\n%s\n", lr)
	}
	return mcp.NewToolResultText(b.String()), nil
}

func (s *Server) handleCreateSession(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	name, _ := args["name"].(string)
	workdir, _ := args["workdir"].(string)
	model, _ := args["model"].(string)

	if workdir == "" {
		workdir = s.workerWD
	}

	sess, err := s.mgr.Create(manager.CreateConfig{
		Name:    name,
		WorkDir: workdir,
		Model:   model,
	})
	if err != nil {
		slog.Error("MCP: failed to create session", "err", err)
		return mcp.NewToolResultError(fmt.Sprintf("failed to create session: %v", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Created session %s (%s)", sess.ID(), sess.Name())), nil
}

func (s *Server) handleSendCommand(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	id, _ := args["id"].(string)
	text, _ := args["text"].(string)
	if id == "" {
		return mcp.NewToolResultError("missing required parameter: id"), nil
	}
	if text == "" {
		return mcp.NewToolResultError("missing required parameter: text"), nil
	}

	// Default wait=true.
	wait := true
	if w, ok := args["wait"].(bool); ok {
		wait = w
	}

	if s.mgr.IsExternallyActive(id) {
		return mcp.NewToolResultError("session is in use by another claude process"), nil
	}

	sess := s.mgr.Get(id)
	if sess == nil {
		return mcp.NewToolResultError(fmt.Sprintf("worker %s not found", id)), nil
	}

	if !wait {
		// Fire and forget — notify Jevon via callback when done.
		go s.runAndNotify(id, sess, text)
		return mcp.NewToolResultText(fmt.Sprintf("Command sent to worker %s.", id)), nil
	}

	// Synchronous: run and return the result.
	events, err := sess.Run(ctx, text)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("run failed: %v", err)), nil
	}

	var textParts []string
	for ev := range events {
		if ev.Type == session.EventText {
			textParts = append(textParts, ev.Content)
		}
	}

	result := sess.LastResult()
	if result == "" {
		result = strings.Join(textParts, "")
	}
	if result == "" {
		result = "Worker finished (no result text)."
	}

	return mcp.NewToolResultText(truncate(result, 4000)), nil
}

func (s *Server) handleKillSession(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	id, _ := args["id"].(string)
	if id == "" {
		return mcp.NewToolResultError("missing required parameter: id"), nil
	}

	if err := s.mgr.Kill(id); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to kill worker: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Worker %s killed.", id)), nil
}

// runAndNotify runs a command asynchronously and fires the event callback.
func (s *Server) runAndNotify(id string, sess *session.Session, text string) {
	events, err := sess.Run(context.Background(), text)
	if err != nil {
		slog.Error("worker run failed", "worker", id, "err", err)
		if s.onDone != nil {
			s.onDone(id, sess.Name(), err.Error(), true)
		}
		return
	}

	var textParts []string
	for ev := range events {
		if ev.Type == session.EventText {
			textParts = append(textParts, ev.Content)
		}
	}

	result := sess.LastResult()
	if result == "" {
		result = strings.Join(textParts, "")
	}

	failed := strings.HasPrefix(result, "error: ")
	if s.onDone != nil {
		s.onDone(id, sess.Name(), truncate(result, 2000), failed)
	}
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "\n... (truncated)"
	}
	return s
}
