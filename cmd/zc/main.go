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
	"github.com/charmbracelet/glamour"
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
	// render cache (assistant markdown is expensive)
	cachedRender string
	cachedWidth  int
	cachedLen    int
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

	markdown      *glamour.TermRenderer
	markdownWidth int

	history    []string // sent messages, for up/down recall
	historyIdx int      // len(history) == "not navigating"

	seenApprovals map[string]bool // control_request request_ids already queued
}

func newModel(cli *client.Client, logPath string) *model {
	ta := textarea.New()
	ta.Placeholder = "Message the agent (enter to send, alt+enter for newline, esc aborts turn)"
	ta.Prompt = "┃ "
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.Focus()
	return &model{
		cli:           cli,
		logPath:       logPath,
		input:         ta,
		toolByCallID:  map[string]*entry{},
		loopStatus:    protocol.LoopWaitingOnInput,
		mode:          "?", // real mode arrives via update_device_status
		connected:     true,
		seenApprovals: map[string]bool{},
	}
}

// replayHistory renders past conversation messages into transcript entries.
// Unlike live deltas, each history message is complete, so streaming
// aggregation is closed after every message.
func (m *model) replayHistory(msgs []protocol.Delta) {
	// The messages API returns newest-first; normalize to chronological
	// using the date field rather than trusting the default order.
	if len(msgs) > 1 && msgs[0].Date > msgs[len(msgs)-1].Date {
		for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
			msgs[i], msgs[j] = msgs[j], msgs[i]
		}
	}
	for i := range msgs {
		d := &msgs[i]
		switch d.MessageType {
		case "user_message":
			text := strings.TrimSpace(d.Text())
			if text == "" || strings.HasPrefix(text, "<system-reminder>") {
				continue // heartbeats / injected reminders, not the user
			}
			m.appendEntry(&entry{kind: entryUser, text: text})
		case "assistant_message":
			if text := d.Text(); strings.TrimSpace(text) != "" {
				m.appendEntry(&entry{kind: entryAssistant, text: text})
			}
		case "tool_call_message", "tool_return_message", "client_tool_start",
			"client_tool_end", "approval_request_message":
			m.handleDelta(d)
			m.closeStreaming()
		}
		// reasoning, system, usage, stop_reason: skipped on replay
	}
	if len(m.entries) > 0 {
		m.appendEntry(&entry{kind: entryInfo, text: "── conversation resumed ──"})
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
		key := msg.String()
		switch key {
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
		// Number keys: allow + persist the matching permission suggestion.
		if n := int(key[0] - '0'); len(key) == 1 && n >= 1 && n <= len(req.Request.PermissionSuggestions) {
			s := req.Request.PermissionSuggestions[n-1]
			m.resolveApproval(req, protocol.ApprovalDecision{
				Behavior:                        "allow",
				SelectedPermissionSuggestionIDs: []string{s.ID},
			})
			return m, nil
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
		m.history = append(m.history, text)
		m.historyIdx = len(m.history)
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
	case "up":
		// History recall when the input is empty or already navigating.
		if len(m.history) > 0 && (m.input.Value() == "" || m.historyIdx < len(m.history)) {
			if m.historyIdx > 0 {
				m.historyIdx--
			}
			m.input.SetValue(m.history[m.historyIdx])
			m.input.CursorEnd()
			return m, nil
		}
	case "down":
		if m.historyIdx < len(m.history) {
			m.historyIdx++
			if m.historyIdx == len(m.history) {
				m.input.Reset()
			} else {
				m.input.SetValue(m.history[m.historyIdx])
				m.input.CursorEnd()
			}
			return m, nil
		}
	case "pgup", "pgdown":
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
	// enqueueApproval guarantees the tool entry exists.
	if e, ok := m.toolByCallID[req.Request.ToolCallID]; ok {
		e.toolStatus = verdict
	}
	m.refreshViewport()
}

// ── frame handling ──

func (m *model) handleFrame(f protocol.Frame) {
	switch {
	case f.ControlRequest != nil && f.ControlRequest.Request.Subtype == "can_use_tool":
		m.enqueueApproval(f.ControlRequest)

	case f.LoopStatus != nil:
		m.loopStatus = f.LoopStatus.LoopStatus.Status

	case f.DeviceStatus != nil:
		ds := &f.DeviceStatus.DeviceStatus
		if ds.Mode != "" {
			m.mode = ds.Mode
		}
		// Approval recovery: a resumed conversation may have approvals that
		// were pending when the previous client went away.
		for i := range ds.PendingControlRequests {
			p := &ds.PendingControlRequests[i]
			m.enqueueApproval(&protocol.ControlRequest{
				RequestID: p.RequestID,
				Request:   p.Request,
			})
		}

	case f.StreamDelta != nil:
		m.handleDelta(&f.StreamDelta.Delta)
	}
}

// enqueueApproval queues a can_use_tool request (idempotent by request_id)
// and materializes its tool entry so approval state renders in the
// transcript, not as a detached info line.
func (m *model) enqueueApproval(req *protocol.ControlRequest) {
	if req.RequestID == "" || m.seenApprovals[req.RequestID] {
		return
	}
	m.seenApprovals[req.RequestID] = true
	m.approvals = append(m.approvals, req)
	id := req.Request.ToolCallID
	if _, ok := m.toolByCallID[id]; !ok && id != "" {
		m.closeStreaming()
		e := &entry{kind: entryTool, toolCallID: id, toolName: req.Request.ToolName, toolStatus: "awaiting approval"}
		if args, err := json.Marshal(req.Request.Input); err == nil {
			e.toolArgs.Write(args)
		}
		m.toolByCallID[id] = e
		m.appendEntry(e)
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

	case "approval_request_message":
		// A complete tool call awaiting approval (streams live before the
		// control_request; also how approval-gated calls appear in history).
		m.closeStreaming()
		if d.ToolCall == nil {
			return
		}
		id := d.ToolCall.ToolCallID
		e, ok := m.toolByCallID[id]
		if !ok && id != "" {
			e = &entry{kind: entryTool, toolCallID: id, toolStatus: "awaiting approval"}
			m.toolByCallID[id] = e
			m.appendEntry(e)
		}
		if e == nil {
			return
		}
		if d.ToolCall.Name != "" {
			e.toolName = d.ToolCall.Name
		}
		// Arguments here are complete: replace anything streamed so far.
		e.toolArgs.Reset()
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

// markdownRenderer returns a width-matched glamour renderer, rebuilt on
// resize.
func (m *model) markdownRenderer(width int) *glamour.TermRenderer {
	if m.markdown != nil && m.markdownWidth == width {
		return m.markdown
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
		glamour.WithEmoji(),
	)
	if err != nil {
		return nil
	}
	m.markdown = r
	m.markdownWidth = width
	return r
}

// renderAssistant renders markdown with a per-entry cache: only the entry
// whose text grew (the streaming one) pays the glamour cost per frame.
func (m *model) renderAssistant(e *entry, width int) string {
	if e.cachedWidth == width && e.cachedLen == len(e.text) && e.cachedRender != "" {
		return e.cachedRender
	}
	out := ""
	if r := m.markdownRenderer(width); r != nil {
		if rendered, err := r.Render(e.text); err == nil {
			out = strings.Trim(rendered, "\n")
		}
	}
	if out == "" {
		out = e.text
	}
	out = styleAgent.Render("agent ▸") + "\n" + out
	e.cachedRender = out
	e.cachedWidth = width
	e.cachedLen = len(e.text)
	return out
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
			b.WriteString(m.renderAssistant(e, w-2))
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
	case "awaiting approval":
		return styleTool.Render(label + " ⏸ awaiting approval")
	default:
		return styleTool.Render(label + " …")
	}
}

var (
	styleDiffAdd    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleDiffRemove = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleDiffCtx    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

func renderDiffs(diffs []protocol.DiffPreview, maxLines int) string {
	var b strings.Builder
	lines := 0
	for _, d := range diffs {
		b.WriteString(styleInfo.Render("── "+d.FileName+" ──") + "\n")
		if d.Mode != "advanced" {
			b.WriteString(styleDiffCtx.Render("(no preview: "+d.Reason+")") + "\n")
			continue
		}
		for _, h := range d.Hunks {
			for _, l := range h.Lines {
				if lines >= maxLines {
					b.WriteString(styleDiffCtx.Render("…") + "\n")
					return b.String()
				}
				switch l.Type {
				case "add":
					b.WriteString(styleDiffAdd.Render("+ "+l.Content) + "\n")
				case "remove":
					b.WriteString(styleDiffRemove.Render("- "+l.Content) + "\n")
				default:
					b.WriteString(styleDiffCtx.Render("  "+l.Content) + "\n")
				}
				lines++
			}
		}
	}
	return b.String()
}

func (m *model) renderModal() string {
	req := m.approvals[0]

	var detail string
	if len(req.Request.Diffs) > 0 {
		detail = strings.TrimRight(renderDiffs(req.Request.Diffs, 16), "\n")
	} else {
		input, _ := json.MarshalIndent(req.Request.Input, "", "  ")
		detail = string(input)
		if lines := strings.Split(detail, "\n"); len(lines) > 12 {
			detail = strings.Join(lines[:12], "\n") + "\n…"
		}
	}

	queued := ""
	if n := len(m.approvals) - 1; n > 0 {
		queued = fmt.Sprintf("  (+%d queued)", n)
	}

	options := "[a]llow   [d]eny"
	for i, s := range req.Request.PermissionSuggestions {
		options += fmt.Sprintf("   [%d] %s", i+1, s.Text)
	}

	body := fmt.Sprintf("agent wants to run %s%s\n\n%s\n\n%s",
		styleTool.Render(req.Request.ToolName), queued, detail, options)
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

	m := newModel(cli, logPath)
	if *conversationID != "" {
		if msgs, err := cli.MessagesList(context.Background()); err == nil {
			m.replayHistory(msgs)
		} else {
			m.appendEntry(&entry{kind: entryError, text: "history fetch failed: " + err.Error()})
		}
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tui error: %v\n", err)
		os.Exit(1)
	}
}
