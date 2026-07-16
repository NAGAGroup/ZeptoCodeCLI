// zc — ZeptoCodeCLI: a BubbleTea TUI for interactive Letta Code conversations.
//
// Milestone 1 scope: streaming chat viewport, input box, statusline
// (loop status / permission mode / runtime ids), and a tool-approval modal.
// Management commands stay with the native `letta` CLI.
//
// Run: pixi run zc -- --agent <agent-id> [--conversation <id>] [--port 8493]
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/client"
	"github.com/NAGAGroup/ZeptoCodeCLI/internal/protocol"
	"github.com/NAGAGroup/ZeptoCodeCLI/internal/ui/form"
	"github.com/NAGAGroup/ZeptoCodeCLI/internal/ui/list"
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
	entryDoc     // markdown document view (memory files)
	entryDiff    // unified diff view (memory commit diffs)
)

type entry struct {
	kind entryKind
	text string
	// tool entries
	toolName   string
	toolCallID string
	toolArgs   strings.Builder
	toolStatus string // "", "running", "success", "error", "denied", "allowed"
	toolReturn string // trimmed tool output body (ctrl+o expands)
	// command entries
	cmdInput string
	// streaming state: finished flips when the entry stops streaming (part
	// of the cache key so the final full render replaces split renders).
	finished bool
	stream   *streamMD
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
	cli       *client.StdioClient
	logPath   string
	port      int
	serverLog *os.File

	list   *list.List
	input  textarea.Model
	ready  bool
	width  int
	height int

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
	overlay            *overlay
	completion         completionState
	question           *questionForm // AskUserQuestion takes over the modal slot
	memoryDir          string        // agent MemFS root (from device_status)
	helpModel          help.Model
	showHelp           bool
	spin               spinner.Model
	showReasoning      bool // reasoning collapsed by default
	showToolOutput     bool // tool-return previews hidden by default
	queue              []protocol.QueueItem
	subagents          []protocol.SubagentSnapshot
	supportedCmds      []string
	modCmds            []protocol.ModCommandInfo
	serverVersion      string
	versionWarned      bool
	serverCWD          string
	modelHandle        string // current model handle (agent_retrieve / /model)
	agentName          string // display name for the statusline
	convTitle          string // conversation title for the statusline
	pager              *pager // modal scrollable viewer (diffs, memory files)
	lastUsage          protocol.Delta
	sessionTokens      int64
	ctrlCArmed         time.Time
	reconnectTried     bool
	spinning           bool
	switching          bool // conversation/agent switch in flight (history loading)
	bgProcs            []protocol.BackgroundProcessSummary
	modelID            string // current model catalog id when known (variant cycling)
	reasoningTabCycle  bool   // Tab on empty input cycles reasoning variants
	statuslineTemplate string // custom statusline template (~/.letta/zc.json)

	// startup state machine (phase 4): the TUI owns connection + agent pick
	phase      int // phaseConnecting | phasePicking | phaseChat
	startupErr string
	startOpts  client.StdioOptions
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

func newModel(cli *client.StdioClient, logPath string, port int, serverLog *os.File) *model {
	ta := textarea.New()
	ta.Placeholder = "Message the agent · / for commands · @ for files · ctrl+g for help"
	ta.Prompt = ""
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	// v2 defaults paint the cursor line with a harsh black background —
	// clear it and dim the placeholder to the theme.
	st := ta.Styles()
	st.Focused.CursorLine = lipgloss.NewStyle()
	st.Focused.Placeholder = lipgloss.NewStyle().Foreground(theme.Dim)
	st.Blurred.Placeholder = lipgloss.NewStyle().Foreground(theme.Dim)
	ta.SetStyles(st)
	ta.Focus()
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = lipgloss.NewStyle().Foreground(theme.Accent)
	mm := &model{
		cli:           cli,
		logPath:       logPath,
		port:          port,
		serverLog:     serverLog,
		helpModel:     newHelp(),
		spin:          sp,
		input:         ta,
		list:          list.New(),
		toolByCallID:  map[string]*entry{},
		loopStatus:    protocol.LoopWaitingOnInput,
		mode:          "?", // real mode arrives via update_device_status
		connected:     true,
		seenApprovals: map[string]bool{},

		reasoningTabCycle: readReasoningTabSetting(),
	}
	if tpl, ok := readZcConfig()["statusline"].(string); ok {
		mm.statuslineTemplate = tpl
	}
	return mm
}

// readZcConfig reads ~/.letta/zc.json for zc-local config (statusline template).
func readZcConfig() map[string]any {
	home, err := os.UserHomeDir()
	if err != nil {
		return map[string]any{}
	}
	raw, err := os.ReadFile(filepath.Join(home, ".letta", "zc.json"))
	if err != nil {
		return map[string]any{}
	}
	var cfg map[string]any
	_ = json.Unmarshal(raw, &cfg)
	if cfg == nil {
		cfg = map[string]any{}
	}
	return cfg
}

// readReasoningTabSetting reads reasoningTabCycleEnabled from settings.json.
func readReasoningTabSetting() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(home, ".letta", "settings.json"))
	if err != nil {
		return false
	}
	var s struct {
		ReasoningTabCycleEnabled bool `json:"reasoningTabCycleEnabled"`
	}
	_ = json.Unmarshal(raw, &s)
	return s.ReasoningTabCycleEnabled
}

// ── list adapter: entries as lazily rendered list items ──

// entryItem adapts an entry to list.Item. Rendering goes through the
// model's renderEntry (markdown renderer, display toggles); the cache key
// is the same invalidation key renderEntry uses.
type entryItem struct {
	e *entry
	m *model
}

func (it entryItem) Render(w int) string { return it.m.renderEntry(it.e, w) }
func (it entryItem) CacheKey() string    { return it.m.entryCacheKey(it.e) }

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
	cli    *client.StdioClient
	agents []overlayItem // non-nil ⇒ user must pick
	err    error
}

type runtimeReadyMsg struct {
	history []protocol.Delta
	resumed bool // an existing conversation was requested
	histErr error
	err     error
}

