// zc — ZeptoCodeCLI: a BubbleTea TUI for interactive Letta Code conversations.
//
// Milestone 1 scope: streaming chat viewport, input box, statusline
// (loop status / permission mode / runtime ids), and a tool-approval modal.
// Management commands stay with the native `letta` CLI.
//
// Run: pixi run zc -- --agent <agent-id> [--conversation <id>] [--port 8493]
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	entryCommand // slash-command output block
)

type entry struct {
	kind entryKind
	text string
	// tool entries
	toolName   string
	toolCallID string
	toolArgs   strings.Builder
	toolStatus string // "", "running", "success", "error", "denied", "allowed"
	toolReturn string // trimmed tool output, shown when ctrl+o enabled
	// command entries
	cmdInput string
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
	cli       *client.Client
	logPath   string
	port      int
	serverLog *os.File

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

	// milestone 3+ state
	overlay        *overlay
	showReasoning  bool // reasoning collapsed by default
	showToolOutput bool // tool-return previews hidden by default
	queue          []protocol.QueueItem
	subagents      []protocol.SubagentSnapshot
	supportedCmds  []string
	modCmds        []protocol.ModCommandInfo
	serverVersion  string
	versionWarned  bool
	serverCWD      string
	ctrlCArmed     time.Time
	reconnectTried bool
}

// testedLettaCode is the letta-code minor-version prefix this client was
// verified against. There is no wire version negotiation; drift gets a
// visible warning instead of silent breakage.
const testedLettaCode = "0.28."

