// remote is a terminal client for jevond. It connects to Jevon
// and sends text messages, displaying streamed responses with markdown
// rendering. Reconnects automatically with exponential backoff.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/marcelocantos/jevon/internal/cli"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/coder/websocket"
	"github.com/peterh/liner"
)

// Message types for bubbletea.
type (
	connectedMsg    string // server version
	disconnectedMsg struct{ err error }
	textMsg         string // incremental text from Jevon
	statusMsg       string // Jevon status change
	errorMsg        string // error from server
	historyMsg      []historyEntry
	userBroadcastMsg struct {
		Text      string    `json:"text"`
		Timestamp time.Time `json:"timestamp"`
	}
)

type historyEntry struct {
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

// logEntry is a structured conversation entry, stored raw and
// re-rendered on demand (for resize support).
type logEntry struct {
	kind      string    // "user", "jevon", "status", "error"
	text      string    // raw text (markdown for chat, plain for status/error)
	timestamp time.Time
}

var (
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
)

func main() {
	addr := flag.String("addr", "localhost:13705", "jevond address")
	showVersion := flag.Bool("version", false, "print version and exit")
	helpAgent := flag.Bool("help-agent", false, "print agent guide and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("remote", cli.Version)
		os.Exit(0)
	}
	if *helpAgent {
		flag.PrintDefaults()
		fmt.Println()
		fmt.Print(cli.AgentGuide)
		os.Exit(0)
	}

	url := fmt.Sprintf("ws://%s/ws/remote", *addr)

	p := tea.NewProgram(
		newModel(url),
		tea.WithAltScreen(),
	)

	// Handle Ctrl-C outside bubbletea (backup).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	go func() {
		<-sigCh
		p.Quit()
	}()

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Save history on exit.
	saveHistory(nil)
}

// --- History management ---

var history []string

func historyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".jevon")
	os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "remote_history")
}

func loadHistory() {
	hp := historyPath()
	if hp == "" {
		return
	}
	l := liner.NewLiner()
	defer l.Close()
	if f, err := os.Open(hp); err == nil {
		l.ReadHistory(f)
		f.Close()
	}
	// liner doesn't expose history directly, so we store our own.
	if data, err := os.ReadFile(hp); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line != "" {
				history = append(history, line)
			}
		}
	}
}

func saveHistory(extra []string) {
	history = append(history, extra...)
	hp := historyPath()
	if hp == "" {
		return
	}
	// Keep last 1000 lines.
	if len(history) > 1000 {
		history = history[len(history)-1000:]
	}
	os.WriteFile(hp, []byte(strings.Join(history, "\n")+"\n"), 0o644)
}

func addHistory(line string) {
	history = append(history, line)
}

// --- Model ---

type model struct {
	url    string
	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc

	viewport viewport.Model
	input    textarea.Model
	renderer *glamour.TermRenderer

	// Jevon state.
	markdown  string    // accumulated markdown for current turn
	rendered  string    // glamour-rendered output (for streaming display)
	status    string    // "idle", "thinking", "disconnected"
	version   string
	turnStart time.Time // when the current Jevon turn began

	// Conversation log (structured entries, re-rendered on demand).
	entries []logEntry

	// Reconnect state.
	backoff time.Duration

	// History navigation.
	histIdx int // -1 = editing new input
	draft   string

	// Layout.
	width  int
	height int
	ready  bool

	// Unread tracking (when scrolled away from bottom).
	unread    int
	lastCount int // entry count at last scroll-to-bottom
}

