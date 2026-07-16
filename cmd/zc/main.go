// zc — ZeptoCodeCLI: a BubbleTea TUI for interactive Letta Code conversations.
//
// Milestone 1 scope: streaming chat viewport, input box, statusline
// (loop status / permission mode / runtime ids), and a tool-approval modal.
// Management commands stay with the native `letta` CLI.
//
// Run: pixi run zc -- --agent <agent-id> [--conversation <id>] [--port 8493]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/client"
	"github.com/NAGAGroup/ZeptoCodeCLI/internal/protocol"
)

// ── entry model ──

type entryKind int

const (
	entryUser entryKind = iota
	entryAssistant
	entryReasoning
	entryTool
	entryInfo
	entryError
)

type entry struct {
	kind entryKind
	text string
	// tool entries
	toolName   string
	toolCallID string
	toolArgs   strings.Builder
	toolStatus string // "", "running", "success", "error", "denied", "allowed"
}

// ── tea messages ──

type frameMsg struct{ frame protocol.Frame }
type disconnectedMsg struct{}

func waitForFrame(frames <-chan protocol.Frame) tea.Cmd {
	return func() tea.Msg {
		f, ok := <-frames
		if !ok {
			return disconnectedMsg{}
		}
		return frameMsg{frame: f}
	}
}

// ── styles ──

var (
	styleUser       = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	styleAgent      = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	styleReasoning  = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	styleTool       = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleToolOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleToolErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleInfo       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleError      = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	styleStatus     = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("6")).Padding(0, 1)
	styleStatusBusy = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("3")).Padding(0, 1)
	styleModal      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("11")).Padding(0, 1)
)

// ── model ──

type model struct {
	cli     *client.Client
	logPath string

	viewport viewport.Model
	input    textarea.Model
	ready    bool
	width    int
	height   int

	entries       []*entry
	openAssistant *entry // streaming aggregation target (run-scoped, not id-scoped)
	openReasoning *entry
	toolByCallID  map[string]*entry

	approvals []*protocol.ControlRequest // FIFO; head is the visible modal

	loopStatus string
	mode       protocol.PermissionMode
	connected  bool
	quitting   bool
	lastModalH int
}

