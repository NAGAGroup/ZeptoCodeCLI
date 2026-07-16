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
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/huh"
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
	// render cache: every kind caches its final rendered block, keyed on
	// width + content + display state (see model.renderEntry)
	cachedRender string
	cachedKey    string
}

// ── tea messages ──

type framesMsg struct{ frames []protocol.Frame }
type disconnectedMsg struct{}

// waitForFrame blocks for one frame, then drains everything already queued:
// heavy streams (subagent bursts) collapse into one render per batch instead
// of one render per frame — this is what un-freezes the UI under load.
func waitForFrame(frames <-chan protocol.Frame) tea.Cmd {
	return func() tea.Msg {
		f, ok := <-frames
		if !ok {
			return disconnectedMsg{}
		}
		batch := []protocol.Frame{f}
		for len(batch) < 512 {
			select {
			case f, ok := <-frames:
				if !ok {
					return framesMsg{frames: batch}
				}
				batch = append(batch, f)
			default:
				return framesMsg{frames: batch}
			}
		}
		return framesMsg{frames: batch}
	}
}

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
	completion     completionState
	question       *questionForm // AskUserQuestion takes over the modal slot
	helpModel      help.Model
	showHelp       bool
	spin           spinner.Model
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
	spinning       bool

	// startup state machine (phase 4): the TUI owns connection + agent pick
	phase      int // phaseConnecting | phasePicking | phaseChat
	startupErr string
	startOpts  client.Options
}

const (
	phaseConnecting = iota
	phasePicking
	phaseChat
)

// testedLettaCode is the letta-code minor-version prefix this client was
// verified against. There is no wire version negotiation; drift gets a
// visible warning instead of silent breakage.
const testedLettaCode = "0.28."