// connectCmd spawns the ui-server and either connects directly (--agent
// given as an agent ID) or fetches the agent list for the in-TUI picker.
func (m *model) connectCmd() tea.Cmd {
	opts := m.startOpts
	return func() tea.Msg {
		if opts.Agent == "" {
			// No agent: list agents via headless letta for the picker.
			agents, err := listAgentsHeadless()
			if err != nil {
				return connectDoneMsg{err: err}
			}
			return connectDoneMsg{agents: agentOverlayItems(agents)}
		}
		// Agent given: connect directly (hello handshake starts the runtime).
		cli, err := client.ConnectStdio(context.Background(), opts)
		if err != nil {
			return connectDoneMsg{err: err}
		}
		return connectDoneMsg{cli: cli}
	}
}

// startupConvPicker lists the picked agent's conversations so startup can
// resume one directly (or begin fresh).
func (m *model) startupConvPicker(agentID string) tea.Cmd {
	return func() tea.Msg {
		// Connect with the agent to get a StdioClient for listing conversations.
		cli, err := client.ConnectStdio(context.Background(), client.StdioOptions{
			Agent:        agentID,
			UIBin:        m.startOpts.UIBin,
			UIServerPath: m.startOpts.UIServerPath,
			CWD:          m.startOpts.CWD,
			ServerLog:    m.startOpts.ServerLog,
		})
		if err != nil {
			return connectDoneMsg{err: err}
		}
		convs, err := cli.ListConversations(context.Background(), agentID)
		if err != nil {
			cli.Close()
			return connectDoneMsg{err: err}
		}
		items := []overlayItem{{id: "", title: "(new conversation)"}}
		for _, c := range convs {
			if c.Archived {
				continue
			}
			title := c.Title
			if title == "" {
				title = c.Summary
			}
			if title == "" {
				title = c.ID
			}
			when := c.LastMessageAt
			marker := ""
			if when == "" {
				when, marker = c.UpdatedAt, "  (empty)"
			}
			items = append(items, overlayItem{id: c.ID, title: title, desc: c.ID + "  " + when + marker})
		}
		// Close this temporary connection; we'll reconnect with the chosen conversation.
		cli.Close()
		ov := &overlay{kind: overlayMgmt, title: "choose a conversation", items: items}
		ov.onSelect = func(m *model, it overlayItem) tea.Cmd {
			m.startOpts.ConversationID = it.id
			return m.beginSwitch(m.startRuntimeCmd(agentID))
		}
		return mgmtMsg{overlay: ov}
	}
}

// startRuntimeCmd connects to the ui-server with the chosen agent/conversation
// and fetches history if resuming.
func (m *model) startRuntimeCmd(agent string) tea.Cmd {
	conversation := m.startOpts.ConversationID
	opts := m.startOpts
	return func() tea.Msg {
		cli, err := client.ConnectStdio(context.Background(), opts)
		if err != nil {
			return runtimeReadyMsg{err: err}
		}
		m.cli = cli
		var hist []protocol.Delta
		var histErr error
		if conversation != "" {
			hist, histErr = cli.ListMessages(context.Background())
		}
		return runtimeReadyMsg{history: hist, resumed: conversation != "", histErr: histErr}
	}
}

// mgmtMsg carries an overlay from a background command (e.g. startup picker).
type mgmtMsg struct {
	overlay *overlay
	text    string
}

// pagerRequest requests opening the pager modal with given title + content.
type pagerRequest struct {
	title   string
	content string
	kind    string // "markdown" or "diff"
}

// listAgentsHeadless shells out to `letta agents list --json` to get agents
// without spawning a ui-server (needed for the agent picker when no --agent
// is given).
func listAgentsHeadless() ([]protocol.AgentSummary, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "letta", "agents", "list", "--json")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		// Fallback: try to list agents from the local backend directory
		return listAgentsFromBackend()
	}
	var agents []protocol.AgentSummary
	if err := json.Unmarshal(stdout.Bytes(), &agents); err != nil {
		return listAgentsFromBackend()
	}
	return agents, nil
}

// listAgentsFromBackend reads agent records from the local backend directory.
func listAgentsFromBackend() ([]protocol.AgentSummary, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	agentsDir := filepath.Join(home, ".letta", "lc-local-backend", "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil, err
	}
	var agents []protocol.AgentSummary
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(agentsDir, entry.Name()))
		if err != nil {
			continue
		}
		var agent protocol.AgentSummary
		if err := json.Unmarshal(raw, &agent); err == nil && agent.ID != "" {
			agents = append(agents, agent)
		}
	}
	return agents, nil
}

