// Package worker manages Claude Code worker processes,
// including spawning, lifecycle management, and command routing.
// Supports hybrid mode: raw API for quick responses, auto-escalation
// to CLI subprocess when tools are needed.
package worker