func newModel(cli *client.Client, logPath string, port int, serverLog *os.File) *model {
	ta := textarea.New()
	ta.Placeholder = "Message the agent · / for commands · @ for files · ctrl+g for help"
	ta.Prompt = ""
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.Focus()
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(theme.Accent)
	return &model{
		cli:           cli,
		logPath:       logPath,
		port:          port,
		serverLog:     serverLog,
		helpModel:     newHelp(),
		spin:          sp,
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
	return tea.Batch(m.connectCmd(), m.spin.Tick, textarea.Blink)
}

type connectDoneMsg struct {
	cli    *client.Client
	agents []overlayItem // non-nil ⇒ user must pick
	err    error
}

type runtimeReadyMsg struct {
	history []protocol.Delta
	err     error
}

// connectCmd spawns the app-server and either starts the runtime directly
// (--agent given) or fetches the agent list for the in-TUI picker.
func (m *model) connectCmd() tea.Cmd {
	opts := m.startOpts
	return func() tea.Msg {
		cli, err := client.Connect(context.Background(), opts)
		if err != nil {
			return connectDoneMsg{err: err}
		}
		if opts.Agent == "" {
			agents, err := cli.ListAgents(context.Background())
			if err != nil {
				cli.Close()
				return connectDoneMsg{err: err}
			}
			items := make([]overlayItem, 0, len(agents))
			for _, a := range agents {
				items = append(items, overlayItem{id: a.ID, title: a.Name, desc: a.ID})
			}
			return connectDoneMsg{cli: cli, agents: items}
		}
		return connectDoneMsg{cli: cli}
	}
}

// startRuntimeCmd starts the runtime for agent and fetches history if resuming.
func (m *model) startRuntimeCmd(agent string) tea.Cmd {
	cli := m.cli
	conversation := m.startOpts.ConversationID
	return func() tea.Msg {
		if err := cli.StartRuntime(context.Background(), agent); err != nil {
			return runtimeReadyMsg{err: err}
		}
		var hist []protocol.Delta
		if conversation != "" {
			hist, _ = cli.MessagesList(context.Background())
		}
		return runtimeReadyMsg{history: hist}
	}
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

	case connectDoneMsg:
		if msg.err != nil {
			m.startupErr = msg.err.Error()
			return m, nil
		}
		m.cli = msg.cli
		if msg.agents != nil {
			m.phase = phasePicking
			m.overlay = &overlay{kind: overlayAgents, title: "choose an agent", items: msg.agents}
			return m, nil
		}
		return m, m.startRuntimeCmd(m.startOpts.Agent)

	case runtimeReadyMsg:
		if msg.err != nil {
			m.startupErr = msg.err.Error()
			return m, nil
		}
		m.phase = phaseChat
		m.overlay = nil
		if len(msg.history) > 0 {
			m.replayHistory(msg.history)
		}
		m.layout()
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, waitForFrame(m.cli.Frames)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		if m.turnActive() || m.question != nil || m.phase != phaseChat {
			return m, cmd
		}
		m.spinning = false
		return m, nil // stop ticking when idle

	case tea.MouseMsg:
		// Wheel scrolls the transcript; everything else is ignored.
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

	case framesMsg:
		for i := range msg.frames {
			m.handleFrame(msg.frames[i])
		}
		m.refreshViewport()
		cmds := []tea.Cmd{waitForFrame(m.cli.Frames)}
		if m.question != nil && !m.question.inited {
			m.question.inited = true
			cmds = append(cmds, m.question.form.Init())
		}
		if m.turnActive() && !m.spinning {
			m.spinning = true
			cmds = append(cmds, m.spin.Tick)
		}
		return m, tea.Batch(cmds...)

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

	// huh's internal messages (from Init/its own cmds) must reach the form.
	if m.question != nil {
		form, cmd := m.question.form.Update(msg)
		if f, ok := form.(*huh.Form); ok {
			m.question.form = f
		}
		m.finishQuestionIfDone()
		return m, cmd
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.autosizeInput()
	m.refreshCompletions()
	return m, cmd
}

// finishQuestionIfDone submits the answers once the huh form reports
// completion — which can happen on huh's own async messages, not only on
// the keypress that triggered it.
func (m *model) finishQuestionIfDone() {
	if m.question == nil || m.question.form.State != huh.StateCompleted {
		return
	}
	qf := m.question
	m.question = nil
	m.layout()
	if err := m.cli.RespondApproval(qf.req.RequestID, qf.decision()); err != nil {
		m.appendEntry(&entry{kind: entryError, text: "answer send failed: " + err.Error()})
	} else {
		var parts []string
		for q, a := range qf.answers() {
			parts = append(parts, compactOneLine(q, 40)+" → "+a)
		}
		m.appendEntry(&entry{kind: entryInfo, text: "answered: " + strings.Join(parts, " · ")})
	}
	m.refreshViewport()
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
	// Startup phases: only the agent picker (and quitting) are interactive.
	if m.phase != phaseChat {
		if msg.String() == "ctrl+c" || (msg.String() == "esc" && m.overlay == nil) {
			m.quitting = true
			return m, tea.Quit
		}
		if m.overlay != nil {
			return m.handleOverlayKey(msg)
		}
		return m, nil
	}

	// Question form takes the whole modal slot while active.
	if m.question != nil {
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		if msg.String() == "esc" {
			req := m.question.req
			m.question = nil
			m.layout()
			if err := m.cli.RespondApproval(req.RequestID, protocol.ApprovalDecision{
				Behavior: "deny",
				Message:  "User dismissed the question form.",
			}); err != nil {
				m.appendEntry(&entry{kind: entryError, text: "deny send failed: " + err.Error()})
			}
			m.refreshViewport()
			return m, nil
		}
		form, cmd := m.question.form.Update(msg)
		if f, ok := form.(*huh.Form); ok {
			m.question.form = f
		}
		m.finishQuestionIfDone()
		return m, cmd
	}

	// Overlays swallow keys next (user-invoked, so they take precedence).
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
	case "ctrl+g":
		m.showHelp = !m.showHelp
		m.layout()
		return m, nil
	case "tab", "right":
		if m.completion.visible {
			m.acceptCompletion()
			return m, nil
		}
		if msg.String() == "tab" {
			return m, nil // swallow raw tabs; textarea tabs are never wanted
		}
	case "enter":
		// An incomplete /command with the dropdown open: enter completes.
		if m.completion.visible && strings.HasPrefix(m.input.Value(), "/") &&
			!strings.Contains(m.input.Value(), " ") {
			m.acceptCompletion()
			return m, nil
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		m.input.Reset()
		m.completion = completionState{}
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
		if m.completion.visible {
			if m.completion.sel > 0 {
				m.completion.sel--
			}
			return m, nil
		}
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
		if m.completion.visible {
			if m.completion.sel < len(m.completion.items)-1 {
				m.completion.sel++
			}
			return m, nil
		}
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
			m.refreshCompletions()
		case overlayAgents:
			// startup picker: launch the chosen runtime
			return m, m.startRuntimeCmd(it.id)
		case overlayJobs:
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
	// Carry the CURRENT permission mode into the target conversation —
	// otherwise new conversations silently reset to the server default
	// (unrestricted), which reads as "permissions broken".
	mode := m.mode
	return func() tea.Msg {
		switch mode {
		case protocol.ModeStandard, protocol.ModeAcceptEdits, protocol.ModeUnrestricted:
			cli.SetMode(mode)
		}
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
		return
	}
	// Optimistic update: rapid repeated shift+tab must compute each step
	// from the requested mode, not the last server-confirmed one (the
	// device_status confirmation races the next keypress).
	m.mode = next
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
		// Subagent token streams never enter the transcript (matches the
		// native client): their status arrives via update_subagent_state
		// and renders as activity rollups.
		if f.StreamDelta.SubagentID != "" {
			return
		}
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
	// AskUserQuestion gets a real form, not an allow/deny modal.
	if isQuestionRequest(req) {
		if qf := newQuestionForm(req); qf != nil {
			m.question = qf
			m.layout()
			return
		}
	}
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

// extraHeight is everything between the viewport and the statusline other
// than the input itself: modal slot, activity lines, completion dropdown,
// help view, and the input border (2 rows).
func (m *model) extraHeight() int {
	h := 2 // input border
	if m.question != nil {
		h += lipgloss.Height(m.renderQuestion())
	} else if m.overlay != nil {
		h += lipgloss.Height(m.overlay.render(m.width, 14))
	} else if len(m.approvals) > 0 {
		h += lipgloss.Height(m.renderModal())
	}
	if act := m.activityLines(); act != "" {
		h += lipgloss.Height(act)
	}
	if m.completion.visible {
		h += lipgloss.Height(m.renderCompletions())
	}
	if m.showHelp {
		h += lipgloss.Height(m.helpModel.FullHelpView(keys.FullHelp()))
	}
	return h
}

func (m *model) renderQuestion() string {
	w := m.width - 4
	if w < 20 {
		w = 20
	}
	title := styleAccent.Render("agent asks") + styleInfo.Render("  (esc dismisses = deny)")
	return styleModal.BorderForeground(theme.Accent).Width(w).
		Render(title + "\n\n" + m.question.form.View())
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

// renderEntry renders one entry with caching: the cache key covers width,
// content length, and every display toggle that affects output — so a full
// transcript render is a string join of cached blocks, and only entries
// whose content or state changed pay rendering cost.
func (m *model) renderEntry(e *entry, w int) string {
	key := fmt.Sprintf("%d|%d|%d|%d|%s|%v|%v",
		w, len(e.text), e.toolArgs.Len(), len(e.toolReturn),
		e.toolStatus, m.showReasoning, m.showToolOutput)
	if e.cachedKey == key && e.cachedRender != "" {
		return e.cachedRender
	}
	wrap := lipgloss.NewStyle().Width(w)
	var out string
	switch e.kind {
	case entryUser:
		out = wrap.Render(rail(theme.User) + styleUser.Render("you ▸ ") + e.text)
	case entryAssistant:
		md := ""
		if r := m.markdownRenderer(w - 2); r != nil {
			if rendered, err := r.Render(e.text); err == nil {
				md = strings.Trim(rendered, "\n")
			}
		}
		if md == "" {
			md = e.text
		}
		out = rail(theme.Agent) + styleAgent.Render("agent ▸") + "\n" + md
	case entryReasoning:
		if m.showReasoning {
			out = wrap.Render(styleReasoning.Render("· " + e.text))
		} else {
			out = wrap.Render(styleReasoning.Render(fmt.Sprintf(
				"· reasoning (%d chars — ctrl+r expands)", len(e.text))))
		}
	case entryTool:
		line := m.renderToolLine(e)
		if m.showToolOutput && e.toolReturn != "" {
			line += "\n" + styleInfo.Render(indentLines(e.toolReturn, "  │ "))
		}
		out = wrap.Render(line)
	case entryCommand:
		out = wrap.Render(styleAccent.Render("⌁ "+e.cmdInput) + "\n" + e.text)
	case entryInfo:
		out = wrap.Render(styleInfo.Render(e.text))
	case entryError:
		out = wrap.Render(styleError.Render(e.text))
	}
	e.cachedRender = out
	e.cachedKey = key
	return out
}

func (m *model) renderTranscript() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	var b strings.Builder
	for _, e := range m.entries {
		b.WriteString(m.renderEntry(e, w))
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

// verbStatus humanizes loop states.
func verbStatus(s string) string {
	switch s {
	case protocol.LoopSendingAPIRequest, protocol.LoopWaitingForResponse:
		return "thinking"
	case protocol.LoopRetryingAPIRequest:
		return "retrying"
	case protocol.LoopProcessingResponse:
		return "responding"
	case protocol.LoopExecutingClientTool, "EXECUTING_COMMAND":
		return "running tool"
	case protocol.LoopWaitingOnApproval:
		return "waiting on you"
	case protocol.LoopWaitingOnInput:
		return "ready"
	default:
		return strings.ToLower(strings.ReplaceAll(s, "_", " "))
	}
}

func (m *model) statusline() string {
	status := verbStatus(m.loopStatus)
	if m.turnActive() {
		status = m.spin.View() + " " + status
	}
	if !m.connected {
		status = styleError.Render("disconnected")
	}
	mode := styleStatusMode.Foreground(modeColor(m.mode)).Render(string(m.mode))
	left := fmt.Sprintf(" %s · %s · %s · %s",
		shortID(m.cli.Runtime.AgentID), m.cli.Runtime.ConversationID, mode, status)
	if n := len(m.queue); n > 0 {
		left += styleAccent.Render(fmt.Sprintf(" · ⧗%d", n))
	}
	right := styleInfo.Render("^k cmds · ^p convs · ^j jobs · ^g help ")
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
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
	if m.phase != phaseChat {
		var body string
		switch {
		case m.startupErr != "":
			body = styleError.Render("startup failed: "+m.startupErr) +
				styleInfo.Render("\n(server log: "+m.logPath+")\n\npress ctrl+c to exit")
		case m.overlay != nil:
			body = m.overlay.render(max(m.width, 40), 16)
		default:
			body = m.spin.View() + " " + styleInfo.Render("starting app-server…")
		}
		if m.width > 0 && m.height > 0 {
			return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body)
		}
		return body
	}
	if !m.ready {
		return m.spin.View() + " connecting…"
	}
	parts := []string{m.viewport.View()}
	if m.question != nil {
		parts = append(parts, m.renderQuestion())
	} else if m.overlay != nil {
		parts = append(parts, m.overlay.render(m.width, 14))
	} else if len(m.approvals) > 0 {
		parts = append(parts, m.renderModal())
	}
	if act := m.activityLines(); act != "" {
		parts = append(parts, act)
	}
	// The input border wears the permission mode: blue standard, yellow
	// acceptEdits, red unrestricted — legible without reading the statusline.
	inputBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(modeColor(m.mode)).
		Width(m.width - 2).
		Render(m.input.View())
	parts = append(parts, inputBox)
	if m.completion.visible {
		parts = append(parts, m.renderCompletions())
	}
	if m.showHelp {
		parts = append(parts, m.helpModel.FullHelpView(keys.FullHelp()))
	}
	parts = append(parts, m.statusline())
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

	// Connection, agent pick, and runtime start all happen inside the TUI
	// (phase state machine) — no stdout preamble.
	m := newModel(nil, logPath, *port, logFile)
	m.startOpts = client.Options{
		Agent:          *agentID,
		ConversationID: *conversationID,
		Port:           *port,
		Mode:           protocol.PermissionMode(*mode),
		ServerLog:      logFile,
	}

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tui error: %v\n", err)
		os.Exit(1)
	}
	if m.cli != nil {
		m.cli.Close()
	}
}