func newModel(cli *client.Client, logPath string) *model {
	ta := textarea.New()
	ta.Placeholder = "Message the agent (enter to send, alt+enter for newline, esc aborts turn)"
	ta.Prompt = "┃ "
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.Focus()
	return &model{
		cli:          cli,
		logPath:      logPath,
		input:        ta,
		toolByCallID: map[string]*entry{},
		loopStatus:   protocol.LoopWaitingOnInput,
		mode:         "?", // real mode arrives via update_device_status
		connected:    true,
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(waitForFrame(m.cli.Frames), textarea.Blink)
}

// ── update ──

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case frameMsg:
		m.handleFrame(msg.frame)
		m.refreshViewport()
		return m, waitForFrame(m.cli.Frames)

	case disconnectedMsg:
		m.connected = false
		m.appendEntry(&entry{kind: entryError, text: "connection to app-server lost"})
		m.refreshViewport()
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Approval modal swallows keys first.
	if len(m.approvals) > 0 {
		req := m.approvals[0]
		switch msg.String() {
		case "a", "y":
			m.resolveApproval(req, protocol.ApprovalDecision{Behavior: "allow"})
			return m, nil
		case "d", "n":
			m.resolveApproval(req, protocol.ApprovalDecision{
				Behavior: "deny",
				Message:  "Denied by user in ZeptoCodeCLI.",
			})
			return m, nil
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	}

	switch msg.String() {
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "esc":
		if m.turnActive() {
			_ = m.cli.Abort()
			m.appendEntry(&entry{kind: entryInfo, text: "⏹ abort requested"})
			m.refreshViewport()
		}
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		m.input.Reset()
		m.closeStreaming()
		m.appendEntry(&entry{kind: entryUser, text: text})
		if err := m.cli.SendUserMessage(text); err != nil {
			m.appendEntry(&entry{kind: entryError, text: "send failed: " + err.Error()})
		}
		m.refreshViewport()
		return m, nil
	case "alt+enter":
		m.input.InsertString("\n")
		return m, nil
	case "pgup", "pgdown", "up", "down":
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) resolveApproval(req *protocol.ControlRequest, decision protocol.ApprovalDecision) {
	m.approvals = m.approvals[1:]
	if err := m.cli.RespondApproval(req.RequestID, decision); err != nil {
		m.appendEntry(&entry{kind: entryError, text: "approval send failed: " + err.Error()})
	}
	verdict := "allowed"
	if decision.Behavior == "deny" {
		verdict = "denied"
	}
	if e, ok := m.toolByCallID[req.Request.ToolCallID]; ok {
		e.toolStatus = verdict
	} else {
		m.appendEntry(&entry{kind: entryInfo, text: fmt.Sprintf("%s %s", verdict, req.Request.ToolName)})
	}
	m.refreshViewport()
}

// ── frame handling ──

func (m *model) handleFrame(f protocol.Frame) {
	switch {
	case f.ControlRequest != nil && f.ControlRequest.Request.Subtype == "can_use_tool":
		m.approvals = append(m.approvals, f.ControlRequest)

	case f.LoopStatus != nil:
		m.loopStatus = f.LoopStatus.LoopStatus.Status

	case f.DeviceStatus != nil:
		if f.DeviceStatus.DeviceStatus.Mode != "" {
			m.mode = f.DeviceStatus.DeviceStatus.Mode
		}

	case f.StreamDelta != nil:
		m.handleDelta(&f.StreamDelta.Delta)
	}
}

func (m *model) handleDelta(d *protocol.Delta) {
	switch d.MessageType {
	case "assistant_message":
		if m.openAssistant == nil {
			m.openAssistant = &entry{kind: entryAssistant}
			m.appendEntry(m.openAssistant)
		}
		m.openAssistant.text += d.Text()
		m.openReasoning = nil

	case "reasoning_message", "hidden_reasoning_message":
		if m.openReasoning == nil {
			m.openReasoning = &entry{kind: entryReasoning}
			m.appendEntry(m.openReasoning)
		}
		m.openReasoning.text += d.Reasoning
		m.openAssistant = nil

	case "tool_call_message":
		m.closeStreaming()
		if d.ToolCall == nil {
			return
		}
		id := d.ToolCall.ToolCallID
		e, ok := m.toolByCallID[id]
		if !ok && id != "" {
			e = &entry{kind: entryTool, toolCallID: id, toolStatus: "running"}
			m.toolByCallID[id] = e
			m.appendEntry(e)
		}
		if e == nil {
			return
		}
		if d.ToolCall.Name != "" {
			e.toolName = d.ToolCall.Name
		}
		e.toolArgs.WriteString(d.ToolCall.Arguments)

	case "tool_return_message":
		if e, ok := m.toolByCallID[d.ToolCallID]; ok {
			if e.toolStatus != "denied" {
				e.toolStatus = d.Status
			}
		}

	case "client_tool_start":
		m.closeStreaming()
		if _, ok := m.toolByCallID[d.ToolCallID]; !ok {
			e := &entry{kind: entryTool, toolCallID: d.ToolCallID, toolName: d.ToolName, toolStatus: "running"}
			e.toolArgs.WriteString(d.ToolArgs)
			m.toolByCallID[d.ToolCallID] = e
			m.appendEntry(e)
		}

	case "client_tool_end":
		if e, ok := m.toolByCallID[d.ToolCallID]; ok && e.toolStatus != "denied" {
			e.toolStatus = d.Status
		}

	case "status":
		m.appendEntry(&entry{kind: entryInfo, text: d.Message})

	case "retry":
		m.appendEntry(&entry{kind: entryInfo, text: "retrying: " + d.Message})

	case "loop_error":
		m.appendEntry(&entry{kind: entryError, text: d.Message})

	case "stop_reason":
		m.closeStreaming()
	}
}

func (m *model) closeStreaming() {
	m.openAssistant = nil
	m.openReasoning = nil
}

func (m *model) turnActive() bool {
	return m.loopStatus != protocol.LoopWaitingOnInput && m.loopStatus != ""
}

// ── view ──

func (m *model) appendEntry(e *entry) {
	m.entries = append(m.entries, e)
}

func (m *model) layout() {
	statusH := 1
	inputH := m.input.Height() + 1
	modalH := 0
	if len(m.approvals) > 0 {
		modalH = lipgloss.Height(m.renderModal())
	}
	vpH := m.height - statusH - inputH - modalH
	if vpH < 3 {
		vpH = 3
	}
	if !m.ready {
		m.viewport = viewport.New(m.width, vpH)
		m.ready = true
	} else {
		m.viewport.Width = m.width
		m.viewport.Height = vpH
	}
	m.input.SetWidth(m.width - 2)
	m.refreshViewport()
}

func (m *model) refreshViewport() {
	if !m.ready {
		return
	}
	atBottom := m.viewport.AtBottom()
	m.viewport.SetContent(m.renderTranscript())
	if atBottom {
		m.viewport.GotoBottom()
	}
	// Modal height can change with queue state; keep geometry honest.
	m.layoutIfModalChanged()
}

func (m *model) layoutIfModalChanged() {
	h := 0
	if len(m.approvals) > 0 {
		h = lipgloss.Height(m.renderModal())
	}
	if h != m.lastModalH {
		m.lastModalH = h
		statusH := 1
		inputH := m.input.Height() + 1
		vpH := m.height - statusH - inputH - h
		if vpH < 3 {
			vpH = 3
		}
		m.viewport.Height = vpH
		m.viewport.GotoBottom()
	}
}

func (m *model) renderTranscript() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	wrap := lipgloss.NewStyle().Width(w)
	var b strings.Builder
	for _, e := range m.entries {
		switch e.kind {
		case entryUser:
			b.WriteString(wrap.Render(styleUser.Render("you ▸ ") + e.text))
		case entryAssistant:
			b.WriteString(wrap.Render(styleAgent.Render("agent ▸ ") + e.text))
		case entryReasoning:
			b.WriteString(wrap.Render(styleReasoning.Render("· " + e.text)))
		case entryTool:
			b.WriteString(wrap.Render(m.renderToolLine(e)))
		case entryInfo:
			b.WriteString(wrap.Render(styleInfo.Render(e.text)))
		case entryError:
			b.WriteString(wrap.Render(styleError.Render(e.text)))
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

func (m *model) renderToolLine(e *entry) string {
	args := compactOneLine(e.toolArgs.String(), 70)
	label := fmt.Sprintf("⚒ %s %s", e.toolName, args)
	switch e.toolStatus {
	case "success", "allowed":
		return styleToolOK.Render(label + " ✓")
	case "error", "denied":
		return styleToolErr.Render(label + " ✗ " + e.toolStatus)
	default:
		return styleTool.Render(label + " …")
	}
}

func (m *model) renderModal() string {
	req := m.approvals[0]
	input, _ := json.MarshalIndent(req.Request.Input, "", "  ")
	inputStr := string(input)
	if lines := strings.Split(inputStr, "\n"); len(lines) > 12 {
		inputStr = strings.Join(lines[:12], "\n") + "\n…"
	}
	queued := ""
	if n := len(m.approvals) - 1; n > 0 {
		queued = fmt.Sprintf("  (+%d queued)", n)
	}
	body := fmt.Sprintf("%s wants to run %s%s\n\n%s\n\n[a]llow   [d]eny",
		"agent", styleTool.Render(req.Request.ToolName), queued, inputStr)
	w := m.width - 4
	if w < 20 {
		w = 20
	}
	return styleModal.Width(w).Render(body)
}

func (m *model) statusline() string {
	style := styleStatus
	status := m.loopStatus
	if m.turnActive() {
		style = styleStatusBusy
	}
	if !m.connected {
		status = "DISCONNECTED"
		style = styleStatusBusy
	}
	left := fmt.Sprintf("%s · %s · %s",
		shortID(m.cli.Runtime.AgentID), m.cli.Runtime.ConversationID, m.mode)
	line := fmt.Sprintf("%s │ %s", left, status)
	return style.Width(m.width).Render(line)
}

func (m *model) View() string {
	if m.quitting {
		return ""
	}
	if !m.ready {
		return "connecting…"
	}
	parts := []string{m.viewport.View()}
	if len(m.approvals) > 0 {
		parts = append(parts, m.renderModal())
	}
	parts = append(parts, m.input.View(), m.statusline())
	return strings.Join(parts, "\n")
}

// ── helpers ──

func shortID(id string) string {
	if len(id) > 20 {
		return id[:20] + "…"
	}
	return id
}

func compactOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// ── main ──

func main() {
	agentID := flag.String("agent", "", "agent id or name (required)")
	conversationID := flag.String("conversation", "", "conversation id to resume (default: create new)")
	port := flag.Int("port", 8493, "loopback port for the spawned app-server")
	mode := flag.String("mode", "", "permission mode: standard | acceptEdits | unrestricted (default: server default)")
	flag.Parse()

	if *agentID == "" {
		fmt.Fprintln(os.Stderr, "usage: zc --agent <agent-id-or-name> [--conversation <id>]")
		os.Exit(2)
	}

	logFile, logPath, err := client.OpenServerLog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open server log: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	fmt.Printf("starting app-server and runtime for %s…\n", *agentID)
	cli, err := client.Start(context.Background(), client.Options{
		Agent:          *agentID,
		ConversationID: *conversationID,
		Port:           *port,
		Mode:           protocol.PermissionMode(*mode),
		ServerLog:      logFile,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "startup failed: %v\n(server log: %s)\n", err, logPath)
		os.Exit(1)
	}
	defer cli.Close()

	p := tea.NewProgram(newModel(cli, logPath), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tui error: %v\n", err)
		os.Exit(1)
	}
}
