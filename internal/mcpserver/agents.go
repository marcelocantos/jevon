// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/jevon/internal/claude"
)

// NotifyFunc injects a text message into the Jevon overseer's PTY input.
type NotifyFunc func(text string)

// SetRegistry attaches the agent registry to the MCP server and
// registers agent management tools.
func (s *Server) SetRegistry(registry *claude.Registry) {
	s.registry = registry

	s.mcpSrv.AddTool(
		mcp.NewTool("jevon_agent_list",
			mcp.WithDescription("List all registered agents and their status (running/stopped)."),
		),
		s.handleAgentList,
	)

	s.mcpSrv.AddTool(
		mcp.NewTool("jevon_agent_start",
			mcp.WithDescription("Start a persistent agent in a repo/directory. Creates and registers it if new. The agent runs as a persistent Claude Code process that retains conversation across messages."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Unique agent name (e.g. 'tern', 'jevon-frontend')")),
			mcp.WithString("workdir", mcp.Required(), mcp.Description("Working directory for the agent (e.g. '~/work/github.com/marcelocantos/tern')")),
			mcp.WithString("model", mcp.Description("Model override (e.g. 'opus', 'sonnet')")),
		),
		s.handleAgentStart,
	)

	s.mcpSrv.AddTool(
		mcp.NewTool("jevon_agent_send",
			mcp.WithDescription("Send a message to a running agent. Returns immediately — the agent processes asynchronously. When the agent responds, you will receive a notification with the response text."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Agent name")),
			mcp.WithString("text", mcp.Required(), mcp.Description("Message to send")),
		),
		s.handleAgentSend,
	)

	s.mcpSrv.AddTool(
		mcp.NewTool("jevon_agent_stop",
			mcp.WithDescription("Stop a running agent. It can be restarted later and will resume its session."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Agent name")),
		),
		s.handleAgentStop,
	)
}

// SetNotify sets the callback for injecting notifications into the
// Jevon overseer (e.g. agent responses).
func (s *Server) SetNotify(fn NotifyFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifyJevon = fn
}

func (s *Server) handleAgentList(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	defs := s.registry.List()
	if len(defs) == 0 {
		return mcp.NewToolResultText("No agents registered."), nil
	}

	var b strings.Builder
	for _, d := range defs {
		proc := s.registry.Get(d.Name)
		status := "stopped"
		if proc != nil && proc.Alive() {
			status = "running"
		}
		fmt.Fprintf(&b, "%-20s %-10s %s (session: %s)\n", d.Name, status, d.WorkDir, d.SessionID[:8])
	}
	return mcp.NewToolResultText(b.String()), nil
}

func (s *Server) handleAgentStart(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	name, _ := args["name"].(string)
	workdir, _ := args["workdir"].(string)
	model, _ := args["model"].(string)

	if name == "" || workdir == "" {
		return mcp.NewToolResultError("name and workdir are required"), nil
	}

	// Expand ~ in workdir.
	if strings.HasPrefix(workdir, "~/") {
		home, _ := os.UserHomeDir()
		workdir = home + workdir[1:]
	}

	def, err := s.registry.EnsureAgent(name, workdir, model, true)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("register failed: %v", err)), nil
	}

	proc, err := s.registry.Start(name)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("start failed: %v", err)), nil
	}

	// Wire events: broadcast to web UI and notify Jevon on agent responses.
	s.wireAgentEvents(name, proc)

	return mcp.NewToolResultText(fmt.Sprintf("Agent %q started (session: %s, workdir: %s)", name, def.SessionID[:8], def.WorkDir)), nil
}

func (s *Server) handleAgentSend(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	name, _ := args["name"].(string)
	text, _ := args["text"].(string)

	if name == "" || text == "" {
		return mcp.NewToolResultError("name and text are required"), nil
	}

	proc := s.registry.Get(name)
	if proc == nil || !proc.Alive() {
		return mcp.NewToolResultError(fmt.Sprintf("agent %q is not running", name)), nil
	}

	if err := proc.Send(text); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("send failed: %v", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Message sent to %q. You will be notified when it responds.", name)), nil
}

func (s *Server) handleAgentStop(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	name, _ := args["name"].(string)
	if name == "" {
		return mcp.NewToolResultError("name is required"), nil
	}

	s.registry.Stop(name)
	return mcp.NewToolResultText(fmt.Sprintf("Agent %q stopped.", name)), nil
}

// wireAgentEvents sets up the event handler for an agent process.
// It broadcasts to the web UI and notifies Jevon when the agent
// produces a text response.
func (s *Server) wireAgentEvents(name string, proc *claude.Process) {
	var mu sync.Mutex
	var responseText strings.Builder

	proc.OnEvent(func(ev claude.Event) {
		// Broadcast raw event to web UI activity feed.
		s.broadcastAgentEvent(name, ev)

		mu.Lock()
		defer mu.Unlock()

		switch ev.Type {
		case "assistant":
			if ev.Text != "" {
				responseText.WriteString(ev.Text)
			}
		case "system":
			// Turn complete — notify Jevon with the accumulated response.
			text := responseText.String()
			responseText.Reset()
			if text != "" {
				s.notify(name, text)
			}
		}
	})
}

// notify injects an agent response notification into Jevon's PTY.
func (s *Server) notify(agentName, text string) {
	s.mu.Lock()
	fn := s.notifyJevon
	s.mu.Unlock()

	if fn == nil {
		slog.Warn("agent response but no notify function set", "agent", agentName)
		return
	}

	// Truncate very long responses for the notification.
	if len(text) > 2000 {
		text = text[:1997] + "..."
	}

	msg := fmt.Sprintf("[Agent %s responded]\n%s", agentName, text)
	slog.Info("notifying jevon", "agent", agentName, "len", len(text))
	fn(msg)
}

// broadcastAgentEvent sends agent events to the web UI.
func (s *Server) broadcastAgentEvent(name string, ev claude.Event) {
	data, _ := json.Marshal(map[string]any{
		"type":  "agent_event",
		"agent": name,
		"event": json.RawMessage(ev.Raw),
	})
	_ = data // TODO: wire to activity WebSocket via BroadcastChat
}