func newModel(url string) *model {
	loadHistory()

	ti := textarea.New()
	ti.Placeholder = "Type a message..."
	ti.Focus()
	ti.SetHeight(1)
	ti.ShowLineNumbers = false
	ti.KeyMap.InsertNewline.SetEnabled(false)

	ctx, cancel := context.WithCancel(context.Background())

	return &model{
		url:     url,
		ctx:     ctx,
		cancel:  cancel,
		input:   ti,
		status:  "disconnected",
		backoff: 100 * time.Millisecond,
		histIdx: -1,
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		m.connect(),
	)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.cancel()
			return m, tea.Quit

		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" || m.status != "idle" {
				break
			}
			m.input.Reset()
			m.histIdx = -1
			m.draft = ""
			addHistory(text)

			return m, m.send(text)

		case "ctrl+p":
			// History: previous.
			if len(history) == 0 {
				break
			}
			if m.histIdx == -1 {
				m.draft = m.input.Value()
				m.histIdx = len(history) - 1
			} else if m.histIdx > 0 {
				m.histIdx--
			}
			m.input.SetValue(history[m.histIdx])
			return m, nil

		case "ctrl+n":
			// History: next.
			if m.histIdx == -1 {
				break
			}
			if m.histIdx < len(history)-1 {
				m.histIdx++
				m.input.SetValue(history[m.histIdx])
			} else {
				m.histIdx = -1
				m.input.SetValue(m.draft)
			}
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		inputHeight := 3 // textarea + border
		statusHeight := 1

		if !m.ready {
			m.viewport = viewport.New(msg.Width, msg.Height-inputHeight-statusHeight)
			m.viewport.SetContent("")
			// Keep arrow/pgup/pgdn but drop vi aliases (j/k/d/u/f/b/h/l)
			// that conflict with typing in the textarea.
			m.viewport.KeyMap = viewport.KeyMap{
				PageDown:     key.NewBinding(key.WithKeys("pgdown")),
				PageUp:       key.NewBinding(key.WithKeys("pgup")),
				HalfPageUp:   key.NewBinding(key.WithKeys("ctrl+u")),
				HalfPageDown: key.NewBinding(key.WithKeys("ctrl+d")),
				Up:           key.NewBinding(key.WithKeys("up")),
				Down:         key.NewBinding(key.WithKeys("down")),
				Left:         key.NewBinding(key.WithKeys("left")),
				Right:        key.NewBinding(key.WithKeys("right")),
			}
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = msg.Height - inputHeight - statusHeight
		}

		m.input.SetWidth(msg.Width - 2)

		// Recreate renderer with wrap width for content column.
		r, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle("light"),
			glamour.WithWordWrap(msg.Width-14),
		)
		if err == nil {
			m.renderer = r
		}

		m.updateViewport()
		return m, nil

	case connectedMsg:
		m.version = string(msg)
		m.status = "idle"
		m.backoff = 100 * time.Millisecond
		m.entries = nil
		m.entries = append(m.entries, logEntry{
			kind:      "status",
			text:      fmt.Sprintf("Connected to jevond %s", m.version),
			timestamp: time.Now(),
		})
		m.updateViewport()
		return m, m.readWS()

	case historyMsg:
		for _, entry := range msg {
			m.entries = append(m.entries, logEntry{
				kind:      entry.Role,
				text:      entry.Text,
				timestamp: entry.Timestamp,
			})
		}
		m.updateViewport()
		return m, m.readWS()

	case disconnectedMsg:
		m.conn = nil
		m.status = "disconnected"
		m.entries = append(m.entries, logEntry{
			kind:      "status",
			text:      fmt.Sprintf("Disconnected: %v", msg.err),
			timestamp: time.Now(),
		})
		m.updateViewport()
		return m, m.reconnectAfter()

	case textMsg:
		m.markdown += string(msg)
		m.renderMarkdown()
		m.updateViewport()
		return m, m.readWS()

	case statusMsg:
		m.status = string(msg)
		if m.status == "idle" && m.markdown != "" {
			// Turn complete — store raw markdown as a log entry.
			m.entries = append(m.entries, logEntry{
				kind:      "jevon",
				text:      m.markdown,
				timestamp: m.turnStart,
			})
			m.markdown = ""
			m.rendered = ""
		}
		m.updateViewport()
		return m, m.readWS()

	case errorMsg:
		m.entries = append(m.entries, logEntry{
			kind: "error",
			text: string(msg),
		})
		m.updateViewport()
		return m, m.readWS()

	case userBroadcastMsg:
		m.entries = append(m.entries, logEntry{
			kind:      "user",
			text:      msg.Text,
			timestamp: msg.Timestamp,
		})
		m.turnStart = msg.Timestamp
		m.markdown = ""
		m.rendered = ""
		m.updateViewport()
		return m, m.readWS()
	}

	// Update textarea.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)

	// Update viewport.
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	if m.viewport.AtBottom() {
		m.unread = 0
		m.lastCount = len(m.entries)
	}

	return m, tea.Batch(cmds...)
}