// ── update ──

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.KeyReleaseMsg:
		// Terminals with the Kitty keyboard protocol report releases too.
		// Never route them anywhere: a release reaching the generic input
		// path used to reset completion selection state.
		return m, nil

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
		if m.startOpts.ConversationID == "" {
			// --agent without --conversation: offer the conversation picker
			// instead of eagerly creating a fresh conversation. Every launch
			// used to abandon an empty conversation, and picking those later
			// looked like "history not populating".
			m.phase = phasePicking
			return m, m.startupConvPicker(msg.cli.Runtime.AgentID)
		}
		return m, m.startRuntimeCmd(m.startOpts.Agent)

	case runtimeReadyMsg:
		if msg.err != nil {
			m.startupErr = msg.err.Error()
			return m, nil
		}
		m.phase = phaseChat
		m.overlay = nil
		m.switching = false
		if len(msg.history) > 0 {
			m.replayHistory(msg.history)
		} else if msg.resumed {
			// Distinguish "nothing to show" from "fetch silently failed" —
			// a blank transcript on resume reads as a replay bug.
			if msg.histErr != nil {
				m.appendEntry(&entry{kind: entryError, text: "history fetch failed: " + msg.histErr.Error()})
			} else {
				m.appendEntry(&entry{kind: entryInfo, text: "(empty conversation — no messages yet)"})
			}
		}
		m.layout()
		m.list.GotoBottom()
		return m, tea.Batch(waitForFrame(m.cli.Frames), m.loadRuntimeInfo())

	case runtimeInfoMsg:
		if msg.agentName != "" {
			m.agentName = msg.agentName
		}
		if msg.model != "" {
			m.modelHandle = msg.model
		}
		m.convTitle = msg.convTitle
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		if m.turnActive() || m.question != nil || m.phase != phaseChat || m.switching {
			return m, cmd
		}
		m.spinning = false
		return m, nil // stop ticking when idle

	case tea.MouseWheelMsg:
		// Wheel scrolls the transcript; other mouse events are ignored.
		switch msg.Button {
		case tea.MouseWheelUp:
			m.list.ScrollBy(-3)
		case tea.MouseWheelDown:
			m.list.ScrollBy(3)
		}
		return m, nil

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

	case agentsMsg:
		if msg.err != nil {
			m.appendEntry(&entry{kind: entryError, text: "agent list failed: " + msg.err.Error()})
			m.refreshViewport()
			return m, nil
		}
		m.overlay = &overlay{kind: overlayAgents, title: "switch agent", items: msg.items}
		m.layout()
		return m, nil

	case modelsMsg:
		if msg.err != nil {
			m.appendEntry(&entry{kind: entryError, text: "model list failed: " + msg.err.Error()})
			m.refreshViewport()
			return m, nil
		}
		m.overlay = &overlay{kind: overlayModels, title: "switch model", items: msg.items}
		m.layout()
		return m, nil

	case modelSwitchedMsg:
		if msg.err != nil {
			m.appendEntry(&entry{kind: entryError, text: "model switch failed: " + msg.err.Error()})
		} else {
			m.modelHandle = msg.resp.ModelHandle
			m.modelID = msg.resp.ModelID
			m.appendEntry(&entry{kind: entryInfo, text: fmt.Sprintf(
				"model → %s (%s, applied to %s)", msg.resp.ModelID, msg.resp.ModelHandle, msg.resp.AppliedTo)})
		}
		m.refreshViewport()
		return m, nil

	case switchedMsg:
		m.switching = false
		if msg.err != nil {
			m.appendEntry(&entry{kind: entryError, text: "switch failed: " + msg.err.Error()})
			m.refreshViewport()
			return m, nil
		}
		m.resetEntries()
		m.toolByCallID = map[string]*entry{}
		m.approvals = nil
		m.seenApprovals = map[string]bool{}
		m.queue = nil
		m.subagents = nil
		m.closeStreaming()
		if len(msg.history) > 0 {
			m.replayHistory(msg.history)
		} else if msg.resumed {
			// Distinguish "nothing to show" from "fetch silently failed".
			if msg.histErr != nil {
				m.appendEntry(&entry{kind: entryError, text: "history fetch failed: " + msg.histErr.Error()})
			} else {
				m.appendEntry(&entry{kind: entryInfo, text: "(empty conversation — no messages yet)"})
			}
		}
		m.appendEntry(&entry{kind: entryInfo, text: "switched to " + msg.conversationID})
		m.refreshViewport()
		m.list.GotoBottom()
		return m, m.loadRuntimeInfo()

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

	case mgmtMsg:
		if msg.overlay != nil {
			m.overlay = msg.overlay
			m.layout()
		}
		if msg.text != "" {
			m.appendEntry(&entry{kind: entryInfo, text: msg.text})
			m.refreshViewport()
		}
		return m, nil
	}

	// Form-internal messages (cursor blink etc.) must reach the form.
	if m.question != nil {
		cmd := m.question.form.Update(msg)
		m.finishQuestionIfDone()
		return m, cmd
	}

	// Non-key messages (cursor blink etc.) reach the input but must NOT
	// touch completion state — a blink tick resetting the selection was
	// exactly the "picker jumps back to the first entry" bug.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// finishQuestionIfDone submits the answers once the form reports
// completion — which can happen on the form's own async messages, not only
// on the keypress that triggered it.
func (m *model) finishQuestionIfDone() {
	if m.question == nil || m.question.form.State() != form.StateCompleted {
		return
	}
	qf := m.question
	m.question = nil
	m.layout()
	dec := qf.decision()
	if err := m.cli.SendApprovalResponse(qf.req.RequestID, dec.Behavior, dec.UpdatedInput); err != nil {
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

type runtimeInfoMsg struct {
	agentName string
	model     string
	convTitle string
}

// loadRuntimeInfo fetches display metadata for the statusline: agent name +
// model handle (agent_retrieve) and conversation title.
func (m *model) loadRuntimeInfo() tea.Cmd {
	cli := m.cli
	return func() tea.Msg {
		var out runtimeInfoMsg
		if name, model, err := cli.AgentRetrieve(context.Background()); err == nil {
			out.agentName, out.model = name, model
		}
		if convs, err := cli.ListConversations(context.Background(), cli.Runtime.AgentID); err == nil {
			for _, c := range convs {
				if c.ID == cli.Runtime.ConversationID {
					out.convTitle = c.Title
					break
				}
			}
		}
		return out
	}
}

type reconnectedMsg struct {
	cli *client.StdioClient
	err error
}

// reconnect respawns the ui-server and resumes the same conversation.
func (m *model) reconnect() tea.Cmd {
	opts := m.cli.GetOpts()
	old := m.cli
	return func() tea.Msg {
		old.Close()
		time.Sleep(500 * time.Millisecond)
		cli, err := client.ConnectStdio(context.Background(), opts)
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

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
			if err := m.cli.SendApprovalResponse(req.RequestID, "deny", nil); err != nil {
				m.appendEntry(&entry{kind: entryError, text: "deny send failed: " + err.Error()})
			}
			m.refreshViewport()
			return m, nil
		}
		cmd := m.question.form.Update(msg)
		m.finishQuestionIfDone()
		return m, cmd
	}

	// Management forms use the same modal slot; esc cancels harmlessly.
	// Pager modal: scroll or dismiss.
	if m.pager != nil {
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		if !m.pager.handleKey(msg, m.height) {
			m.pager = nil
		}
		return m, nil
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

	// Help dialog closes on esc/q (lowest-priority modal).
	if m.showHelp {
		switch msg.String() {
		case "esc", "q", "ctrl+g":
			m.showHelp = false
			m.layout()
			return m, nil
		}
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
			m.completion = completionState{}
			m.historyIdx = len(m.history)
			return m, nil
		}
		if m.turnActive() {
			_ = m.cli.SendInterrupt()
			m.appendEntry(&entry{kind: entryInfo, text: "⏹ abort requested"})
			m.refreshViewport()
		}
		return m, nil
	case "ctrl+p":
		return m, m.openConversationPicker()
	case "ctrl+a":
		return m, m.openAgentPicker()
	case "ctrl+k":
		m.openCommandPalette()
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
			// Native parity: with /reasoning-tab on, Tab on an empty input
			// cycles the current model's reasoning variants.
			if m.reasoningTabCycle && strings.TrimSpace(m.input.Value()) == "" {
				return m, m.cycleReasoningVariant()
			}
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
			return m, m.dispatchSlashCommand(text)
		}
		m.closeStreaming()
		m.appendEntry(&entry{kind: entryUser, text: text})
		if err := m.cli.SendMessage(text); err != nil {
			m.appendEntry(&entry{kind: entryError, text: "send failed: " + err.Error()})
		}
		m.refreshViewport()
		return m, nil
	case "ctrl+j", "alt+enter":
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
			m.refreshCompletions()
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
			m.refreshCompletions()
			return m, nil
		}
	case "pgup":
		m.list.PageUp()
		return m, nil
	case "pgdown":
		m.list.PageDown()
		return m, nil
	case "ctrl+u":
		m.list.HalfUp()
		return m, nil
	case "ctrl+d":
		m.list.HalfDown()
		return m, nil
	case "home":
		m.list.GotoTop()
		return m, nil
	case "end":
		m.list.GotoBottom()
		return m, nil
	}

	// Typing path: refresh completions only when the content changed, so
	// pure navigation keys never clobber the selection.
	prev := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.autosizeInput()
	if m.input.Value() != prev {
		m.refreshCompletions()
	}
	return m, cmd
}

