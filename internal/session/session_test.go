package session

import (
	"context"
	"encoding/json"
	"testing"
)

func TestParseSystemEvent(t *testing.T) {
	line := `{"type":"system","subtype":"init","session_id":"abc-123","tools":[],"model":"claude-sonnet-4-6"}`
	events := ParseLine([]byte(line))

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != EventInit {
		t.Errorf("expected type %q, got %q", EventInit, ev.Type)
	}
	if ev.SessionID != "abc-123" {
		t.Errorf("expected session_id %q, got %q", "abc-123", ev.SessionID)
	}
}

func TestParseAssistantTextEvent(t *testing.T) {
	msg := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "Hello, world!"},
			},
		},
	}
	line, _ := json.Marshal(msg)
	events := ParseLine(line)

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != EventText {
		t.Errorf("expected type %q, got %q", EventText, ev.Type)
	}
	if ev.Content != "Hello, world!" {
		t.Errorf("expected content %q, got %q", "Hello, world!", ev.Content)
	}
}

func TestParseAssistantToolUseEvent(t *testing.T) {
	msg := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type":  "tool_use",
					"id":    "toolu_abc",
					"name":  "Bash",
					"input": map[string]any{"command": "ls -la"},
				},
			},
		},
	}
	line, _ := json.Marshal(msg)
	events := ParseLine(line)

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != EventToolUse {
		t.Errorf("expected type %q, got %q", EventToolUse, ev.Type)
	}
	if ev.ToolName != "Bash" {
		t.Errorf("expected tool name %q, got %q", "Bash", ev.ToolName)
	}
	if ev.ToolID != "toolu_abc" {
		t.Errorf("expected tool ID %q, got %q", "toolu_abc", ev.ToolID)
	}

	var input map[string]any
	if err := json.Unmarshal([]byte(ev.ToolInput), &input); err != nil {
		t.Fatalf("failed to parse tool input: %v", err)
	}
	if input["command"] != "ls -la" {
		t.Errorf("expected command %q, got %q", "ls -la", input["command"])
	}
}

func TestParseAssistantMixedContent(t *testing.T) {
	msg := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "Let me check."},
				map[string]any{
					"type":  "tool_use",
					"id":    "toolu_xyz",
					"name":  "Read",
					"input": map[string]any{"file_path": "/tmp/test"},
				},
			},
		},
	}
	line, _ := json.Marshal(msg)
	events := ParseLine(line)

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != EventText {
		t.Errorf("event 0: expected type %q, got %q", EventText, events[0].Type)
	}
	if events[1].Type != EventToolUse {
		t.Errorf("event 1: expected type %q, got %q", EventToolUse, events[1].Type)
	}
}

func TestParseResultSuccess(t *testing.T) {
	msg := map[string]any{
		"type":           "result",
		"subtype":        "success",
		"result":         "Done!",
		"duration_ms":    1234.5,
		"total_cost_usd": 0.05,
	}
	line, _ := json.Marshal(msg)
	events := ParseLine(line)

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != EventResult {
		t.Errorf("expected type %q, got %q", EventResult, ev.Type)
	}
	if ev.Content != "Done!" {
		t.Errorf("expected content %q, got %q", "Done!", ev.Content)
	}
	if ev.DurationMs != 1234.5 {
		t.Errorf("expected duration 1234.5, got %f", ev.DurationMs)
	}
	if ev.CostUSD != 0.05 {
		t.Errorf("expected cost 0.05, got %f", ev.CostUSD)
	}
	if ev.IsError {
		t.Error("expected IsError false")
	}
}

func TestParseResultError(t *testing.T) {
	msg := map[string]any{
		"type":    "result",
		"subtype": "error_tool",
		"errors": []any{
			map[string]any{"message": "tool failed"},
			map[string]any{"message": "another error"},
		},
	}
	line, _ := json.Marshal(msg)
	events := ParseLine(line)

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != EventError {
		t.Errorf("expected type %q, got %q", EventError, ev.Type)
	}
	if !ev.IsError {
		t.Error("expected IsError true")
	}
	if ev.ErrorMsg != "tool failed; another error" {
		t.Errorf("expected error msg %q, got %q", "tool failed; another error", ev.ErrorMsg)
	}
}

func TestParseUnknownType(t *testing.T) {
	line := `{"type":"stream_event","event":{"type":"content_block_delta"}}`
	events := ParseLine([]byte(line))
	if len(events) != 0 {
		t.Errorf("expected 0 events for unknown type, got %d", len(events))
	}
}

func TestParseInvalidJSON(t *testing.T) {
	events := ParseLine([]byte("not json"))
	if len(events) != 0 {
		t.Errorf("expected 0 events for invalid JSON, got %d", len(events))
	}
}

func TestParseEmptyContent(t *testing.T) {
	msg := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": ""},
			},
		},
	}
	line, _ := json.Marshal(msg)
	events := ParseLine(line)
	if len(events) != 0 {
		t.Errorf("expected 0 events for empty text, got %d", len(events))
	}
}

func TestNewSession(t *testing.T) {
	s := New(Config{
		ID:      "test-123",
		Name:    "my session",
		WorkDir: "/tmp",
		Model:   "sonnet",
	})
	if s.ID() != "test-123" {
		t.Errorf("expected ID %q, got %q", "test-123", s.ID())
	}
	if s.Name() != "my session" {
		t.Errorf("expected name %q, got %q", "my session", s.Name())
	}
	if s.Status() != StatusIdle {
		t.Errorf("expected status %q, got %q", StatusIdle, s.Status())
	}
}

func TestStopSession(t *testing.T) {
	s := New(Config{ID: "test-456", Name: "test"})
	s.Stop()
	if s.Status() != StatusStopped {
		t.Errorf("expected status %q, got %q", StatusStopped, s.Status())
	}
	_, err := s.Run(context.Background(), "hello")
	if err == nil {
		t.Error("expected error running stopped session")
	}
}
