// Package jevon implements the coordinator that sits between the user
// and multiple Claude Code worker sessions, routing tasks and synthesizing
// results into a conversational interface.
package jevon

import (
	"fmt"
	"strings"
	"time"
)

// EventKind identifies what triggered a Jevon turn.
type EventKind string

const (
	EventUserMessage     EventKind = "user_message"
	EventWorkerCompleted EventKind = "worker_completed"
	EventWorkerFailed    EventKind = "worker_failed"
	EventWorkerStarted   EventKind = "worker_started"
)

// Event is something Jevon needs to know about.
type Event struct {
	Kind      EventKind
	Timestamp time.Time

	// For user_message:
	Text string

	// For worker_* events:
	WorkerID   string
	WorkerName string
	Detail     string // summary of what happened
}

// FormatPrompt converts a batch of events into a prompt string for the
// Jevon LLM. Events are prefixed with [USER] or [WORKER] tags.
func FormatPrompt(events []Event) string {
	var b strings.Builder
	for _, ev := range events {
		switch ev.Kind {
		case EventUserMessage:
			fmt.Fprintf(&b, "[USER] %s\n", ev.Text)
		case EventWorkerCompleted:
			fmt.Fprintf(&b, "[WORKER %s (%s)] Completed: %s\n",
				ev.WorkerName, ev.WorkerID, ev.Detail)
		case EventWorkerFailed:
			fmt.Fprintf(&b, "[WORKER %s (%s)] Failed: %s\n",
				ev.WorkerName, ev.WorkerID, ev.Detail)
		case EventWorkerStarted:
			fmt.Fprintf(&b, "[WORKER %s (%s)] Started\n",
				ev.WorkerName, ev.WorkerID)
		}
	}
	return b.String()
}
