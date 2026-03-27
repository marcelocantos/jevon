// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/jevon/internal/memory"
)

// SetMemory attaches the transcript memory store and registers search tools.
func (s *Server) SetMemory(mem *memory.Store) {
	s.memory = mem

	s.mcpSrv.AddTool(
		mcp.NewTool("jevon_search_memory",
			mcp.WithDescription("Search across ALL Claude Code session transcripts — every conversation Marcelo has ever had with any Claude instance, across all repos and projects. Use this to recall past discussions, find decisions, look up what was done before."),
			mcp.WithString("query", mcp.Required(), mcp.Description("Search query (FTS5 syntax: words, phrases in quotes, OR, NOT)")),
			mcp.WithNumber("limit", mcp.Description("Max results (default 20)")),
		),
		s.handleSearchMemory,
	)

	s.mcpSrv.AddTool(
		mcp.NewTool("jevon_memory_stats",
			mcp.WithDescription("Show transcript memory statistics — how many sessions and messages are indexed."),
		),
		s.handleMemoryStats,
	)
}

func (s *Server) handleSearchMemory(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	query, _ := args["query"].(string)
	limit := 20
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	if query == "" {
		return mcp.NewToolResultError("query is required"), nil
	}

	results, err := s.memory.Search(query, limit)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
	}

	if len(results) == 0 {
		return mcp.NewToolResultText("No results found."), nil
	}

	var b strings.Builder
	for _, r := range results {
		sid := r.SessionID
		if len(sid) > 8 {
			sid = sid[:8]
		}
		fmt.Fprintf(&b, "[%s] %s | %s | %s\n%s\n\n",
			r.Role, r.Project, sid, r.Timestamp, r.Text)
	}
	return mcp.NewToolResultText(b.String()), nil
}

func (s *Server) handleMemoryStats(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sessions, messages, err := s.memory.Stats()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("stats failed: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Indexed: %d sessions, %d messages", sessions, messages)), nil
}