func newModel(cli *client.Client, logPath string, port int, serverLog *os.File) *model {
	ta := textarea.New()
	ta.Placeholder = "Message the agent · enter sends · / commands · ctrl+k palette · ctrl+p conversations"
	ta.Prompt = "┃ "
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.Focus()
	return &model{
		cli:           cli,
		logPath:       logPath,
		port:          port,
		serverLog:     serverLog,
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

	case tea.MouseMsg:
		// Wheel scrolls the transcript; everything else is ignored.
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

	case frameMsg:
		m.handleFrame(msg.frame)
		m.refreshViewport()
		return m, waitForFrame(m.cli.Frames)

	case conversationsMsg:
		if msg.err != nil {
			m.appendEntry(&entry{kind: entryError, text: "conversation list failed: " + msg.err.Error()})
			m.refreshViewport()
			return m, nil
		}
		m.overlay = &overlay{kind: overlayConversations, title: "conversations", items: msg.items}
		m.layout()
		return m, nil

	case switchedMsg:
		if msg.err != nil {
			m.appendEntry(&entry{kind: entryError, text: "switch failed: " + msg.err.Error()})
			m.refreshViewport()
			return m, nil
		}
		m.entries = nil
		m.toolByCallID = map[string]*entry{}
		m.approvals = nil
		m.seenApprovals = map[string]bool{}
		m.queue = nil
		m.subagents = nil
		m.closeStreaming()
		if len(msg.history) > 0 {
			m.replayHistory(msg.history)
		}
		m.appendEntry(&entry{kind: entryInfo, text: "switched to " + msg.conversationID})
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, nil

	case disconnectedMsg:
		m.connected = false
		if !m.reconnectTried {
			m.reconnectTried = true
			m.appendEntry(&entry{kind: entryError, text: "connection to app-server lost — reconnecting…"})
			m.refreshViewport()
			return m, m.reconnect()
		}
		m.appendEntry(&entry{kind: entryError, text: "connection to app-server lost (press ctrl+c to exit)"})
		m.refreshViewport()
		return m, nil

	case reconnectedMsg:
		if msg.err != nil {
			m.appendEntry(&entry{kind: entryError, text: "reconnect failed: " + msg.err.Error()})
			m.refreshViewport()
			return m, nil
		}
		m.cli = msg.cli
		m.connected = true
		m.reconnectTried = false
		m.appendEntry(&entry{kind: entryInfo, text: "reconnected (app-server respawned)"})
		m.refreshViewport()
		return m, waitForFrame(m.cli.Frames)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.autosizeInput()
	return m, cmd
}

type reconnectedMsg struct {
	cli *client.Client
	err error
}

// reconnect respawns the app-server and resumes the same conversation.
func (m *model) reconnect() tea.Cmd {
	mode := m.mode
	switch mode {
	case protocol.ModeStandard, protocol.ModeAcceptEdits, protocol.ModeUnrestricted:
	default:
		mode = "" // unknown placeholder — let the server default
	}
	opts := client.Options{
		Agent:          m.cli.Runtime.AgentID,
		ConversationID: m.cli.Runtime.ConversationID,
		Port:           m.port,
		Mode:           mode,
		ServerLog:      m.serverLog,
	}
	old := m.cli
	return func() tea.Msg {
		old.Close()
		time.Sleep(1 * time.Second)
		cli, err := client.Start(context.Background(), opts)
		return reconnectedMsg{cli: cli, err: err}
	}
}

// autosizeInput grows the textarea with its content (3–8 rows).
func (m *model) autosizeInput() {
	lines := strings.Count(m.input.Value(), "\n") + 1
	h := lines + 1
	if h < 3 {
		h = 3
	}
	if h > 8 {
		h = 8
	}
	if h != m.input.Height() {
		m.input.SetHeight(h)
		m.layout()
	}
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Overlays swallow keys first (user-invoked, so they take precedence).
	if m.overlay != nil {
		return m.handleOverlayKey(msg)
	}

	// Approval modal next.
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
		// Double-press to quit while a turn is active; instant when idle.
		if m.turnActive() && time.Since(m.ctrlCArmed) > 2*time.Second {
			m.ctrlCArmed = time.Now()
			m.appendEntry(&entry{kind: entryInfo, text: "turn is active — press ctrl+c again to quit"})
			m.refreshViewport()
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case "esc":
		// Priority: clear non-empty input, else abort the active turn.
		if strings.TrimSpace(m.input.Value()) != "" {
			m.input.Reset()
			m.historyIdx = len(m.history)
			return m, nil
		}
		if m.turnActive() {
			_ = m.cli.Abort()
			m.appendEntry(&entry{kind: entryInfo, text: "⏹ abort requested"})
			m.refreshViewport()
		}
		return m, nil
	case "ctrl+p":
		return m, m.openConversationPicker()
	case "ctrl+k":
		m.openCommandPalette()
		return m, nil
	case "ctrl+j":
		m.overlay = &overlay{kind: overlayJobs, title: "ZeptoCode jobs + broker", items: jobsOverlayItems()}
		m.layout()
		return m, nil
	case "ctrl+r":
		m.showReasoning = !m.showReasoning
		m.refreshViewport()
		return m, nil
	case "ctrl+o":
		m.showToolOutput = !m.showToolOutput
		m.refreshViewport()
		return m, nil
	case "shift+tab":
		m.cycleMode()
		return m, nil
	case "tab":
		if m.tryComplete() {
			return m, nil
		}
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		m.input.Reset()
		m.history = append(m.history, text)
		m.historyIdx = len(m.history)
		if strings.HasPrefix(text, "/") {
			m.dispatchSlashCommand(text)
			return m, nil
		}
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
	case "pgup", "pgdown", "ctrl+u", "ctrl+d", "home", "end":
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// ── overlays, palette, completion, mode ──

type conversationsMsg struct {
	items []overlayItem
	err   error
}

func (m *model) openConversationPicker() tea.Cmd {
	cli := m.cli
	return func() tea.Msg {
		convs, err := cli.ConversationsList(context.Background())
		if err != nil {
			return conversationsMsg{err: err}
		}
		items := []overlayItem{{id: "", title: "(new conversation)"}}
		for _, c := range convs {
			title := c.Title
			if title == "" {
				title = c.Summary
			}
			if title == "" {
				title = c.ID
			}
			when := c.LastMessageAt
			if when == "" {
				when = c.UpdatedAt
			}
			items = append(items, overlayItem{id: c.ID, title: title, desc: c.ID + "  " + when})
		}
		return conversationsMsg{items: items}
	}
}

// knownCommands merges server built-ins and mod commands for the palette
// and completion.
func (m *model) knownCommands() []overlayItem {
	var items []overlayItem
	for _, c := range m.modCmds {
		desc := c.Description
		if c.Args != "" {
			desc = c.Args + " — " + desc
		}
		items = append(items, overlayItem{id: c.ID, title: "/" + c.ID, desc: desc})
	}
	for _, c := range m.supportedCmds {
		items = append(items, overlayItem{id: c, title: "/" + c, desc: "built-in"})
	}
	return items
}

func (m *model) openCommandPalette() {
	m.overlay = &overlay{kind: overlayCommands, title: "slash commands", items: m.knownCommands()}
	m.layout()
}

func (m *model) handleOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	o := m.overlay
	switch msg.String() {
	case "esc", "ctrl+c":
		m.overlay = nil
		m.layout()
		return m, nil
	case "up", "ctrl+p":
		o.sel--
		o.clampSel()
		return m, nil
	case "down", "ctrl+n":
		o.sel++
		o.clampSel()
		return m, nil
	case "backspace":
		if len(o.filter) > 0 {
			o.filter = o.filter[:len(o.filter)-1]
			o.clampSel()
		}
		return m, nil
	case "enter":
		items := o.filtered()
		if len(items) == 0 {
			return m, nil
		}
		it := items[o.sel]
		kind := o.kind
		m.overlay = nil
		m.layout()
		switch kind {
		case overlayConversations:
			return m, m.switchConversation(it.id)
		case overlayCommands:
			m.input.SetValue("/" + it.id + " ")
			m.input.CursorEnd()
			m.input.Focus()
		case overlayJobs, overlayAgents:
			// informational; nothing to launch
		}
		return m, nil
	}
	if msg.Type == tea.KeyRunes {
		o.filter += string(msg.Runes)
		o.clampSel()
	}
	return m, nil
}

type switchedMsg struct {
	conversationID string
	history        []protocol.Delta
	err            error
}

func (m *model) switchConversation(conversationID string) tea.Cmd {
	cli := m.cli
	return func() tea.Msg {
		if err := cli.SwitchConversation(context.Background(), conversationID); err != nil {
			return switchedMsg{err: err}
		}
		var hist []protocol.Delta
		if conversationID != "" {
			hist, _ = cli.MessagesList(context.Background())
		}
		return switchedMsg{conversationID: cli.Runtime.ConversationID, history: hist}
	}
}

func (m *model) cycleMode() {
	order := []protocol.PermissionMode{protocol.ModeStandard, protocol.ModeAcceptEdits, protocol.ModeUnrestricted}
	next := order[0]
	for i, mo := range order {
		if mo == m.mode {
			next = order[(i+1)%len(order)]
			break
		}
	}
	if err := m.cli.ChangeMode(next); err != nil {
		m.appendEntry(&entry{kind: entryError, text: "mode change failed: " + err.Error()})
		m.refreshViewport()
	}
	// statusline updates when update_device_status confirms
}

// tryComplete implements tab completion: "/" prefixes complete against the
// command palette, "@" tokens complete against the filesystem (server cwd).
func (m *model) tryComplete() bool {
	val := m.input.Value()
	if strings.HasPrefix(val, "/") && !strings.Contains(val, " ") {
		prefix := strings.ToLower(strings.TrimPrefix(val, "/"))
		var matches []string
		for _, c := range m.knownCommands() {
			if strings.HasPrefix(strings.ToLower(c.id), prefix) {
				matches = append(matches, c.id)
			}
		}
		if len(matches) == 1 {
			m.input.SetValue("/" + matches[0] + " ")
			m.input.CursorEnd()
			return true
		}
		if len(matches) > 1 {
			m.appendEntry(&entry{kind: entryInfo, text: "matches: /" + strings.Join(matches, "  /")})
			m.refreshViewport()
			return true
		}
		return false
	}
	// @-file completion on the last whitespace-separated token.
	fields := strings.Split(val, " ")
	last := fields[len(fields)-1]
	if !strings.HasPrefix(last, "@") {
		return false
	}
	frag := strings.TrimPrefix(last, "@")
	base := m.serverCWD
	if base == "" {
		base, _ = os.Getwd()
	}
	dir, partial := filepath.Split(frag)
	list, err := os.ReadDir(filepath.Join(base, dir))
	if err != nil {
		return false
	}
	var matches []string
	for _, de := range list {
		if strings.HasPrefix(de.Name(), partial) {
			name := de.Name()
			if de.IsDir() {
				name += "/"
			}
			matches = append(matches, name)
		}
	}
	if len(matches) == 1 {
		fields[len(fields)-1] = "@" + dir + matches[0]
		m.input.SetValue(strings.Join(fields, " "))
		m.input.CursorEnd()
		return true
	}
	if len(matches) > 1 {
		show := matches
		if len(show) > 12 {
			show = show[:12]
		}
		m.appendEntry(&entry{kind: entryInfo, text: "files: " + strings.Join(show, "  ")})
		m.refreshViewport()
		return true
	}
	return false
}

func (m *model) dispatchSlashCommand(text string) {
	name := strings.TrimPrefix(strings.Fields(text)[0], "/")
	args := strings.TrimSpace(strings.TrimPrefix(text, "/"+name))
	known := false
	for _, c := range m.knownCommands() {
		if c.id == name {
			known = true
			break
		}
	}
	if !known {
		m.appendEntry(&entry{kind: entryError, text: "unknown command: /" + name + " (ctrl+k lists commands)"})
		m.refreshViewport()
		return
	}
	m.closeStreaming()
	m.appendEntry(&entry{kind: entryUser, text: text})
	if err := m.cli.ExecuteCommand(name, args); err != nil {
		m.appendEntry(&entry{kind: entryError, text: "command failed: " + err.Error()})
	}
	m.refreshViewport()
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
		if ds.CWD != "" {
			m.serverCWD = ds.CWD
		}
		if len(ds.SupportedCommands) > 0 {
			m.supportedCmds = ds.SupportedCommands
		}
		if len(ds.ModCommands) > 0 {
			m.modCmds = ds.ModCommands
		}
		if ds.LettaCodeVersion != "" {
			m.serverVersion = ds.LettaCodeVersion
			if !m.versionWarned && !strings.HasPrefix(ds.LettaCodeVersion, testedLettaCode) {
				m.versionWarned = true
				m.appendEntry(&entry{kind: entryInfo, text: fmt.Sprintf(
					"⚠ letta-code %s differs from tested %sx — protocol drift possible (no wire versioning)",
					ds.LettaCodeVersion, testedLettaCode)})
			}
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

	case f.QueueUpdate != nil:
		m.queue = f.QueueUpdate.Queue

	case f.SubagentUpdate != nil:
		m.subagents = f.SubagentUpdate.Subagents

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
			if ret := strings.TrimSpace(d.ToolReturnText()); ret != "" {
				e.toolReturn = truncateLines(ret, 6, 400)
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

	case "command_start", "slash_command_start":
		m.closeStreaming()
		m.appendEntry(&entry{kind: entryInfo, text: "⌁ " + d.Input + " running…"})

	case "command_end", "slash_command_end":
		m.closeStreaming()
		e := &entry{kind: entryCommand, cmdInput: d.Input, text: strings.TrimRight(d.Output, "\n")}
		if d.Success != nil && !*d.Success {
			e.kind = entryError
			e.text = d.Input + " failed:\n" + e.text
		}
		m.appendEntry(e)

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

// extraHeight is everything between the viewport and the input: overlay or
// approval modal, plus queue/subagent activity lines.
func (m *model) extraHeight() int {
	h := 0
	if m.overlay != nil {
		h += lipgloss.Height(m.overlay.render(m.width, 14))
	} else if len(m.approvals) > 0 {
		h += lipgloss.Height(m.renderModal())
	}
	if act := m.activityLines(); act != "" {
		h += lipgloss.Height(act)
	}
	return h
}

func (m *model) layout() {
	statusH := 1
	inputH := m.input.Height() + 1
	extraH := m.extraHeight()
	m.lastModalH = extraH
	vpH := m.height - statusH - inputH - extraH
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
	// Overlay/modal/activity heights change with state; keep geometry honest.
	if h := m.extraHeight(); h != m.lastModalH {
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
			if m.showReasoning {
				b.WriteString(wrap.Render(styleReasoning.Render("· " + e.text)))
			} else {
				b.WriteString(wrap.Render(styleReasoning.Render(fmt.Sprintf(
					"· reasoning (%d chars — ctrl+r expands)", len(e.text)))))
			}
		case entryTool:
			line := m.renderToolLine(e)
			if m.showToolOutput && e.toolReturn != "" {
				line += "\n" + styleInfo.Render(indentLines(e.toolReturn, "  │ "))
			}
			b.WriteString(wrap.Render(line))
		case entryCommand:
			b.WriteString(wrap.Render(styleInfo.Render("⌁ "+e.cmdInput) + "\n" + e.text))
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
	if n := len(m.queue); n > 0 {
		left += fmt.Sprintf(" · q:%d", n)
	}
	line := fmt.Sprintf("%s │ %s │ ^k cmds ^p convs ^j jobs", left, status)
	return style.Width(m.width).Render(line)
}

// activityLines renders queue + subagent status between transcript and input.
func (m *model) activityLines() string {
	var lines []string
	if n := len(m.queue); n > 0 {
		first := compactOneLine(m.queue[0].ContentText(), m.width-24)
		lines = append(lines, styleInfo.Render(fmt.Sprintf("⧗ %d queued: %s", n, first)))
	}
	shown := 0
	for i := range m.subagents {
		s := &m.subagents[i]
		if s.Status != "running" && s.Status != "pending" {
			continue
		}
		if shown >= 3 {
			lines = append(lines, styleInfo.Render("⚙ …more subagents running"))
			break
		}
		lines = append(lines, styleInfo.Render(fmt.Sprintf("⚙ %s: %s (%s, %dk tok)",
			s.SubagentType, compactOneLine(s.Description, m.width-40), s.Status, s.TotalTokens/1000)))
		shown++
	}
	return strings.Join(lines, "\n")
}

func (m *model) View() string {
	if m.quitting {
		return ""
	}
	if !m.ready {
		return "connecting…"
	}
	parts := []string{m.viewport.View()}
	if m.overlay != nil {
		parts = append(parts, m.overlay.render(m.width, 14))
	} else if len(m.approvals) > 0 {
		parts = append(parts, m.renderModal())
	}
	if act := m.activityLines(); act != "" {
		parts = append(parts, act)
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

func truncateLines(s string, maxLines, maxChars int) string {
	if len(s) > maxChars {
		s = s[:maxChars] + "…"
	}
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], "…")
	}
	return strings.Join(lines, "\n")
}

func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

// ── main ──

// pickAgentInteractive lists agents on stdout and starts the chosen runtime.
func pickAgentInteractive(cli *client.Client) error {
	agents, err := cli.ListAgents(context.Background())
	if err != nil {
		return err
	}
	if len(agents) == 0 {
		return fmt.Errorf("no agents on this server")
	}
	fmt.Println("agents:")
	for i, a := range agents {
		fmt.Printf("  [%d] %s  (%s)\n", i+1, a.Name, a.ID)
	}
	fmt.Print("select agent: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(agents) {
		return fmt.Errorf("invalid selection %q", strings.TrimSpace(line))
	}
	fmt.Printf("starting runtime for %s…\n", agents[n-1].Name)
	return cli.StartRuntime(context.Background(), agents[n-1].ID)
}

func main() {
	agentID := flag.String("agent", "", "agent id or name (required)")
	conversationID := flag.String("conversation", "", "conversation id to resume (default: create new)")
	port := flag.Int("port", 8493, "loopback port for the spawned app-server")
	mode := flag.String("mode", "", "permission mode: standard | acceptEdits | unrestricted (default: server default)")
	flag.Parse()

	logFile, logPath, err := client.OpenServerLog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open server log: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	opts := client.Options{
		Agent:          *agentID,
		ConversationID: *conversationID,
		Port:           *port,
		Mode:           protocol.PermissionMode(*mode),
		ServerLog:      logFile,
	}

	var cli *client.Client
	if *agentID == "" {
		// No agent given: connect first, offer a picker on stdout.
		fmt.Println("starting app-server…")
		cli, err = client.Connect(context.Background(), opts)
		if err == nil {
			err = pickAgentInteractive(cli)
		}
	} else {
		fmt.Printf("starting app-server and runtime for %s…\n", *agentID)
		cli, err = client.Start(context.Background(), opts)
	}
	if err != nil {
		if cli != nil {
			cli.Close()
		}
		fmt.Fprintf(os.Stderr, "startup failed: %v\n(server log: %s)\n", err, logPath)
		os.Exit(1)
	}
	defer cli.Close()

	m := newModel(cli, logPath, *port, logFile)
	if *conversationID != "" {
		if msgs, err := cli.MessagesList(context.Background()); err == nil {
			m.replayHistory(msgs)
		} else {
			m.appendEntry(&entry{kind: entryError, text: "history fetch failed: " + err.Error()})
		}
	}

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tui error: %v\n", err)
		os.Exit(1)
	}
}