// ── overlays, palette, completion, mode ──

type conversationsMsg struct {
	items []overlayItem
	err   error
}

type agentsMsg struct {
	items []overlayItem
	err   error
}

type modelsMsg struct {
	items []overlayItem
	err   error
}

type modelSwitchedMsg struct {
	resp *protocol.UpdateModelResponse
	err  error
}

func (m *model) openModelPicker() tea.Cmd {
	// Server returns models as formatted text; send /model as input_submit
	// and the server will show the catalog as a command_result.
	m.closeStreaming()
	m.appendEntry(&entry{kind: entryUser, text: "/model"})
	m.cli.SendMessage("/model")
	return nil
}

func (m *model) switchModel(modelID, modelHandle string) tea.Cmd {
	cli := m.cli
	return func() tea.Msg {
		err := cli.UpdateModel(context.Background(), modelID, modelHandle)
		return modelSwitchedMsg{err: err}
	}
}

// cycleReasoningVariant is deferred until the server returns structured
// model catalog data (currently returns formatted text only).
func (m *model) cycleReasoningVariant() tea.Cmd {
	m.appendEntry(&entry{kind: entryInfo, text: "reasoning variant cycling requires structured model catalog (not yet available)"})
	return nil
}

func (m *model) openAgentPicker() tea.Cmd {
	cli := m.cli
	return func() tea.Msg {
		agents, err := cli.ListAgents(context.Background())
		if err != nil {
			return agentsMsg{err: err}
		}
		return agentsMsg{items: agentOverlayItems(agents)}
	}
}

// agentOverlayItems builds picker rows with pinned agents (settings.json
// agents[].pinned) floated to the top and starred.
func agentOverlayItems(agents []protocol.AgentSummary) []overlayItem {
	// Pinned agents feature requires settings_read; return empty for now.
	pinned := map[string]bool{}
	var top, rest []overlayItem
	for _, a := range agents {
		it := overlayItem{id: a.ID, title: a.Name, desc: a.ID}
		if pinned[a.ID] {
			it.title = "★ " + it.title
			top = append(top, it)
		} else {
			rest = append(rest, it)
		}
	}
	return append(top, rest...)
}

// switchAgent kills the current ui-server and reconnects with a new agent
// (fresh conversation).
func (m *model) switchAgent(agentID string) tea.Cmd {
	opts := m.cli.GetOpts()
	old := m.cli
	return func() tea.Msg {
		old.Close()
		time.Sleep(300 * time.Millisecond)
		newOpts := opts
		newOpts.Agent = agentID
		newOpts.ConversationID = ""
		cli, err := client.ConnectStdio(context.Background(), newOpts)
		if err != nil {
			return switchedMsg{err: err}
		}
		m.cli = cli
		return switchedMsg{conversationID: cli.Runtime.ConversationID}
	}
}

// ── client-side slash commands ──
//
// The server only executes a small remote allowlist (clear, compact, …) plus
// mod commands; everything else the native CLI implements client-side. These
// are zc's client-side equivalents, mapped onto protocol operations.

type clientCommand struct {
	id   string
	desc string
	run  func(m *model, args string) tea.Cmd
}