func (m *model) View() string {
	if !m.ready {
		return "Connecting..."
	}

	status := m.statusLine()
	return m.viewport.View() + "\n" + status + "\n" + m.input.View()
}

var (
	ruleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))
	badgeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("63")).
			Bold(true).
			Padding(0, 1)
	thinkingBadgeStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("230")).
				Background(lipgloss.Color("241")).
				Padding(0, 1)
	disconnectedBadgeStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("230")).
				Background(lipgloss.Color("196")).
				Bold(true).
				Padding(0, 1)
)

func (m *model) statusLine() string {
	if m.width == 0 {
		return ""
	}

	// Determine badge text.
	var badge string
	switch {
	case m.status == "disconnected":
		badge = disconnectedBadgeStyle.Render("Disconnected")
	case m.status == "thinking":
		badge = thinkingBadgeStyle.Render("Thinking…")
	}

	// Scrolled-up indicator (shown alongside or instead of status badge).
	if !m.viewport.AtBottom() {
		scrollBadge := "↓"
		if m.unread > 0 {
			scrollBadge = fmt.Sprintf("↓ %d new", m.unread)
		}
		if badge != "" {
			// Append scroll indicator after status badge.
			badge += " " + badgeStyle.Render(scrollBadge)
		} else {
			badge = badgeStyle.Render(scrollBadge)
		}
	}

	if badge == "" {
		return ruleStyle.Render(strings.Repeat("─", m.width))
	}

	// Center badge on a full-width ─ rule.
	return lipgloss.PlaceHorizontal(m.width, lipgloss.Center, badge,
		lipgloss.WithWhitespaceChars("─"),
		lipgloss.WithWhitespaceForeground(lipgloss.Color("241")),
	)
}

// --- Rendering ---

// newTable creates a lipgloss table with the standard 3-column layout:
// time │ icon │ content.
func (m *model) newTable() *table.Table {
	return table.New().
		Border(lipgloss.NormalBorder()).
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		BorderRow(false).
		BorderHeader(false).
		BorderColumn(true).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("241"))).
		Width(m.width).
		StyleFunc(m.tableStyle)
}

func (m *model) tableStyle(row, col int) lipgloss.Style {
	s := lipgloss.NewStyle().PaddingBottom(1)
	switch col {
	case 0:
		return s.Width(5)
	case 1:
		return s.Width(2)
	default:
		return s
	}
}

// renderLog builds the full conversation log as a table string.
func (m *model) renderLog() string {
	if len(m.entries) == 0 || m.width == 0 {
		return ""
	}

	t := m.newTable()

	for _, entry := range m.entries {
		ts := ""
		if !entry.timestamp.IsZero() {
			ts = statusStyle.Render(entry.timestamp.Format("15:04"))
		}

		switch entry.kind {
		case "user":
			t.Row(ts, "💬", m.renderBold(entry.text))
		case "jevon":
			t.Row(ts, "", m.glamourRender(entry.text))
		case "status":
			t.Row(ts, "", statusStyle.Render(entry.text))
		case "error":
			t.Row(ts, "", errorStyle.Render(entry.text))
		}
	}

	return t.String()
}

