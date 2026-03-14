// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"sync"
	"time"
)

// MessageEntry is a chat message in the view state.
type MessageEntry struct {
	Role      string `json:"role"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp"`
}

// SessionEntry is a session summary for the view state.
type SessionEntry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	WorkDir string `json:"workdir"`
	Active  bool   `json:"active"`
}

// ViewState tracks the current app state for Lua view rendering.
type ViewState struct {
	mu sync.RWMutex

	connected     bool
	version       string
	status        string // "idle" or "thinking"
	messages      []MessageEntry
	streamingText string // partial text during streaming
	sessions      []SessionEntry
	activeSheet   string // "" = none, "sessions" = session list
}

// NewViewState creates a new ViewState in disconnected state.
func NewViewState() *ViewState {
	return &ViewState{
		status: "idle",
	}
}

// SetConnected marks the client as connected with the given server version.
func (vs *ViewState) SetConnected(version string) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	vs.connected = true
	vs.version = version
}

// SetDisconnected marks the client as disconnected.
func (vs *ViewState) SetDisconnected() {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	vs.connected = false
}

// AddMessage appends a completed message to the transcript.
func (vs *ViewState) AddMessage(role, text string) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	vs.messages = append(vs.messages, MessageEntry{
		Role:      role,
		Text:      text,
		Timestamp: time.Now().Format(time.RFC3339),
	})
}

// SetStatus updates the server status (idle/thinking).
func (vs *ViewState) SetStatus(state string) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	vs.status = state
}

// UpdateStreamingText sets the partial streaming text.
func (vs *ViewState) UpdateStreamingText(text string) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	vs.streamingText += text
}

// FlushStreaming moves accumulated streaming text into a message.
func (vs *ViewState) FlushStreaming() {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	if vs.streamingText != "" {
		vs.messages = append(vs.messages, MessageEntry{
			Role:      "jevon",
			Text:      vs.streamingText,
			Timestamp: time.Now().Format(time.RFC3339),
		})
		vs.streamingText = ""
	}
}

// SetSessions replaces the session list.
func (vs *ViewState) SetSessions(sessions []SessionEntry) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	vs.sessions = sessions
}

// SetSheet opens or closes a sheet. Use "" to close.
func (vs *ViewState) SetSheet(sheet string) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	vs.activeSheet = sheet
}

// Sheet returns the currently active sheet name.
func (vs *ViewState) Sheet() string {
	vs.mu.RLock()
	defer vs.mu.RUnlock()
	return vs.activeSheet
}

// Render calls the appropriate Lua screen function with the current state
// and returns a ViewMessage. If a sheet is open, it renders both main and sheet.
func (vs *ViewState) Render(rt *LuaRuntime) ([]any, error) {
	vs.mu.RLock()
	state := vs.snapshot()
	sheet := vs.activeSheet
	vs.mu.RUnlock()

	var msgs []any

	// Pick the main screen.
	screenName := "chat_screen"
	if !vs.connected {
		screenName = "connect_screen"
	}

	root, err := rt.CallScreen(screenName, state)
	if err != nil {
		return nil, err
	}
	msgs = append(msgs, ViewMessage{
		Type: "view",
		Root: *root,
	})

	// Render sheet if open.
	if sheet != "" {
		sheetScreen := sheet + "_screen"
		sheetRoot, err := rt.CallScreen(sheetScreen, state)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, ViewMessage{
			Type: "view",
			Root: *sheetRoot,
			Slot: "sheet",
		})
	}

	return msgs, nil
}

// snapshot builds the state map for Lua. Must be called with mu held.
func (vs *ViewState) snapshot() map[string]any {
	// Convert messages to []any for goToLua.
	msgs := make([]any, len(vs.messages))
	for i, m := range vs.messages {
		msgs[i] = map[string]any{
			"role":      m.Role,
			"text":      m.Text,
			"timestamp": m.Timestamp,
		}
	}

	sessions := make([]any, len(vs.sessions))
	for i, s := range vs.sessions {
		sessions[i] = map[string]any{
			"id":      s.ID,
			"name":    s.Name,
			"status":  s.Status,
			"workdir": s.WorkDir,
			"active":  s.Active,
		}
	}

	state := map[string]any{
		"connected":      vs.connected,
		"version":        vs.version,
		"status":         vs.status,
		"messages":       msgs,
		"streaming_text": vs.streamingText,
		"sessions":       sessions,
	}
	return state
}