var clientCommands = []clientCommand{
	{"agents", "switch to another agent", func(m *model, _ string) tea.Cmd {
		return m.openAgentPicker()
	}},
	{"conversations", "switch conversation", func(m *model, _ string) tea.Cmd {
		return m.openConversationPicker()
	}},
	{"new", "start a fresh conversation", func(m *model, _ string) tea.Cmd {
		return m.beginSwitch(m.switchConversation(""))
	}},
	{"mode", "set permission mode: /mode standard|acceptEdits|unrestricted", func(m *model, args string) tea.Cmd {
		switch protocol.PermissionMode(strings.TrimSpace(args)) {
		case protocol.ModeStandard, protocol.ModeAcceptEdits, protocol.ModeUnrestricted:
			target := protocol.PermissionMode(strings.TrimSpace(args))
			if err := m.cli.ChangeMode(target); err == nil {
				m.mode = target
			}
		default:
			m.cycleMode()
		}
		return nil
	}},
	{"model", "switch model: /model [id-or-handle]", func(m *model, args string) tea.Cmd {
		args = strings.TrimSpace(args)
		if args == "" {
			m.closeStreaming()
			m.appendEntry(&entry{kind: entryUser, text: "/model"})
			m.cli.SendMessage("/model")
			return nil
		}
		modelID := args
		modelHandle := ""
		if strings.Contains(args, "/") {
			modelHandle = args
			modelID = ""
		}
		if err := m.cli.UpdateModel(context.Background(), modelID, modelHandle); err != nil {
			m.appendEntry(&entry{kind: entryError, text: err.Error()})
		} else {
			m.appendEntry(&entry{kind: entryInfo, text: "model switched to " + args})
		}
		return nil
	}},
	{"fork", "fork this conversation and switch to the fork", func(m *model, _ string) tea.Cmd {
		cli := m.cli
		return m.beginSwitch(func() tea.Msg {
			forkID, err := cli.Fork(context.Background())
			if err != nil {
				return switchedMsg{err: err}
			}
			opts := cli.GetOpts()
			cli.Close()
			newCli, connErr := client.ConnectStdio(context.Background(), client.StdioOptions{
				Agent:          opts.Agent,
				ConversationID: forkID,
				UIBin:          opts.UIBin,
				UIServerPath:   opts.UIServerPath,
				CWD:            opts.CWD,
				ServerLog:      opts.ServerLog,
			})
			if connErr != nil {
				return switchedMsg{err: connErr}
			}
			m.cli = newCli
			hist, histErr := newCli.ListMessages(context.Background())
			return switchedMsg{conversationID: forkID, history: hist, resumed: true, histErr: histErr}
		})
	}},
	{"title", "set conversation title: /title <text>", func(m *model, args string) tea.Cmd {
		if strings.TrimSpace(args) == "" {
			m.appendEntry(&entry{kind: entryError, text: "usage: /title <text>"})
			return nil
		}
		if err := m.cli.UpdateConversation(context.Background(), map[string]any{"title": strings.TrimSpace(args)}); err != nil {
			m.appendEntry(&entry{kind: entryError, text: err.Error()})
		} else {
			m.convTitle = strings.TrimSpace(args)
			m.appendEntry(&entry{kind: entryInfo, text: "title set"})
		}
		return nil
	}},
	{"rename", "rename this agent: /rename <name>", func(m *model, args string) tea.Cmd {
		if strings.TrimSpace(args) == "" {
			m.appendEntry(&entry{kind: entryError, text: "usage: /rename <name>"})
			return nil
		}
		if err := m.cli.UpdateAgent(context.Background(), map[string]any{"name": strings.TrimSpace(args)}); err != nil {
			m.appendEntry(&entry{kind: entryError, text: err.Error()})
		} else {
			m.appendEntry(&entry{kind: entryInfo, text: "agent renamed to " + strings.TrimSpace(args)})
		}
		return nil
	}},
	{"cd", "change tool working directory: /cd <path>", func(m *model, args string) tea.Cmd {
		path := strings.TrimSpace(args)
		if path == "" {
			m.appendEntry(&entry{kind: entryInfo, text: "cwd: " + m.serverCWD})
			return nil
		}
		if strings.HasPrefix(path, "~") {
			if home, err := os.UserHomeDir(); err == nil {
				path = home + strings.TrimPrefix(path, "~")
			}
		}
		if !filepath.IsAbs(path) && m.serverCWD != "" {
			path = filepath.Join(m.serverCWD, path)
		}
		if err := m.cli.ChangeCWD(path); err != nil {
			m.appendEntry(&entry{kind: entryError, text: err.Error()})
		}
		return nil
	}},
	{"export", "export transcript to a file: /export [path]", func(m *model, args string) tea.Cmd {
		path := strings.TrimSpace(args)
		if path == "" {
			path = filepath.Join(os.TempDir(), fmt.Sprintf("zc-%s.md", m.cli.Runtime.ConversationID))
		}
		var b strings.Builder
		for _, e := range m.entries {
			switch e.kind {
			case entryUser:
				b.WriteString("## you\n\n" + e.text + "\n\n")
			case entryAssistant:
				b.WriteString("## agent\n\n" + e.text + "\n\n")
			case entryTool:
				b.WriteString("`\u2312 " + e.toolName + " " + compactOneLine(e.toolArgs.String(), 120) + " \u2192 " + e.toolStatus + "`\n\n")
			}
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			m.appendEntry(&entry{kind: entryError, text: "export failed: " + err.Error()})
		} else {
			m.appendEntry(&entry{kind: entryInfo, text: "exported to " + path})
		}
		return nil
	}},
	{"help", "show keybindings and commands", func(m *model, _ string) tea.Cmd {
		m.showHelp = true
		return nil
	}},
	{"quit", "exit zc", func(m *model, _ string) tea.Cmd {
		m.quitting = true
		return tea.Quit
	}},
}

func (m *model) openConversationPicker() tea.Cmd {
	cli := m.cli
	return func() tea.Msg {
		convs, err := cli.ListConversations(context.Background(), cli.Runtime.AgentID)
		if err != nil {
			return conversationsMsg{err: err}
		}
		items := []overlayItem{{id: "", title: "(new conversation)"}}
		for _, c := range convs {
			if c.Archived {
				continue
			}
			title := c.Title
			if title == "" {
				title = c.Summary
			}
			if title == "" {
				title = c.ID
			}
			when := c.LastMessageAt
			marker := ""
			if when == "" {
				// Never had a message — resuming it shows nothing.
				when, marker = c.UpdatedAt, "  (empty)"
			}
			items = append(items, overlayItem{id: c.ID, title: title, desc: c.ID + "  " + when + marker})
		}
		return conversationsMsg{items: items}
	}
}

// knownCommands merges zc client commands, mod commands, and server
// built-ins for the palette and completion.
func (m *model) knownCommands() []overlayItem {
	var items []overlayItem
	seen := map[string]bool{}
	for _, c := range clientCommands {
		items = append(items, overlayItem{id: c.id, title: "/" + c.id, desc: c.desc + " (zc)"})
		seen[c.id] = true
	}
	for _, c := range m.modCmds {
		if seen[c.ID] { // client commands shadow same-named mod commands (e.g. /jobs)
			continue
		}
		desc := c.Description
		if c.Args != "" {
			desc = c.Args + " — " + desc
		}
		items = append(items, overlayItem{id: c.ID, title: "/" + c.ID, desc: desc})
	}
	for _, c := range m.supportedCmds {
		if seen[c] {
			continue
		}
		seen[c] = true
		items = append(items, overlayItem{id: c, title: "/" + c, desc: "built-in"})
	}
	// Skills are server-side; the server routes /<skill-name> through its
	// native command registry. No client-side skill scanning needed.
	return items
}

func (m *model) openCommandPalette() {
	m.overlay = &overlay{kind: overlayCommands, title: "slash commands", items: m.knownCommands()}
	m.layout()
}

