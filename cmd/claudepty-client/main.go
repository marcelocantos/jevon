// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// claudepty-client connects to a claudepty server, sends a message,
// and prints all JSONL events received back.
//
// Usage: claudepty-client [--addr localhost:9119] "message to send"
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/coder/websocket"
)

func main() {
	addr := "localhost:9119"
	var message string

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--addr":
			i++
			addr = args[i]
		default:
			message = args[i]
		}
	}

	if message == "" {
		fmt.Fprintln(os.Stderr, "usage: claudepty-client [--addr host:port] \"message\"")
		os.Exit(1)
	}

	ctx := context.Background()

	// Check server status first.
	slog.Info("connecting", "addr", addr)
	conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws", nil)
	if err != nil {
		slog.Error("dial failed", "err", err)
		os.Exit(1)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20) // 1MB

	slog.Info("connected")

	// Start reading events in background.
	done := make(chan struct{})
	turnDone := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				slog.Info("read done", "err", err)
				return
			}
			line := string(data)

			// Parse and pretty-print.
			var entry map[string]any
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				fmt.Printf("RAW: %s\n", line)
				continue
			}

			typ, _ := entry["type"].(string)
			switch typ {
			case "assistant":
				msg, _ := entry["message"].(map[string]any)
				content, _ := msg["content"].([]any)
				for _, c := range content {
					cm, _ := c.(map[string]any)
					if cm["type"] == "text" {
						fmt.Printf("[assistant] %s\n", cm["text"])
					}
				}
			case "user":
				msg, _ := entry["message"].(map[string]any)
				content, _ := msg["content"].([]any)
				for _, c := range content {
					cm, _ := c.(map[string]any)
					if cm["type"] == "text" {
						fmt.Printf("[user] %s\n", cm["text"])
					}
				}
			case "progress":
				data, _ := entry["data"].(map[string]any)
				ptype, _ := data["type"].(string)
				fmt.Printf("[progress] %s\n", ptype)
			case "last-prompt":
				prompt, _ := entry["lastPrompt"].(string)
				fmt.Printf("[turn-end] %s\n", truncate(prompt, 80))
				select {
				case turnDone <- struct{}{}:
				default:
				}
			case "system":
				fmt.Printf("[system] turn complete\n")
				select {
				case turnDone <- struct{}{}:
				default:
				}
			default:
				raw, _ := json.Marshal(entry)
				fmt.Printf("[%s] %s\n", typ, truncate(string(raw), 120))
			}
		}
	}()

	// Wait a moment for the connection to stabilise.
	time.Sleep(500 * time.Millisecond)

	// Send the message.
	slog.Info("sending", "msg", message)
	if err := conn.Write(ctx, websocket.MessageText, []byte(message)); err != nil {
		slog.Error("write failed", "err", err)
		os.Exit(1)
	}

	// Wait for turn to complete.
	select {
	case <-done:
	case <-turnDone:
		slog.Info("turn complete")
	case <-time.After(120 * time.Second):
		slog.Info("timeout")
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