// renderStreamingRow renders the in-progress Jevon response as a
// single-row table with the same column layout as the log.
func (m *model) renderStreamingRow() string {
	if m.width == 0 || m.rendered == "" {
		return ""
	}

	ts := ""
	if !m.turnStart.IsZero() {
		ts = statusStyle.Render(m.turnStart.Format("15:04"))
	}

	t := m.newTable()
	t.Row(ts, "", m.rendered)
	return t.String()
}

// renderBold renders text as bold italic via glamour.
func (m *model) renderBold(text string) string {
	if m.renderer == nil {
		return text
	}
	r, err := m.renderer.Render("***" + text + "***")
	if err != nil {
		return text
	}
	return strings.TrimSpace(r)
}

// glamourRender renders markdown via glamour.
func (m *model) glamourRender(text string) string {
	if m.renderer == nil {
		return text
	}
	r, err := m.renderer.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimSpace(r)
}

func (m *model) renderMarkdown() {
	if m.renderer == nil || m.markdown == "" {
		return
	}
	rendered, err := m.renderer.Render(m.markdown)
	if err == nil {
		m.rendered = strings.TrimSpace(rendered)
	}
}

func (m *model) updateViewport() {
	if !m.ready {
		return
	}
	content := m.renderLog()
	if m.rendered != "" {
		content += m.renderStreamingRow()
	}
	atBottom := m.viewport.AtBottom()
	m.viewport.SetContent(content)
	if atBottom {
		m.viewport.GotoBottom()
		m.unread = 0
		m.lastCount = len(m.entries)
	} else {
		m.unread = len(m.entries) - m.lastCount
	}
}

// --- WebSocket commands ---

func (m *model) connect() tea.Cmd {
	return func() tea.Msg {
		conn, _, err := websocket.Dial(m.ctx, m.url, nil)
		if err != nil {
			return disconnectedMsg{err: err}
		}
		conn.SetReadLimit(1 << 20)

		// Read init message.
		_, data, err := conn.Read(m.ctx)
		if err != nil {
			conn.CloseNow()
			return disconnectedMsg{err: err}
		}

		var init struct {
			Version string `json:"version"`
		}
		json.Unmarshal(data, &init)

		m.conn = conn
		return connectedMsg(init.Version)
	}
}

func (m *model) reconnectAfter() tea.Cmd {
	delay := m.backoff
	m.backoff = min(m.backoff*2, 5*time.Second)
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return m.connect()()
	})
}

func (m *model) readWS() tea.Cmd {
	return func() tea.Msg {
		if m.conn == nil {
			return disconnectedMsg{err: fmt.Errorf("no connection")}
		}

		_, data, err := m.conn.Read(m.ctx)
		if err != nil {
			return disconnectedMsg{err: err}
		}

		var msg struct {
			Type    string `json:"type"`
			Content string `json:"content,omitempty"`
			State   string `json:"state,omitempty"`
			Message string `json:"message,omitempty"`
		}
		json.Unmarshal(data, &msg)

		switch msg.Type {
		case "text":
			return textMsg(msg.Content)
		case "status":
			return statusMsg(msg.State)
		case "error":
			return errorMsg(msg.Message)
		case "history":
			var full struct {
				Entries []historyEntry `json:"entries"`
			}
			json.Unmarshal(data, &full)
			return historyMsg(full.Entries)
		case "user_message":
			var um userBroadcastMsg
			json.Unmarshal(data, &um)
			return um
		}

		// Unknown message type — keep reading.
		return m.readWS()()
	}
}

func (m *model) send(text string) tea.Cmd {
	return func() tea.Msg {
		if m.conn == nil {
			return disconnectedMsg{err: fmt.Errorf("not connected")}
		}

		data, _ := json.Marshal(map[string]string{
			"type": "message",
			"text": text,
		})
		if err := m.conn.Write(m.ctx, websocket.MessageText, data); err != nil {
			return disconnectedMsg{err: err}
		}
		return nil
	}
}