func (m *model) handleOverlayKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
			return m, m.beginSwitch(m.switchConversation(it.id))
		case overlayCommands:
			m.input.SetValue("/" + it.id + " ")
			m.input.CursorEnd()
			m.input.Focus()
			m.refreshCompletions()
		case overlayAgents:
			if m.phase != phaseChat {
				// startup: picked an agent — offer its conversations next
				return m, m.startupConvPicker(it.id)
			}
			return m, m.beginSwitch(m.switchAgent(it.id))
		case overlayModels:
			return m, m.switchModel(it.id, "")
		case overlayMgmt:
			if o.onSelect != nil {
				return m, o.onSelect(m, it)
			}
		case overlayJobs:
			// informational; nothing to launch
		}
		return m, nil
	}
	if msg.Text != "" {
		o.filter += msg.Text
		o.clampSel()
	}
	return m, nil
}

type switchedMsg struct {
	conversationID string
	history        []protocol.Delta
	resumed        bool // an existing conversation was requested
	histErr        error
	err            error
}

func (m *model) switchConversation(conversationID string) tea.Cmd {
	opts := m.cli.GetOpts()
	old := m.cli
	return func() tea.Msg {
		old.Close()
		time.Sleep(300 * time.Millisecond)
		newOpts := opts
		newOpts.ConversationID = conversationID
		cli, err := client.ConnectStdio(context.Background(), newOpts)
		if err != nil {
			return switchedMsg{err: err}
		}
		m.cli = cli
		var hist []protocol.Delta
		var histErr error
		if conversationID != "" {
			hist, histErr = cli.ListMessages(context.Background())
		}
		return switchedMsg{conversationID: cli.Runtime.ConversationID, history: hist, resumed: conversationID != "", histErr: histErr}
	}
}

