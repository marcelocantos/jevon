package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/coder/websocket"
)

func main() {
	addr := flag.String("addr", "localhost:8080", "daisd address")
	sessionName := flag.String("name", "", "session name")
	model := flag.String("model", "", "model override")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	url := fmt.Sprintf("ws://%s/ws/remote", *addr)
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect failed: %v\n", err)
		os.Exit(1)
	}
	defer conn.CloseNow()

	conn.SetReadLimit(1 << 20) // 1 MB

	// Read init message.
	_, data, err := conn.Read(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read init failed: %v\n", err)
		os.Exit(1)
	}

	var init struct {
		Version string `json:"version"`
	}
	json.Unmarshal(data, &init)
	fmt.Fprintf(os.Stderr, "Connected to daisd %s\n", init.Version)

	// Create a session.
	createMsg, _ := json.Marshal(map[string]string{
		"type":  "create_session",
		"name":  *sessionName,
		"model": *model,
	})
	if err := conn.Write(ctx, websocket.MessageText, createMsg); err != nil {
		fmt.Fprintf(os.Stderr, "create session failed: %v\n", err)
		os.Exit(1)
	}

	// Read session_created response.
	_, data, err = conn.Read(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read session_created failed: %v\n", err)
		os.Exit(1)
	}

	var created struct {
		SessionID string `json:"session_id"`
		Name      string `json:"name"`
	}
	json.Unmarshal(data, &created)
	sessionID := created.SessionID
	fmt.Fprintf(os.Stderr, "Session: %s (%s)\n", created.Name, sessionID)

	// commandDone signals when a command completes.
	var commandMu sync.Mutex
	commandDone := make(chan struct{}, 1)
	inCommand := false

	// Handle Ctrl-C: first press cancels current command, second exits.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	go func() {
		for range sigCh {
			commandMu.Lock()
			active := inCommand
			commandMu.Unlock()

			if active {
				msg, _ := json.Marshal(map[string]string{
					"type":       "cancel",
					"session_id": sessionID,
				})
				_ = conn.Write(ctx, websocket.MessageText, msg)
			} else {
				fmt.Fprintln(os.Stderr, "\nBye.")
				conn.Close(websocket.StatusNormalClosure, "user exit")
				os.Exit(0)
			}
		}
	}()

	// Response reader goroutine.
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				if ctx.Err() == nil {
					fmt.Fprintf(os.Stderr, "\nDisconnected: %v\n", err)
				}
				cancel()
				return
			}

			var msg struct {
				Type       string  `json:"type"`
				SessionID  string  `json:"session_id,omitempty"`
				Content    string  `json:"content,omitempty"`
				Name       string  `json:"name,omitempty"`
				Message    string  `json:"message,omitempty"`
				State      string  `json:"state,omitempty"`
				DurationMs float64 `json:"duration_ms,omitempty"`
				CostUSD    float64 `json:"cost_usd,omitempty"`
			}
			json.Unmarshal(data, &msg)

			// Only process events for our session.
			if msg.SessionID != "" && msg.SessionID != sessionID {
				continue
			}

			switch msg.Type {
			case "text":
				fmt.Print(msg.Content)
			case "tool_use":
				fmt.Fprintf(os.Stderr, "\n[tool: %s]\n", msg.Name)
			case "done":
				fmt.Println()
				if msg.DurationMs > 0 || msg.CostUSD > 0 {
					fmt.Fprintf(os.Stderr, "[%.1fs, $%.4f]\n", msg.DurationMs/1000, msg.CostUSD)
				}
				commandMu.Lock()
				inCommand = false
				commandMu.Unlock()
				select {
				case commandDone <- struct{}{}:
				default:
				}
			case "error":
				fmt.Fprintf(os.Stderr, "\nError: %s\n", msg.Message)
				commandMu.Lock()
				inCommand = false
				commandMu.Unlock()
				select {
				case commandDone <- struct{}{}:
				default:
				}
			case "status":
				// Could show a spinner here.
			}
		}
	}()

	// Detect interactive vs piped stdin.
	interactive := isTerminal(os.Stdin)

	// REPL: read stdin lines, send as commands.
	scanner := bufio.NewScanner(os.Stdin)
	if interactive {
		fmt.Print("> ")
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if interactive {
				fmt.Print("> ")
			}
			continue
		}

		commandMu.Lock()
		inCommand = true
		commandMu.Unlock()

		msg, _ := json.Marshal(map[string]string{
			"type":       "command",
			"session_id": sessionID,
			"text":       line,
		})
		if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
			fmt.Fprintf(os.Stderr, "send failed: %v\n", err)
			break
		}

		// Wait for done/error before next prompt.
		select {
		case <-commandDone:
		case <-ctx.Done():
			return
		}
		if interactive {
			fmt.Print("> ")
		}
	}

	// If stdin was piped and a command is still in flight, wait for it.
	if !interactive {
		commandMu.Lock()
		active := inCommand
		commandMu.Unlock()
		if active {
			select {
			case <-commandDone:
			case <-ctx.Done():
			}
		}
	}
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