// beginSwitch flags a conversation/agent switch as in flight (statusline
// shows a "loading conversation…" spinner until switchedMsg/runtimeReadyMsg
// lands) and kicks the spinner if it is idle.
func (m *model) beginSwitch(cmd tea.Cmd) tea.Cmd {
	m.switching = true
	if !m.spinning {
		m.spinning = true
		return tea.Batch(cmd, m.spin.Tick)
	}
	return cmd
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

func (m *model) dispatchSlashCommand(text string) tea.Cmd {
	name := strings.TrimPrefix(strings.Fields(text)[0], "/")
	args := strings.TrimSpace(strings.TrimPrefix(text, "/"+name))
	// zc client-side commands take precedence.
	for _, c := range clientCommands {
		if c.id == name {
			cmd := c.run(m, args)
			m.refreshViewport()
			return cmd
		}
	}
	known := false
	for _, c := range m.knownCommands() {
		if c.id == name {
			known = true
			break
		}
	}
	if !known {
		// Unknown to the client command registry: send as input_submit and
		// let the server route it (native command registry, mod commands,
		// or skill invocation all happen server-side).
		m.closeStreaming()
		m.appendEntry(&entry{kind: entryUser, text: text})
		if err := m.cli.SendMessage(text); err != nil {
			m.appendEntry(&entry{kind: entryError, text: "send failed: " + err.Error()})
		}
		m.refreshViewport()
		return nil
	}
	m.closeStreaming()
	m.appendEntry(&entry{kind: entryUser, text: text})
	if err := m.cli.ExecuteCommand(name, args); err != nil {
		m.appendEntry(&entry{kind: entryError, text: "command failed: " + err.Error()})
	}
	m.refreshViewport()
	return nil
}

func (m *model) resolveApproval(req *protocol.ControlRequest, decision protocol.ApprovalDecision) {
	m.approvals = m.approvals[1:]
	if err := m.cli.SendApprovalResponse(req.RequestID, decision.Behavior, decision.UpdatedInput); err != nil {
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
	// ── ui-server frames ──
	case f.HelloResponse != nil:
		// Hello handshake already processed in ConnectStdio; this is a
		// duplicate if it reaches here. Ignore.
		return

	case f.DeviceStatusFlat != nil:
		ds := f.DeviceStatusFlat
		if ds.PermissionMode != "" {
			m.mode = ds.PermissionMode
		}
		if ds.CWD != "" {
			m.serverCWD = ds.CWD
		}
		if ds.AgentName != "" {
			m.agentName = ds.AgentName
		}
		if ds.ConversationID != "" {
			m.cli.Runtime.ConversationID = ds.ConversationID
		}
		if ds.Model != "" {
			m.modelHandle = ds.Model
		}
		if ds.LettaCodeVersion != "" {
			m.serverVersion = ds.LettaCodeVersion
			if !m.versionWarned && !strings.HasPrefix(ds.LettaCodeVersion, testedLettaCode) {
				m.versionWarned = true
				m.appendEntry(&entry{kind: entryInfo, text: fmt.Sprintf(
					"⚠ letta-code %s differs from tested %sx — protocol drift possible",
					ds.LettaCodeVersion, testedLettaCode)})
			}
		}
		// Tools list provides autocomplete data
		if len(ds.Tools) > 0 {
			// Tools are available for reference but not used for command catalog
		}

	case f.TurnStart != nil:
		m.spinning = true
		m.lastUsage = protocol.Delta{}

	case f.TurnEnd != nil:
		m.spinning = false
		m.closeStreaming()
		if f.TurnEnd.StopReason == "cancelled" {
			m.appendEntry(&entry{kind: entryInfo, text: "interrupted"})
		}

	case f.TranscriptUpdate != nil:
		m.handleDelta(&f.TranscriptUpdate.Chunk)

	case f.TranscriptSync != nil:
		// Replace transcript with accumulated entries from the server.
		// This is the authoritative state — reconcile any divergence.
		if len(f.TranscriptSync.Entries) > 0 {
			m.resetEntries()
			for _, te := range f.TranscriptSync.Entries {
				e := &entry{text: te.Text}
				switch te.Kind {
				case "user":
					e.kind = entryUser
				case "assistant":
					e.kind = entryAssistant
				case "tool_call", "tool_return":
					e.kind = entryTool
				case "thinking":
					e.kind = entryReasoning
				case "error":
					e.kind = entryError
				case "shell":
					e.kind = entryCommand
				default:
					e.kind = entryInfo
				}
				m.appendEntry(e)
			}
			m.refreshViewport()
		}

	case f.ControlRequest != nil:
		m.enqueueApproval(f.ControlRequest)

	case f.CommandResult != nil:
		kind := entryInfo
		if !f.CommandResult.Success {
			kind = entryError
		}
		m.appendEntry(&entry{kind: kind, text: f.CommandResult.Output})
		m.refreshViewport()

	case f.BashOutput != nil:
		if f.BashOutput.Error != "" {
			m.appendEntry(&entry{kind: entryError, text: f.BashOutput.Error})
		} else {
			m.appendEntry(&entry{kind: entryCommand, text: f.BashOutput.Output})
		}
		m.refreshViewport()

	case f.ErrorMsg != nil:
		m.appendEntry(&entry{kind: entryError, text: f.ErrorMsg.Message})
		m.refreshViewport()

	case f.OverlayState != nil:
		// Overlay state from the server — render the top descriptor.
		if len(f.OverlayState.Stack) > 0 {
			top := f.OverlayState.Stack[len(f.OverlayState.Stack)-1]
			items := make([]overlayItem, len(top.Items))
			for i, it := range top.Items {
				items[i] = overlayItem{id: it.ID, title: it.Label, desc: it.Description}
			}
			m.overlay = &overlay{kind: overlayMgmt, title: top.Title, items: items}
			m.layout()
		} else {
			m.overlay = nil
			m.layout()
		}

	// ── Legacy protocol_v2 frames (should not arrive under ui-server) ──
	case f.StreamDelta != nil:
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
				// Keep enough for the expanded card view.
				e.toolReturn = truncateLines(ret, toolBodyExpandedLines+1, 6000)
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

	case "usage_statistics":
		m.lastUsage = *d
		m.sessionTokens += d.CompletionTokens

	case "stop_reason":
		m.closeStreaming()
	}
}

func (m *model) closeStreaming() {
	if m.openAssistant != nil {
		m.openAssistant.finished = true
		m.openAssistant.stream = nil // final render is a clean full render
	}
	if m.openReasoning != nil {
		m.openReasoning.finished = true
	}
	m.openAssistant = nil
	m.openReasoning = nil
}

func (m *model) turnActive() bool {
	return m.loopStatus != protocol.LoopWaitingOnInput && m.loopStatus != ""
}

// ── view ──

func (m *model) appendEntry(e *entry) {
	m.entries = append(m.entries, e)
	m.list.Append(entryItem{e: e, m: m})
}

// resetEntries clears the transcript (conversation switch).
func (m *model) resetEntries() {
	m.entries = nil
	m.list.SetItems(nil)
}

// extraHeight is everything between the transcript and the statusline
// other than the input itself: activity lines, completion dropdown, help
// view, and the input border (2 rows). Dialogs float OVER the transcript
// (see dialog.go) and cost no height.
func (m *model) extraHeight() int {
	h := 2 // input border
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
	m.list.SetSize(m.width, vpH)
	m.ready = m.width > 0
	m.input.SetWidth(m.width - 2)
}

// refreshViewport keeps the window geometry honest when modal/activity/
// completion heights change. Content updates need no work here — the lazy
// list materializes them on the next View().
func (m *model) refreshViewport() {
	if h := m.extraHeight(); h != m.lastModalH {
		m.layout()
	}
}

// glamourStyle is resolved ONCE in main() before bubbletea owns the
// terminal. glamour's WithAutoStyle queries the terminal background (OSC);
// doing that mid-session makes the query RESPONSE arrive on stdin where it
// gets parsed as keystrokes — the "escape codes typed into the input" bug.
var glamourStyle = "dark"

// markdownRenderer returns a width-matched glamour renderer, rebuilt on
// resize.
func (m *model) markdownRenderer(width int) *glamour.TermRenderer {
	if m.markdown != nil && m.markdownWidth == width {
		return m.markdown
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(glamourStyle),
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

// entryCacheKey is the invalidation key shared by renderEntry's memo and
// the list-level line cache: content lengths + every display toggle that
// affects output.
func (m *model) entryCacheKey(e *entry) string {
	return fmt.Sprintf("%d|%d|%d|%s|%v|%v|%v",
		len(e.text), e.toolArgs.Len(), len(e.toolReturn),
		e.toolStatus, m.showReasoning, m.showToolOutput, e.finished)
}

// renderEntry renders one entry with caching: only entries whose content
// or display state changed pay rendering cost.
func (m *model) renderEntry(e *entry, w int) string {
	key := fmt.Sprintf("%d|%s", w, m.entryCacheKey(e))
	if e.cachedKey == key && e.cachedRender != "" {
		return e.cachedRender
	}
	wrap := lipgloss.NewStyle().Width(w)
	var out string
	switch e.kind {
	case entryUser:
		out = wrap.Render(rail(theme.User) + styleUser.Render("you ▸ ") + e.text)
	case entryAssistant:
		var md string
		if !e.finished && e == m.openAssistant {
			// Streaming: stable-prefix cache renders only the tail.
			if e.stream == nil {
				e.stream = &streamMD{}
			}
			md = m.renderStreamingMarkdown(e.stream, e.text, w-2)
		} else if r := m.markdownRenderer(w - 2); r != nil {
			if rendered, err := r.Render(e.text); err == nil {
				md = strings.Trim(rendered, "\n")
			}
		}
		if md == "" {
			md = e.text
		}
		out = styleAgent.Render(iconAgent+" agent") + "\n" + md
	case entryReasoning:
		if m.showReasoning {
			out = wrap.Render(styleReasoning.Render("· " + e.text))
		} else {
			out = wrap.Render(styleReasoning.Render(fmt.Sprintf(
				"· reasoning (%d chars — ctrl+r expands)", len(e.text))))
		}
	case entryTool:
		out = wrap.Render(m.renderToolCard(e, w))
	case entryCommand:
		out = wrap.Render(styleAccent.Render("⌁ "+e.cmdInput) + "\n" + e.text)
	case entryDoc:
		md := ""
		if r := m.markdownRenderer(w - 2); r != nil {
			if rendered, err := r.Render(e.text); err == nil {
				md = strings.Trim(rendered, "\n")
			}
		}
		if md == "" {
			md = e.text
		}
		out = styleAccent.Render("▤ "+e.cmdInput) + "\n" + md
	case entryDiff:
		var b strings.Builder
		b.WriteString(styleAccent.Render("± "+e.cmdInput) + "\n")
		for _, line := range strings.Split(strings.TrimRight(e.text, "\n"), "\n") {
			switch {
			case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
				b.WriteString(styleDiffAdd.Render(line) + "\n")
			case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
				b.WriteString(styleDiffRemove.Render(line) + "\n")
			default:
				b.WriteString(styleDiffCtx.Render(line) + "\n")
			}
		}
		out = wrap.Render(strings.TrimRight(b.String(), "\n"))
	case entryInfo:
		out = wrap.Render(styleInfo.Render(e.text))
	case entryError:
		out = wrap.Render(styleError.Render(e.text))
	}
	e.cachedRender = out
	e.cachedKey = key
	return out
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

	w := dialogWidth(m.width)
	body := fmt.Sprintf("agent wants to run %s%s\n\n%s\n\n%s",
		styleToolName.Render(req.Request.ToolName), queued, detail, styleInfo.Render(options))
	return dialogBox("tool approval", "a/d or a number", body, w)
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
		status = m.spin.View() + " " + styleAccent.Render(status)
	} else {
		status = styleInfo.Render(status)
	}
	if m.switching {
		status = m.spin.View() + " " + styleAccent.Render("loading conversation…")
	}
	if !m.connected {
		status = styleError.Render("✕ disconnected")
	}
	pill := styleModePill.Background(modeColor(m.mode)).Render(string(m.mode))

	agent := m.agentName
	if agent == "" {
		agent = shortID(m.cli.Runtime.AgentID)
	}
	conv := m.convTitle
	if conv == "" {
		conv = m.cli.Runtime.ConversationID
	}
	conv = compactOneLine(conv, 32)
	queue := ""
	if n := len(m.queue); n > 0 {
		queue = styleAccent.Render(fmt.Sprintf("%s%d", iconQueue, n))
	}
	hints := styleInfo.Render("^k cmds · ^p convs · ^j newline · ^g help ")

	// Custom template (/statusline): substitute placeholders and render flat.
	if m.statuslineTemplate != "" {
		line := m.statuslineTemplate
		for k, v := range map[string]string{
			"{mode}":         pill,
			"{agent}":        agent,
			"{conversation}": conv,
			"{model}":        shortModel(m.modelHandle),
			"{status}":       status,
			"{queue}":        queue,
			"{hints}":        hints,
		} {
			line = strings.ReplaceAll(line, k, v)
		}
		return line
	}

	left := pill + styleInfo.Render(fmt.Sprintf(" %s · %s ", agent, conv))
	if model := shortModel(m.modelHandle); model != "" {
		left += styleAccent.Render("◇ "+model) + " "
	}
	left += status
	if queue != "" {
		left += " · " + queue
	}
	right := hints
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

// shortModel strips the provider prefix from a model handle for display.
func shortModel(handle string) string {
	if i := strings.LastIndex(handle, "/"); i >= 0 {
		return handle[i+1:]
	}
	return handle
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

// View declares terminal features (alt screen, mouse) and delegates content
// rendering to viewContent — the v2 declarative style.
func (m *model) View() tea.View {
	v := tea.NewView(m.viewContent())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	// Terminal window/tab title (native sets this via /title; zc keeps it
	// automatic: who am I talking to, in which conversation).
	title := "zc"
	if m.agentName != "" {
		title += " — " + m.agentName
		if conv := m.convTitle; conv != "" {
			title += " · " + compactOneLine(conv, 40)
		} else if m.cli != nil && m.cli.Runtime.ConversationID != "" {
			title += " · " + m.cli.Runtime.ConversationID
		}
	}
	v.WindowTitle = title
	return v
}

func (m *model) viewContent() string {
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
	parts := []string{m.list.View()}
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
	parts = append(parts, m.statusline())
	base := strings.Join(parts, "\n")

	// Dialogs float centered over everything (crush-style overlay).
	if box := m.activeDialog(); box != "" {
		return compositeDialog(base, box, m.width, m.height)
	}
	// Completions float anchored just above the input box.
	if m.completion.visible {
		box := styleOverlay.Render(m.renderCompletions())
		y := m.height - 1 - lipgloss.Height(inputBox) - lipgloss.Height(box)
		return compositeAt(base, box, 1, y)
	}
	return base
}

// ── helpers ──

func shortID(id string) string {
	if len(id) > 20 {
		return id[:20] + "…"
	}
	return id
}

// kvBlock renders aligned key/value rows (accent keys, plain values) — the
// house style for status-style command output.
func kvBlock(rows [][2]string) string {
	keyW := 0
	for _, r := range rows {
		if len(r[0]) > keyW {
			keyW = len(r[0])
		}
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(styleAccent.Render(fmt.Sprintf("%*s", keyW, r[0])) + "  " + r[1] + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
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

// ── main ──

func main() {
	agentID := flag.String("agent", "", "agent id or name (required)")
	conversationID := flag.String("conversation", "", "conversation id to resume (default: create new)")
	port := flag.Int("port", 8493, "loopback port for the spawned app-server")
	mode := flag.String("mode", "", "permission mode: standard | acceptEdits | unrestricted (default: server default)")
	flag.Parse()

	// Open a log file for the ui-server's stderr.
	logDir := filepath.Join(os.Getenv("HOME"), ".letta", "logs")
	os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, "zc-ui-server.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open server log: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	// Resolve terminal background ONCE, pre-tea: this both fixes glamour's
	// style and resolves the whole theme so no adaptive-color code queries
	// the terminal mid-session (see glamourStyle).
	isDark := lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
	if !isDark {
		glamourStyle = "light"
	}
	initTheme(isDark)

	// Connection, agent pick, and runtime start all happen inside the TUI
	// (phase state machine) — no stdout preamble.
	m := newModel(nil, logPath, *port, logFile)
	m.startOpts = client.StdioOptions{
		Agent:          *agentID,
		ConversationID: *conversationID,
		Mode:           protocol.PermissionMode(*mode),
		ServerLog:      logFile,
	}

	p := tea.NewProgram(m) // alt screen + mouse mode are declared in View()
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tui error: %v\n", err)
		os.Exit(1)
	}
	if m.cli != nil {
		m.cli.Close()
	}
}
