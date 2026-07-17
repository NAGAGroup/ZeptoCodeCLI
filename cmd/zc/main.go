// zc — a Go/bubbletea TUI client for `letta ui-server`, speaking the zc UI
// protocol (SPEC draft 5, protocol.ts PROTOCOL_VERSION 2).
//
// Architecture: the server pushes JSON state frames; this client is a pure
// REDUCER (State, frame) -> State plus a View that renders from State. There
// is no request/response correlation, no pending maps, no blocking IO in
// Update — all child IO lives in the client read goroutine and in tea.Cmds.
//
// Stage 0 (lobby) + Stage 1 (core chat) vertical slice:
//
//	lobby: agent picker -> conversation picker -> chat
//	chat:  transcript (Line[] pushed whole, REPLACED per frame), statusline
//	       from device+turn, streaming, esc-interrupt, approval modal,
//	       shift+tab mode cycle, toasts, mod-panel polling.
//
// Run: pixi run zc -- [--agent <id>] [--conversation <id|new>]
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
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

// ── State: the reducer target (the server's pushed state, held 1:1) ──

type toast struct {
	level  string
	text   string
	expire time.Time
}

// statusHint is an ephemeral hint shown in the statusline area.
type statusHint struct {
	text   string
	level  string // info | warning | error
	expire time.Time
}

// State is the held copy of the server-pushed state. Update mutates it; View
// renders from it. Every field maps to exactly one server frame (SPEC §4).
type State struct {
	phase string // "" | "lobby" | "starting" | "chat"

	// slash-command palette source (pushed on entering chat)
	commands []protocol.CommandInfo

	// turn queue (messages submitted while a turn is active)
	queue         []protocol.QueueItem
	queueDeferred bool

	// chat
	transcript       []protocol.TranscriptLine // REPLACED wholesale per `transcript` frame
	turn             protocol.TurnState        // spinner / gating / interrupt arming
	device           *protocol.DeviceState     // statusline / header source
	pendingApprovals []protocol.PendingApproval
	modPanels        []protocol.ModPanel
	toasts           []toast
	statusHint       *statusHint
	convID           string

	// handshake info
	runtimeBuild string
	lettaVersion string
}

// ── model: State + the client + all UI/render machinery ──

type model struct {
	cli     *client.Client
	frames  <-chan any
	logPath string

	width, height int
	ready         bool
	quitting      bool
	disconnected  bool
	startupErr    string

	st State

	// pickers: slash-command palette + the unified tagged selection picker
	// (drives BOTH the lobby agent/conversation cascade and in-chat pickers).
	palette   *overlay
	selection *overlay
	selQ      *selectionQuestions

	// "@" file-path completion popup (resolved against serverCWD)
	complete    *overlay
	completeTok string // the "@partial" token currently being completed

	// client-side paste registry: large pastes are stashed here and shown as
	// "[Pasted text #N +M lines]" placeholders, resolved on submit.
	pastes   map[string]string
	pasteSeq int

	// chat UI
	list           *list.List
	input          textarea.Model
	spin           spinner.Model
	spinning       bool
	helpModel      help.Model
	showHelp       bool
	helpScroll     int // scroll offset in the help overlay
	showReasoning  bool
	showToolOutput bool

	// markdown rendering
	markdown      *glamour.TermRenderer
	markdownWidth int
	streamCache   map[string]*streamMD // line id -> streaming md prefix cache

	// stable transcript item identity across Sync (keyed by line id)
	itemsByID map[string]*lineItem

	// approval / question modal
	question *questionForm

	// derived-from-device ambient status
	mode        protocol.PermissionMode
	serverCWD   string
	agentName   string
	convTitle   string
	modelHandle string
	isDark      bool // for user message background selection

	// token smoothing display
	displayTokens int

	// input history recall
	history      []string
	historyIdx   int
	historyDraft string // preserved draft when browsing history

	escArmed       time.Time // double-escape-to-clear arming
	placeholderIdx int       // rotating placeholder cursor
	ctrlCArmed     time.Time
	modPanelPoll bool
	focused      bool
	approvalSel  int // cursor in the numbered approval options
	startOpts    client.Options

	// approval UX: custom deny input + memoized modal content
	denyText                   string
	approvalModalCacheKey      string
	approvalModalCachedContent string

	// approval UX: stubs for sequential approvals (id -> decision)
	decidedApprovals map[string]decidedApproval

	// transient hint tracking
	lastQueueDeferred bool
	lastBashCount     int
}

// decidedApproval tracks a user decision before the server removes it from
// pending_approvals. Used to render "queued" stubs in the transcript area.
type decidedApproval struct {
	decision string // "allow" | "deny"
	toolName string
}

// ── tea messages ──

type framesMsg struct{ frames []any }
type disconnectedMsg struct{}
type toastTickMsg struct{}
type modPanelTickMsg struct{}
type hintTickMsg struct{}
type placeholderTickMsg struct{}

// Rotating inspirational placeholders shown when the input is empty.
var placeholderHints = []string{
	"Message the agent · esc interrupts · shift+tab mode · ctrl+g help",
	`Try "help me understand this codebase"`,
	`Try "debug this error"`,
	`Try "explain what this function does"`,
	`Try "review this pull request"`,
}

func placeholderTick() tea.Cmd {
	return tea.Tick(6*time.Second, func(time.Time) tea.Msg { return placeholderTickMsg{} })
}

// waitForFrame blocks for one frame then drains everything already queued so
// heavy streams collapse into one render per batch. The read loop never drops
// frames; a closed channel means the child exited.
func (m *model) waitForFrame() tea.Cmd {
	frames := m.frames
	return func() tea.Msg {
		f, ok := <-frames
		if !ok {
			return disconnectedMsg{}
		}
		batch := []any{f}
		for len(batch) < 512 {
			select {
			case f, ok := <-frames:
				if !ok {
					return framesMsg{batch}
				}
				batch = append(batch, f)
			default:
				return framesMsg{batch}
			}
		}
		return framesMsg{batch}
	}
}

func newModel(cli *client.Client, logPath string, opts client.Options) *model {
	ta := textarea.New()
	ta.Placeholder = "Message the agent · esc interrupts · shift+tab mode · ctrl+g help"
	ta.Prompt = ""
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	st := ta.Styles()
	st.Focused.CursorLine = lipgloss.NewStyle()
	st.Focused.Placeholder = lipgloss.NewStyle().Foreground(theme.Dim)
	st.Blurred.Placeholder = lipgloss.NewStyle().Foreground(theme.Dim)
	ta.SetStyles(st)
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = lipgloss.NewStyle().Foreground(theme.Accent)

	return &model{
		cli:         cli,
		frames:      cli.Frames(),
		logPath:     logPath,
		list:        list.New(),
		input:       ta,
		spin:        sp,
		helpModel:   newHelp(),
		streamCache: map[string]*streamMD{},
		itemsByID:   map[string]*lineItem{},
		mode:        "?",
		focused:     true,
		startOpts:   opts,
		decidedApprovals: map[string]decidedApproval{},
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.waitForFrame(), textarea.Blink, placeholderTick())
}

// ── list adapter: one transcript Line rendered lazily & memoized ──

// lineItem is one transcript entry in the list.
type lineItem struct {
	m      *model
	line   *protocol.TranscriptLine
	frozen bool // once true, CacheKey returns "" and the list never re-renders
}

func (it *lineItem) Render(w int) string { return it.m.renderLine(it.line, w) }
func (it *lineItem) CacheKey() string {
	if it.frozen {
		return "" // frozen items are memoized forever by the list
	}
	return it.m.lineCacheKey(it.line)
}

// rebuildTranscript re-derives the list items from the (freshly replaced)
// transcript state. Item identity is stable per line id so unchanged lines
// keep their memoized render and the scroll position is preserved.
// When a line's phase changes to "finished", it is frozen (never re-rendered).
func (m *model) rebuildTranscript() {
	items := make([]list.Item, 0, len(m.st.transcript))
	present := make(map[string]bool, len(m.st.transcript))
	for i := range m.st.transcript {
		l := &m.st.transcript[i]
		present[l.ID] = true
		it := m.itemsByID[l.ID]
		if it == nil {
			it = &lineItem{m: m}
			m.itemsByID[l.ID] = it
		}
		it.line = l
		// Freeze finished items so they never re-render.
		if l.Phase == "finished" {
			it.frozen = true
		}
		items = append(items, it)
	}
	for id := range m.itemsByID {
		if !present[id] {
			delete(m.itemsByID, id)
			delete(m.streamCache, id)
		}
	}
	m.list.Sync(items)
}

// ── Update: the reducer over frame batches + key handling ──

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.PasteMsg:
		// Large pastes become a client-side placeholder; small pastes insert
		// inline. Nothing goes to the server until submit (see handleSubmit).
		if m.st.phase == "chat" && m.palette == nil && m.selection == nil &&
			m.question == nil && len(m.st.pendingApprovals) == 0 {
			m.handlePaste(msg.Content)
			m.refreshCompletion()
		}
		return m, nil

	case tea.KeyReleaseMsg:
		// Kitty-protocol releases must never route anywhere.
		return m, nil

	case tea.FocusMsg:
		m.focused = true
		return m, nil

	case tea.BlurMsg:
		m.focused = false
		return m, nil

	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.list.ScrollBy(-3)
		case tea.MouseWheelDown:
			m.list.ScrollBy(3)
		}
		return m, nil

	case framesMsg:
		return m.reduceBatch(msg.frames)

	case disconnectedMsg:
		m.disconnected = true
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		if m.turnActive() {
			return m, cmd
		}
		m.spinning = false
		return m, nil

	case toastTickMsg:
		m.pruneToasts()
		if len(m.st.toasts) > 0 {
			return m, toastTick()
		}
		return m, nil

	case modPanelTickMsg:
		if m.st.phase == "chat" && !m.disconnected {
			m.cli.Send(protocol.NewModPanelsQuery(max(m.width, 1)))
			return m, modPanelTick()
		}
		m.modPanelPoll = false
		return m, nil

	case hintTickMsg:
		m.pruneStatusHint()
		if m.st.statusHint != nil {
			return m, hintTick()
		}
		return m, nil

	case placeholderTickMsg:
		// Rotate the empty-input placeholder through inspirational hints.
		if m.st.phase == "chat" && strings.TrimSpace(m.input.Value()) == "" {
			m.placeholderIdx = (m.placeholderIdx + 1) % len(placeholderHints)
			m.input.Placeholder = placeholderHints[m.placeholderIdx]
		}
		return m, placeholderTick()
	}

	// Form-internal messages (cursor blink) reach the active question form.
	if m.question != nil {
		cmd := m.question.form.Update(msg)
		m.finishQuestionIfDone()
		return m, cmd
	}
	// Everything else (blink ticks) goes to the input.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// reduceBatch folds a batch of frames into State, then does the post-fold
// bookkeeping (rebuild transcript, relayout) and re-arms the frame reader.
func (m *model) reduceBatch(frames []any) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	transcriptChanged := false
	for _, f := range frames {
		if c := m.reduce(f, &transcriptChanged); c != nil {
			cmds = append(cmds, c)
		}
	}
	if transcriptChanged {
		m.rebuildTranscript()
	}
	m.layout()

	if !m.disconnected {
		cmds = append(cmds, m.waitForFrame())
	}
	if m.turnActive() && !m.spinning {
		m.spinning = true
		cmds = append(cmds, m.spin.Tick)
	}
	if m.st.phase == "chat" && !m.modPanelPoll {
		m.modPanelPoll = true
		cmds = append(cmds, modPanelTick())
	}
	return m, tea.Batch(cmds...)
}

// reduce applies one frame to State. It returns an optional follow-up cmd
// (form init, toast TTL tick). No blocking IO happens here.
func (m *model) reduce(f any, transcriptChanged *bool) tea.Cmd {
	switch fr := f.(type) {
	case *protocol.HelloResponse:
		m.st.runtimeBuild = fr.RuntimeBuild
		m.st.lettaVersion = fr.LettaCodeVer
		if fr.Error != "" {
			m.startupErr = fr.Error
		}

	case *protocol.SessionPhase:
		m.st.phase = fr.Phase
		switch fr.Phase {
		case "chat":
			m.st.convID = fr.ConversationID
			if fr.AgentName != "" {
				m.agentName = fr.AgentName
			}
			m.selection = nil // close any open lobby/switch picker
			m.selQ = nil
			m.input.Focus()
		case "lobby", "starting":
			// The server pushes a `selection` frame (tag=agent) to drive the lobby.
		}

	case *protocol.DeviceState:
		m.st.device = fr
		m.applyDevice(fr)

	case *protocol.Transcript:
		m.st.transcript = fr.Lines // authoritative REPLACE — never merge/append
		m.st.convID = fr.ConversationID
		*transcriptChanged = true

	case *protocol.TurnState:
		wasActive := m.st.turn.Status != "" && m.st.turn.Status != "idle"
		m.st.turn = *fr
		// Focus-aware notification: ping the terminal when a turn finishes while
		// the window is unfocused (native OSC/bell behavior).
		if wasActive && fr.Status == "idle" && fr.StopReason == "end_turn" && !m.focused {
			return notifyTurnComplete(m.agentName)
		}

	case *protocol.PendingApprovals:
		m.st.pendingApprovals = fr.Items
		// prune decided approvals that are no longer pending
		for id := range m.decidedApprovals {
			found := false
			for _, a := range fr.Items {
				if a.ID == id {
					found = true
					break
				}
			}
			if !found {
				delete(m.decidedApprovals, id)
			}
		}
		return m.syncApprovalUI()

	case *protocol.CommandCatalog:
		m.st.commands = fr.Commands

	case *protocol.Selection:
		m.openSelection(fr)

	case *protocol.ModPanels:
		m.st.modPanels = fr.Panels

	case *protocol.QueueState:
		m.st.queue = fr.Items
		m.st.queueDeferred = fr.Deferred

	case *protocol.Toast:
		m.pushToast(fr.Level, fr.Message)
		return toastTick()

	case *protocol.Notification:
		m.pushToast(fr.Level, fr.Message)
		return toastTick()

	case *protocol.ErrorFrame:
		m.pushToast("error", fr.Message)
		return toastTick()

	case client.Disconnected:
		m.disconnected = true

	default:
		// ChangeSignal, Unknown: not consumed in the current slice.
	}
	return nil
}

// applyDevice pulls ambient status out of the device frame (never hardcoded).
func (m *model) applyDevice(d *protocol.DeviceState) {
	m.mode = d.PermissionMode
	m.serverCWD = d.CWD
	if d.Agent.Name != "" {
		m.agentName = d.Agent.Name
	}
	if d.Conversation.Title != "" {
		m.convTitle = d.Conversation.Title
	}
	switch {
	case d.Model.Handle != "":
		m.modelHandle = d.Model.Handle
	case d.Model.Label != "":
		m.modelHandle = d.Model.Label
	}
	if d.Usage != nil {
		m.smoothTokens(d.Usage.TotalTokens)
	}
}

// ── toasts ──

func (m *model) pushToast(level, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	m.st.toasts = append(m.st.toasts, toast{level: level, text: text, expire: time.Now().Add(6 * time.Second)})
	if len(m.st.toasts) > 4 {
		m.st.toasts = m.st.toasts[len(m.st.toasts)-4:]
	}
}

func (m *model) pruneToasts() {
	now := time.Now()
	kept := m.st.toasts[:0]
	for _, t := range m.st.toasts {
		if t.expire.After(now) {
			kept = append(kept, t)
		}
	}
	m.st.toasts = kept
}

func toastTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return toastTickMsg{} })
}

func modPanelTick() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return modPanelTickMsg{} })
}

func hintTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return hintTickMsg{} })
}

// ── statusline hints ──

func (m *model) pushStatusHint(level, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	m.st.statusHint = &statusHint{level: level, text: text, expire: time.Now().Add(3 * time.Second)}
}

func (m *model) pruneStatusHint() {
	if m.st.statusHint != nil && time.Now().After(m.st.statusHint.expire) {
		m.st.statusHint = nil
	}
}

// ── token smoothing ──

func (m *model) smoothTokens(target int) {
	gap := target - m.displayTokens
	if gap == 0 {
		return
	}
	step := 0
	switch {
	case gap > 0 && gap < 70:
		step = min(gap, 3)
	case gap > 0 && gap < 200:
		step = max(1, int(float64(gap)*0.15))
	case gap > 0:
		step = min(gap, 50)
	case gap < 0 && -gap < 70:
		step = max(gap, -3)
	case gap < 0 && -gap < 200:
		step = min(-1, int(float64(gap)*0.15))
	case gap < 0:
		step = max(gap, -50)
	}
	m.displayTokens += step
}

// isBYOK returns (isBYOK, isOpenAICodex) for a model handle.
func isBYOK(handle string) (bool, bool) {
	h := strings.ToLower(handle)
	if strings.Contains(h, "openai") {
		return true, true
	}
	if strings.Contains(h, "anthropic") || strings.Contains(h, "claude") ||
		strings.Contains(h, "gemini") || strings.Contains(h, "google") ||
		strings.Contains(h, "deepseek") || strings.Contains(h, "mistral") ||
		strings.Contains(h, "groq") || strings.Contains(h, "x.ai") || strings.Contains(h, "xai") {
		return true, false
	}
	return false, false
}

// notifyTurnComplete pings the terminal (OSC 9 desktop notification + BEL) when
// a turn finishes while the window is unfocused. OSC sequences don't move the
// cursor or draw, so they're safe to write mid-alt-screen.
func notifyTurnComplete(agentName string) tea.Cmd {
	return func() tea.Msg {
		name := agentName
		if name == "" {
			name = "zc"
		}
		fmt.Fprintf(os.Stdout, "\x1b]9;%s finished a turn\x07\a", name)
		return nil
	}
}



// ── slash-command palette (chat phase) ──

// openPalette builds the slash-command palette from the stored command_catalog.
// Hidden commands are omitted. The filter is the text the user types after "/"
// (client-side only — the catalog is already pushed; no per-keystroke IO).
func (m *model) openPalette() {
	var items []overlayItem
	for _, c := range m.st.commands {
		if c.Hidden {
			continue
		}
		desc := c.Description
		if c.ArgsHint != "" {
			desc = strings.TrimSpace(desc + " " + c.ArgsHint)
		}
		items = append(items, overlayItem{
			id:    c.ID, // includes leading "/"
			title: c.ID,
			desc:  desc,
			badge: c.Source, // builtin | custom | mod
		})
	}
	m.palette = &overlay{kind: overlayPalette, title: "Commands", items: items}
	m.palette.clampSel()
}

// handlePaletteKey routes keys while the slash palette is open. Enter executes
// the highlighted command via execute_command (id stripped of its leading "/").
func (m *model) handlePaletteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.palette
	switch msg.String() {
	case "ctrl+c":
		return m.handleCtrlC()
	case "esc":
		m.palette = nil
		m.input.SetValue("")
		m.autosize()
		return m, nil
	case "up", "ctrl+p":
		p.move(-1)
	case "down", "ctrl+n":
		p.move(1)
	case "enter", "tab":
		// Prefer an EXACT id match over the highlighted fuzzy row so typing a
		// full command ("/context") never picks a prefix sibling ("/context-limit").
		typed := "/" + strings.TrimSpace(p.filter)
		chosen := ""
		for _, it := range p.items {
			if it.id == typed {
				chosen = it.id
				break
			}
		}
		if chosen == "" {
			it, ok := p.current()
			if !ok {
				return m, nil
			}
			chosen = it.id
		}
		// FILL the input with the command (+ trailing space for args) and close
		// the palette — do NOT send. The user can add args, then Enter submits.
		m.palette = nil
		m.input.SetValue(chosen + " ")
		m.input.CursorEnd()
		m.autosize()
		return m, nil
	case "backspace":
		if p.filter == "" {
			// backspacing past the "/" closes the palette.
			m.palette = nil
			m.input.SetValue("")
			m.autosize()
			return m, nil
		}
		p.filter = p.filter[:len(p.filter)-1]
		m.input.SetValue("/" + p.filter)
		m.input.CursorEnd()
		p.sel = 0
		p.clampSel()
	default:
		if s := msg.String(); len(s) == 1 && s >= " " {
			p.filter += s
			m.input.SetValue("/" + p.filter)
			m.input.CursorEnd()
			p.sel = 0
			p.clampSel()
		}
	}
	return m, nil
}

// ── tagged selection picker (SPEC R23) ──

// selectionQuestions steps a multi-question Selection (AskUserQuestion). This
// primarily arrives via pending_approvals in a later stage; here we wire a
// basic sequential single-select renderer so a `selection` with questions is
// handled instead of crashing.
type selectionQuestions struct {
	tag       string
	questions []protocol.SelectionQuestion
	idx       int
	answers   map[string]string
}

// openSelection opens the tagged picker for a `selection` frame. Groups render
// as a grouped/multi picker; questions render as a sequential stepper.
func (m *model) openSelection(s *protocol.Selection) {
	// Clear the palette if one was open — the command produced a selection.
	m.palette = nil
	m.selection = nil
	m.selQ = nil

	if len(s.Groups) > 0 {
		var items []overlayItem
		for _, g := range s.Groups {
			if g.Label != "" {
				items = append(items, overlayItem{title: g.Label, header: true})
			}
			for _, it := range g.Items {
				items = append(items, overlayItem{
					id:       it.ID,
					title:    it.Label,
					desc:     it.Description,
					badge:    it.Badge,
					disabled: it.Disabled,
				})
			}
		}
		title := s.Title
		if title == "" {
			title = "Select"
		}
		m.selection = &overlay{
			kind:     overlaySelection,
			title:    title,
			items:    items,
			tag:      s.Tag,
			multi:    s.Multi,
			selected: map[string]bool{},
		}
		m.selection.clampSel()
		return
	}

	if len(s.Questions) > 0 {
		m.selQ = &selectionQuestions{tag: s.Tag, questions: s.Questions, answers: map[string]string{}}
		m.openSelectionQuestion()
	}
}

// openSelectionQuestion builds the picker for the current question in a
// sequential multi-question selection.
func (m *model) openSelectionQuestion() {
	q := m.selQ.questions[m.selQ.idx]
	var items []overlayItem
	for _, o := range q.Options {
		items = append(items, overlayItem{id: o.ID, title: o.Label, desc: o.Description, badge: o.Badge, disabled: o.Disabled})
	}
	title := q.Prompt
	if len(m.selQ.questions) > 1 {
		title = fmt.Sprintf("%s (%d/%d)", q.Prompt, m.selQ.idx+1, len(m.selQ.questions))
	}
	m.selection = &overlay{
		kind:     overlaySelection,
		title:    title,
		items:    items,
		tag:      m.selQ.tag,
		selected: map[string]bool{},
	}
	m.selection.clampSel()
}

// handleSelectionKey routes keys while a tagged selection picker is open.
func (m *model) handleSelectionKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := m.selection
	switch msg.String() {
	case "ctrl+c":
		return m.handleCtrlC()
	case "esc":
		m.selection = nil
		m.selQ = nil
		return m, nil
	case "up", "ctrl+p":
		s.move(-1)
	case "down", "ctrl+n":
		s.move(1)
	case " ":
		if s.multi {
			if it, ok := s.current(); ok {
				s.toggle(it.id)
			}
		}
	case "enter":
		return m.commitSelection()
	case "backspace":
		if s.filter != "" {
			s.filter = s.filter[:len(s.filter)-1]
			s.sel = 0
			s.clampSel()
		}
	default:
		if str := msg.String(); len(str) == 1 && str >= " " {
			s.filter += str
			s.sel = 0
			s.clampSel()
		}
	}
	return m, nil
}

// commitSelection sends the selection_choice for the current picker. For a
// question stepper it records the answer and advances (or sends `answers`
// once the last question is answered).
func (m *model) commitSelection() (tea.Model, tea.Cmd) {
	s := m.selection

	// Multi-question stepper.
	if m.selQ != nil {
		it, ok := s.current()
		if !ok {
			return m, nil
		}
		q := m.selQ.questions[m.selQ.idx]
		m.selQ.answers[q.ID] = it.id
		m.selQ.idx++
		if m.selQ.idx >= len(m.selQ.questions) {
			m.cli.Send(protocol.NewSelectionAnswers(m.selQ.tag, m.selQ.answers))
			m.selection = nil
			m.selQ = nil
			return m, nil
		}
		m.openSelectionQuestion()
		return m, nil
	}

	// Multi-select: confirm the toggled set.
	if s.multi {
		m.cli.Send(protocol.NewSelectionChoices(s.tag, s.chosen()))
		m.selection = nil
		return m, nil
	}

	// Single-select.
	it, ok := s.current()
	if !ok {
		return m, nil
	}
	m.cli.Send(protocol.NewSelectionChoice(s.tag, it.id))
	m.selection = nil
	return m, nil
}

// ── key handling (chat phase) ──

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Lobby / starting: the server drives a `selection` picker (agent → conv).
	if m.st.phase != "chat" {
		if m.selection != nil {
			return m.handleSelectionKey(msg)
		}
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	}

	// Modals take the slot, in priority order.
	if m.palette != nil {
		return m.handlePaletteKey(msg)
	}
	if m.selection != nil {
		return m.handleSelectionKey(msg)
	}
	if m.question != nil {
		return m.handleQuestionKey(msg)
	}
	if len(m.st.pendingApprovals) > 0 {
		return m.handleApprovalKey(msg)
	}
	// "@" file-path completion popup intercepts navigation/commit keys.
	if m.complete != nil {
		return m.handleCompleteKey(msg)
	}
	if m.showHelp {
		switch msg.String() {
		case "esc", "ctrl+g", "q":
			m.showHelp = false
			m.helpScroll = 0
		case "up", "ctrl+p", "k":
			m.helpScroll--
		case "down", "ctrl+n", "j":
			m.helpScroll++
		case "pgup", "ctrl+u":
			m.helpScroll -= 10
		case "pgdown", "ctrl+d":
			m.helpScroll += 10
		}
		return m, nil
	}

	// A leading "/" on an empty buffer opens the slash-command palette. The
	// palette then filters client-side over the already-pushed catalog.
	if msg.String() == "/" && strings.TrimSpace(m.input.Value()) == "" {
		m.input.SetValue("/")
		m.input.CursorEnd()
		m.openPalette()
		return m, nil
	}

	switch {
	case key.Matches(msg, keys.Quit):
		return m.handleCtrlC()
	case key.Matches(msg, keys.Help):
		m.showHelp = true
		m.helpScroll = 0
		return m, nil
	case key.Matches(msg, keys.Mode):
		return m.cycleMode()
	case key.Matches(msg, keys.Agents):
		m.cli.Send(protocol.NewExecuteCommand("agents"))
		return m, nil
	case key.Matches(msg, keys.Conversations):
		m.cli.Send(protocol.NewExecuteCommand("conversations"))
		return m, nil
	case key.Matches(msg, keys.Palette):
		m.input.SetValue("/")
		m.input.CursorEnd()
		m.openPalette()
		return m, nil
	case key.Matches(msg, keys.ClearOrAbort):
		return m.handleEsc()
	case key.Matches(msg, keys.Send):
		return m.handleSubmit()
	case key.Matches(msg, keys.Reasoning):
		m.showReasoning = !m.showReasoning
		m.list.Sync(m.currentItems())
		return m, nil
	case key.Matches(msg, keys.ToolOutput):
		m.showToolOutput = !m.showToolOutput
		m.list.Sync(m.currentItems())
		return m, nil
	case key.Matches(msg, keys.ScrollUp):
		m.list.PageUp()
		return m, nil
	case key.Matches(msg, keys.ScrollDown):
		m.list.PageDown()
		return m, nil
	case key.Matches(msg, keys.HistoryUp):
		// Recall only on a single-line buffer; otherwise ↑ moves the cursor.
		if !strings.Contains(m.input.Value(), "\n") && len(m.history) > 0 {
			// Preserve the current draft the first time we enter history browsing.
			if m.historyIdx == len(m.history) {
				m.historyDraft = m.input.Value()
			}
			if m.historyIdx > 0 {
				m.historyIdx--
			}
			m.input.SetValue(m.history[m.historyIdx])
			m.input.CursorEnd()
			m.autosize()
			return m, nil
		}
	case key.Matches(msg, keys.HistoryDown):
		if !strings.Contains(m.input.Value(), "\n") && m.historyIdx < len(m.history) {
			m.historyIdx++
			if m.historyIdx == len(m.history) {
				// Back past the newest entry: restore the preserved draft.
				m.input.SetValue(m.historyDraft)
			} else {
				m.input.SetValue(m.history[m.historyIdx])
			}
			m.input.CursorEnd()
			m.autosize()
			return m, nil
		}
	}
	// keys the switch matched-but-declined (history on multiline) fall here.

	// Any content keystroke disarms the double-escape-to-clear window.
	m.escArmed = time.Time{}

	// Default: the textarea owns the key.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.autosize()
	// Re-evaluate the "@" completion popup after any content keystroke.
	m.refreshCompletion()
	return m, cmd
}

// currentItems rebuilds the list.Item slice from the current transcript
// (used when a display toggle changes rendering but the transcript did not).
func (m *model) currentItems() []list.Item {
	items := make([]list.Item, 0, len(m.st.transcript))
	for i := range m.st.transcript {
		l := &m.st.transcript[i]
		it := m.itemsByID[l.ID]
		if it == nil {
			it = &lineItem{m: m, line: l}
			m.itemsByID[l.ID] = it
		}
		items = append(items, it)
	}
	return items
}

func (m *model) handleCtrlC() (tea.Model, tea.Cmd) {
	if m.turnActive() {
		if !m.ctrlCArmed.IsZero() && time.Since(m.ctrlCArmed) < 2*time.Second {
			m.quitting = true
			return m, tea.Quit
		}
		m.ctrlCArmed = time.Now()
		m.pushToast("info", "press ctrl+c again to quit")
		return m, toastTick()
	}
	m.quitting = true
	return m, tea.Quit
}

func (m *model) handleEsc() (tea.Model, tea.Cmd) {
	if m.input.Value() != "" {
		// Double-escape to clear: first press arms (2.5s), second clears.
		if !m.escArmed.IsZero() && time.Since(m.escArmed) < 2500*time.Millisecond {
			m.escArmed = time.Time{}
			m.input.SetValue("")
			m.autosize()
			return m, nil
		}
		m.escArmed = time.Now()
		m.pushStatusHint("info", "press esc again to clear")
		return m, hintTick()
	}
	if m.turnActive() {
		m.cli.Send(protocol.NewInterrupt())
	}
	return m, nil
}

func (m *model) handleSubmit() (tea.Model, tea.Cmd) {
	text := strings.TrimRight(m.input.Value(), "\n")
	if strings.TrimSpace(text) == "" {
		return m, nil
	}
	// Resolve any paste placeholders back to their full stored content before
	// sending, then drop the registry.
	resolved := m.resolvePastes(text)
	m.cli.Send(protocol.NewInputSubmit(resolved))
	m.clearPastes()
	m.complete = nil
	// Deduplicate: don't add an identical consecutive entry (native behavior).
	if len(m.history) == 0 || m.history[len(m.history)-1] != resolved {
		m.history = append(m.history, resolved)
	}
	m.historyIdx = len(m.history)
	m.historyDraft = ""
	m.input.SetValue("")
	m.autosize()
	return m, nil
}

func (m *model) cycleMode() (tea.Model, tea.Cmd) {
	next := map[protocol.PermissionMode]protocol.PermissionMode{
		protocol.ModeStandard:     protocol.ModeAcceptEdits,
		protocol.ModeAcceptEdits:  protocol.ModeUnrestricted,
		protocol.ModeUnrestricted: protocol.ModeStandard,
	}[m.mode]
	if next == "" {
		next = protocol.ModeAcceptEdits
	}
	m.cli.Send(protocol.NewChangeMode(next))
	m.mode = next // optimistic; the device frame re-echoes authoritatively
	m.pushStatusHint("info", "permission mode → "+string(next))
	return m, hintTick()
}

// ── approval / question modal ──

// approvalOptionCount returns the number of numbered options for the head
// approval: "Yes" + one per permission suggestion + "No" (with inline text input).
func (m *model) approvalOptionCount() int {
	if len(m.st.pendingApprovals) == 0 {
		return 0
	}
	return 2 + len(m.st.pendingApprovals[0].PermissionSuggestions) // Yes + suggestions + No
}

// sendApprovalChoice sends the response for the selected numbered option.
func (m *model) sendApprovalChoice(sel int) {
	a := m.st.pendingApprovals[0]
	last := m.approvalOptionCount() - 1
	switch {
	case sel == 0: // Yes
		m.cli.Send(protocol.NewApprovalResponse(a.ID, "allow"))
		m.decidedApprovals[a.ID] = decidedApproval{decision: "allow", toolName: a.ToolName}
	case sel == last: // No
		resp := protocol.NewApprovalResponse(a.ID, "deny")
		if m.denyText != "" {
			resp.Message = m.denyText
		}
		m.cli.Send(resp)
		m.decidedApprovals[a.ID] = decidedApproval{decision: "deny", toolName: a.ToolName}
	default: // Yes + permission suggestion
		resp := protocol.NewApprovalResponse(a.ID, "allow")
		resp.SelectedSuggestionIDs = []string{a.PermissionSuggestions[sel-1].ID}
		m.cli.Send(resp)
		m.decidedApprovals[a.ID] = decidedApproval{decision: "allow", toolName: a.ToolName}
	}
	m.denyText = "" // reset custom deny text
}

func (m *model) handleApprovalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if len(m.st.pendingApprovals) == 0 {
		return m, nil
	}
	a := m.st.pendingApprovals[0]
	n := m.approvalOptionCount()
	last := n - 1
	s := msg.String()

	// When the "No" option is selected, typing enters custom denial text.
	if m.approvalSel == last {
		switch s {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "up", "ctrl+p":
			if m.approvalSel > 0 {
				m.approvalSel--
				m.denyText = ""
			}
		case "down", "ctrl+n":
			if m.approvalSel < n-1 {
				m.approvalSel++
				m.denyText = ""
			}
		case "enter":
			m.sendApprovalChoice(m.approvalSel)
		case "esc":
			if m.denyText != "" {
				m.denyText = ""
			} else {
				m.cli.Send(protocol.NewApprovalResponse(a.ID, "deny"))
				m.decidedApprovals[a.ID] = decidedApproval{decision: "deny", toolName: a.ToolName}
			}
		case "backspace":
			if len(m.denyText) > 0 {
				m.denyText = m.denyText[:len(m.denyText)-1]
			}
		case "y":
			m.sendApprovalChoice(0)
		case "d", "n":
			// Already on No; ignore
		default:
			// Number keys jump to another option directly (1-indexed).
			if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
				if idx := int(s[0] - '1'); idx < n {
					m.approvalSel = idx
					m.denyText = ""
					if idx != last {
						m.sendApprovalChoice(idx)
					}
				}
			} else if len(s) == 1 && s[0] >= ' ' {
				// Append printable character to custom deny text
				m.denyText += s
			}
		}
		return m, nil
	}

	// Not on the "No" option: standard navigation.
	switch s {
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "up", "ctrl+p":
		if m.approvalSel > 0 {
			m.approvalSel--
		}
	case "down", "ctrl+n":
		if m.approvalSel < n-1 {
			m.approvalSel++
		}
	case "enter":
		m.sendApprovalChoice(m.approvalSel)
	case "esc":
		m.cli.Send(protocol.NewApprovalResponse(a.ID, "deny"))
		m.decidedApprovals[a.ID] = decidedApproval{decision: "deny", toolName: a.ToolName}
	case "y":
		m.sendApprovalChoice(0)
	case "d", "n":
		m.cli.Send(protocol.NewApprovalResponse(a.ID, "deny"))
		m.decidedApprovals[a.ID] = decidedApproval{decision: "deny", toolName: a.ToolName}
	default:
		// Number keys jump to and select an option directly (1-indexed).
		if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
			if idx := int(s[0] - '1'); idx < n {
				m.sendApprovalChoice(idx)
			}
		}
	}
	// Do not mutate locally — the server pushes the authoritative next
	// pending_approvals (empty clears the modal).
	return m, nil
}

// syncApprovalUI opens/closes the AskUserQuestion form based on the head of
// the pending_approvals array.
func (m *model) syncApprovalUI() tea.Cmd {
	m.approvalSel = 0 // reset the numbered-option cursor for the new head
	m.denyText = ""   // reset custom deny text
	if len(m.st.pendingApprovals) == 0 {
		m.question = nil
		return nil
	}
	head := m.st.pendingApprovals[0]
	if len(head.Questions) == 0 {
		m.question = nil
		return nil
	}
	if m.question != nil && m.question.id == head.ID {
		return nil
	}
	m.question = newQuestionForm(head)
	if m.question == nil {
		return nil
	}
	return m.question.form.Init()
}

func (m *model) handleQuestionKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "esc":
		id := m.question.id
		m.question = nil
		m.cli.Send(protocol.NewApprovalResponse(id, "deny"))
		return m, nil
	}
	cmd := m.question.form.Update(msg)
	m.finishQuestionIfDone()
	return m, cmd
}

func (m *model) finishQuestionIfDone() {
	if m.question == nil || m.question.form.State() != form.StateCompleted {
		return
	}
	qf := m.question
	m.question = nil
	m.cli.Send(qf.response())
}

// ── layout ──

func (m *model) turnActive() bool { return m.st.turn.Active() || m.question != nil }

func (m *model) autosize() {
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
	}
	m.layout()
}

func (m *model) extraHeight() int {
	h := 2 // input border
	for _, s := range []string{
		m.panelsAboveInput(), m.queueLines(), m.toastLines(), m.panelsProductStatus(),
		m.panelsPrimary(), m.panelsBelowPrimary(),
	} {
		if s != "" {
			h += lipgloss.Height(s)
		}
	}
	return h
}

func (m *model) layout() {
	if m.width == 0 {
		return
	}
	statusH := 1
	inputH := m.input.Height() + 1
	extraH := m.extraHeight()
	vpH := m.height - statusH - inputH - extraH
	if vpH < 3 {
		vpH = 3
	}
	m.list.SetSize(m.width, vpH)
	m.ready = m.width > 0
	m.input.SetWidth(m.width - 2)
}

// ── markdown renderer (resolved once, rebuilt on resize) ──

var glamourStyle = "dark"

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

// ── transcript line rendering ──

func (m *model) lineCacheKey(l *protocol.TranscriptLine) string {
	// Finished items get a stable key that never changes (they will be frozen).
	if l.Phase == "finished" {
		return fmt.Sprintf("FIN|%s|%s", l.Kind, l.ID)
	}
	ok := ""
	if l.ResultOk != nil {
		ok = fmt.Sprintf("%v", *l.ResultOk)
	}
	success := ""
	if l.Success != nil {
		success = fmt.Sprintf("%v", *l.Success)
	}
	sc := 0
	if l.Streaming != nil {
		sc = l.Streaming.TotalLineCount
	}
	// Include subagent count so subagent lines re-render when turn changes.
	subagentHash := len(m.st.turn.ActiveSubagents)
	return fmt.Sprintf("%s|%d|%d|%d|%s|%s|%s|%d|%v|%v|%v|%d|%d",
		l.Kind, len(l.Text), len(l.ResultText), len(l.Output),
		l.Phase, ok, success, sc, m.showReasoning, m.showToolOutput, m.isExecuting(l),
		subagentHash, len(m.st.turn.ExecutingToolCallIDs))
}

// isExecuting reports whether a tool_call line is currently running client-side
// (its id is in the turn's executing_tool_call_ids).
func (m *model) isExecuting(l *protocol.TranscriptLine) bool {
	if l.Kind != "tool_call" || len(m.st.turn.ExecutingToolCallIDs) == 0 {
		return false
	}
	for _, id := range m.st.turn.ExecutingToolCallIDs {
		if id == l.ID {
			return true
		}
	}
	return false
}

func (m *model) renderLine(l *protocol.TranscriptLine, w int) string {
	wrap := lipgloss.NewStyle().Width(w)
	switch l.Kind {
	case "user":
		// Subtle full-width background highlight: pad the line to width w so the
		// background extends the full row. Keep the "you ▸" rail + prefix.
		bg := styleUserBgLight
		if m.isDark {
			bg = styleUserBgDark
		}
		content := rail(theme.User) + styleUser.Render("you ▸ ") + l.Text
		return bg.Width(w).Render(content)

	case "assistant":
		var md string
		if l.Phase == "streaming" {
			s := m.streamCache[l.ID]
			if s == nil {
				s = &streamMD{}
				m.streamCache[l.ID] = s
			}
			md = m.renderStreamingMarkdown(s, l.Text, w-2)
		} else if r := m.markdownRenderer(w - 2); r != nil {
			if rendered, err := r.Render(l.Text); err == nil {
				md = strings.Trim(rendered, "\n")
			}
		}
		if md == "" {
			md = l.Text
		}
		header := ""
		if !l.IsContinuation {
			header = styleAgent.Render(iconAgent+" agent") + "\n"
		}
		return header + md

	case "reasoning":
		if m.showReasoning {
			header := ""
			if !l.IsContinuation {
				header = styleReasoning.Render("✻ Thinking…") + "\n"
			}
			return header + wrap.Render(styleReasoning.Render("  "+l.Text))
		}
		return wrap.Render(styleReasoning.Render(fmt.Sprintf(
			"✻ reasoning (%d chars — ctrl+r expands)", len(l.Text))))

	case "tool_call":
		return wrap.Render(m.renderToolCard(l, w))

	case "bash_command":
		var b strings.Builder
		b.WriteString(styleAccent.Render("$ " + compactOneLine(l.Input, w-4)))
		if body := m.renderShellOutput(l.Output, w); body != "" {
			b.WriteString("\n" + body)
		}
		return wrap.Render(b.String())

	case "command":
		var b strings.Builder
		b.WriteString(styleAccent.Render("⌁ " + compactOneLine(l.Input, w-4)))
		if strings.TrimSpace(l.Output) != "" {
			b.WriteString("\n" + styleToolBody.Render(l.Output))
		}
		return wrap.Render(b.String())

	case "event":
		txt := l.Summary
		if txt == "" {
			txt = l.EventType
		}
		return wrap.Render(styleInfo.Render("◦ " + txt))

	case "subagent":
		return wrap.Render(m.renderSubagentLine(l, w))

	case "status":
		return wrap.Render(styleInfo.Render(strings.Join(l.Lines, "\n")))

	case "trajectory_summary":
		return wrap.Render(styleInfo.Render(fmt.Sprintf(
			"✦ %s · %d steps · %.1fs", l.Verb, l.StepCount, l.DurationMs/1000)))

	case "separator":
		return styleInfo.Render(strings.Repeat("─", max(4, min(w, 80))))

	case "error":
		return wrap.Render(styleError.Render("✕ " + l.Text))

	default:
		if l.Text != "" {
			return wrap.Render(styleInfo.Render(l.Text))
		}
		return ""
	}
}

// renderShellOutput renders a truncated shell output body behind a rail.
func (m *model) renderShellOutput(out string, w int) string {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return ""
	}
	lines := strings.Split(out, "\n")
	maxLines := toolBodyCollapsedLines
	if m.showToolOutput {
		maxLines = toolBodyExpandedLines
	}
	shown := lines
	if len(lines) > maxLines {
		shown = lines[:maxLines]
	}
	var b strings.Builder
	bodyW := max(10, w-4)
	for _, ln := range shown {
		if lipgloss.Width(ln) > bodyW {
			ln = compactOneLine(ln, bodyW)
		}
		b.WriteString(styleToolBody.Render("  │ "+ln) + "\n")
	}
	if n := len(lines) - len(shown); n > 0 {
		hint := "ctrl+o expands"
		if m.showToolOutput {
			hint = "output capped"
		}
		b.WriteString(styleToolBody.Render(fmt.Sprintf("  │ … +%d lines (%s)", n, hint)))
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatK(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return strconv.Itoa(n)
}

// renderSubagentLine renders subagent activity from the transcript.
// The server pushes active_subagents in the turn frame; we render them
// as a tree with status indicators.
func (m *model) renderSubagentLine(l *protocol.TranscriptLine, w int) string {
	var b strings.Builder
	// Header: count and status
	n := len(m.st.turn.ActiveSubagents)
	if n == 0 {
		return ""
	}
	status := styleAccent.Render(fmt.Sprintf("⚙ Running %d agent%s", n, plural(n)))
	b.WriteString(status)
	// Per-agent details from the line's event data if available
	if l.EventData != nil {
		if agents, ok := l.EventData["agents"].([]any); ok {
			for i, ag := range agents {
				if am, ok := ag.(map[string]any); ok {
					branch := "├─"
					if i == len(agents)-1 {
						branch = "└─"
					}
					desc := str(am["description"])
					if desc == "" {
						desc = str(am["name"])
					}
					typ := str(am["type"])
					model := str(am["model"])
					stats := ""
					if steps, ok := am["steps"].(float64); ok && steps > 0 {
						stats = fmt.Sprintf(" · %.0f steps", steps)
					}
					line := fmt.Sprintf("%s %s", branch, desc)
					if typ != "" {
						line += styleOverlayDim.Render(fmt.Sprintf(" · %s", typ))
					}
					if model != "" {
						line += styleOverlayDim.Render(fmt.Sprintf(" · %s", model))
					}
					line += styleOverlayDim.Render(stats)
					b.WriteString("\n" + compactOneLine(line, w-4))
				}
			}
		}
	}
	return b.String()
}

// ── statusline & activity ──

func turnVerb(t protocol.TurnState) string {
	// Prefer the native ExecutionPhase (distinct spinner text per phase); the
	// rotating thinking_message enriches "thinking".
	switch t.Phase {
	case "requesting":
		return "requesting"
	case "thinking":
		if t.ThinkingMessage != "" {
			return strings.TrimPrefix(t.ThinkingMessage, "is ") // "processing", "thinking"…
		}
		return "thinking"
	case "toolUse":
		return "running tool"
	case "responding":
		return "responding"
	}
	switch t.Status {
	case "sending":
		return "sending"
	case "thinking":
		return "thinking"
	case "streaming":
		return "responding"
	case "executing_tool":
		return "running tool"
	case "waiting_on_approval":
		return "waiting on you"
	case "cancelling":
		return "cancelling"
	default:
		if t.StopReason == "error" {
			return "error"
		}
		return "ready"
	}
}

func (m *model) statusline() string {
	// A transient hint (mode/queue change) preempts the left column for its TTL.
	if h := m.st.statusHint; h != nil {
		style := styleInfo
		switch h.level {
		case "error":
			style = styleError
		case "warning":
			style = lipgloss.NewStyle().Foreground(theme.Warn)
		}
		return style.Render("• " + h.text)
	}
	status := turnVerb(m.st.turn)
	// Native NetworkPhase indicator (upload/download/error).
	switch m.st.turn.Network {
	case "upload":
		status = "↑ " + status
	case "download":
		status = "↓ " + status
	case "error":
		status = "⚠ " + status
	}
	if n := len(m.st.turn.ActiveSubagents); n > 0 {
		status += fmt.Sprintf(" · %d subagent%s", n, plural(n))
	}
	if m.turnActive() {
		status = m.spin.View() + " " + styleAccent.Render(status)
	} else {
		status = styleInfo.Render(status)
	}
	if m.disconnected {
		status = styleError.Render("✕ disconnected")
	}

	modeText := string(m.mode)
	if modeText == "" {
		modeText = "?"
	}
	pill := styleModePill.Background(modeColor(m.mode)).Render(modeText)

	agent := m.agentName
	if agent == "" {
		agent = shortID(m.st.convID)
	}
	conv := m.convTitle
	if conv == "" {
		conv = shortID(m.st.convID)
	}
	conv = compactOneLine(conv, 32)

	left := pill + styleInfo.Render(fmt.Sprintf(" %s · %s ", agent, conv))
	if md := shortModel(m.modelHandle); md != "" {
		left += styleAccent.Render("◇ " + md)
		// BYOK indicator: ▲ (green for OpenAI Codex, yellow otherwise).
		if byok, codex := isBYOK(m.modelHandle); byok {
			if codex {
				left += " " + styleBYOKOpenAI.Render("▲")
			} else {
				left += " " + styleBYOKOther.Render("▲")
			}
		}
		left += " "
	}
	left += status
	if m.st.device != nil && m.st.device.Usage != nil {
		u := m.st.device.Usage
		// Persistent context-window occupancy when known; else session tokens.
		if u.ContextTokens > 0 && u.ContextWindow > 0 {
			pct := u.ContextTokens * 100 / u.ContextWindow
			left += styleInfo.Render(fmt.Sprintf(" · %s/%s (%d%%)", formatK(u.ContextTokens), formatK(u.ContextWindow), pct))
		} else {
			left += styleInfo.Render(fmt.Sprintf(" · %s", formatK(u.TotalTokens)))
		}
	}

	right := styleInfo.Render("esc interrupt · shift+tab mode · ^g help ")
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

func shortModel(handle string) string {
	if i := strings.LastIndex(handle, "/"); i >= 0 {
		return handle[i+1:]
	}
	return handle
}

// panelLinesInRange collects mod-panel lines whose order matches pred, sorted
// so the highest order renders first (native: highest panel at the top).
func (m *model) panelLinesInRange(pred func(order int) bool) string {
	type pnl struct {
		order int
		lines []string
	}
	var sel []pnl
	for _, p := range m.st.modPanels {
		if pred(p.Order) && len(p.Lines) > 0 {
			sel = append(sel, pnl{p.Order, p.Lines})
		}
	}
	sort.SliceStable(sel, func(i, j int) bool { return sel[i].order > sel[j].order })
	var lines []string
	for _, p := range sel {
		lines = append(lines, p.lines...)
	}
	return strings.Join(lines, "\n")
}

// Native openPanel placement (deprecated-api.ts:11 / types.ts:485):
//
//	order > 1  → additive panels ABOVE the input (highest at top)
//	order == 1 → replaces the product-status row (just under the input)
//	order == 0 → the primary line (overrides the built-in agent·model statusline)
//	order < 0  → stacks BELOW the primary line
func (m *model) panelsAboveInput() string    { return m.panelLinesInRange(func(o int) bool { return o > 1 }) }
func (m *model) panelsProductStatus() string { return m.panelLinesInRange(func(o int) bool { return o == 1 }) }
func (m *model) panelsPrimary() string       { return m.panelLinesInRange(func(o int) bool { return o == 0 }) }
func (m *model) panelsBelowPrimary() string  { return m.panelLinesInRange(func(o int) bool { return o < 0 }) }

// queueLines renders queued messages (submitted mid-turn) above the input.
func (m *model) queueLines() string {
	if len(m.st.queue) == 0 {
		return ""
	}
	label := "queued · press ↑ to edit"
	bullet := ">"
	if m.st.queueDeferred {
		label = "queued · defer (ctrl+d releases)"
		bullet = "○"
	}
	var lines []string
	lines = append(lines, styleInfo.Render(fmt.Sprintf("⋯ %d %s", len(m.st.queue), label)))
	for _, q := range m.st.queue {
		lines = append(lines, styleInfo.Render("  "+bullet+" "+compactOneLine(q.Text, max(20, m.width-6))))
	}
	return strings.Join(lines, "\n")
}

// toastLines renders transient toasts (separate from mod panels) above the input.
func (m *model) toastLines() string {
	var lines []string
	for _, t := range m.st.toasts {
		style := styleInfo
		switch t.level {
		case "error":
			style = styleError
		case "warning":
			style = lipgloss.NewStyle().Foreground(theme.Warn)
		}
		lines = append(lines, style.Render("• "+compactOneLine(t.text, max(20, m.width-4))))
	}
	return strings.Join(lines, "\n")
}

// ── View ──

func (m *model) View() tea.View {
	v := tea.NewView(m.viewContent())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = true // for focus-aware turn-complete notifications
	title := "zc"
	if m.agentName != "" {
		title += " — " + m.agentName
		if conv := m.convTitle; conv != "" {
			title += " · " + compactOneLine(conv, 40)
		}
	}
	v.WindowTitle = title
	return v
}

func (m *model) viewContent() string {
	if m.quitting {
		return ""
	}

	// Lobby / starting.
	if m.st.phase != "chat" {
		var body string
		switch {
		case m.startupErr != "":
			body = styleError.Render("startup failed: "+m.startupErr) +
				styleInfo.Render("\n(server log: "+m.logPath+")\n\npress ctrl+c to exit")
		case m.disconnected:
			body = styleError.Render("ui-server exited") +
				styleInfo.Render("\n(server log: "+m.logPath+")\n\npress ctrl+c to exit")
		case m.selection != nil:
			body = m.selection.render(max(m.width-4, 32), 18)
		default:
			logo := lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render("ZeptoCode")
			lines := []string{
				logo,
				"",
				styleInfo.Render("a thin-client TUI for Letta Code"),
			}
			if ver := m.st.lettaVersion; ver != "" {
				lines = append(lines, styleAccent.Render("Letta Code "+ver))
			}
			if b := m.st.runtimeBuild; b != "" {
				lines = append(lines, styleInfo.Render("runtime "+b))
			}
			lines = append(lines, "", styleAccent.Render("select an agent to begin"))
			body = lipgloss.JoinVertical(lipgloss.Center, lines...)
		}
		if m.width > 0 && m.height > 0 {
			return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body)
		}
		return body
	}

	if !m.ready {
		return "loading…"
	}

	parts := []string{m.list.View()}
	// Panels with order > 1 render above the input; transient toasts too.
	if above := m.panelsAboveInput(); above != "" {
		parts = append(parts, above)
	}
	if ql := m.queueLines(); ql != "" {
		parts = append(parts, ql)
	}
	if tl := m.toastLines(); tl != "" {
		parts = append(parts, tl)
	}
	inputBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(modeColor(m.mode)).
		Width(m.width - 2).
		Render(m.input.View())
	parts = append(parts, inputBox)
	// order == 1 replaces the product-status row (just under the input).
	if ps := m.panelsProductStatus(); ps != "" {
		parts = append(parts, ps)
	}
	// order == 0 overrides the built-in agent·model statusline; else default.
	if prim := m.panelsPrimary(); prim != "" {
		parts = append(parts, prim)
	} else {
		parts = append(parts, m.statusline())
	}
	// order < 0 stacks below the primary line.
	if below := m.panelsBelowPrimary(); below != "" {
		parts = append(parts, below)
	}
	base := strings.Join(parts, "\n")

	// "@" completion popup: a floating list anchored just above the input,
	// rather than a centered modal (it accompanies live typing).
	if m.complete != nil {
		w := min(m.width-4, 72)
		if w < 20 {
			w = 20
		}
		box := m.complete.render(w, 12)
		y := m.height - m.extraHeight() - 1 - lipgloss.Height(box)
		return compositeAt(base, box, 2, y)
	}

	if box := m.activeDialog(); box != "" {
		return compositeDialog(base, box, m.width, m.height)
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

func compactOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		if max < 1 {
			max = 1
		}
		return s[:max] + "…"
	}
	return s
}

// ── main ──

func main() {
	agentID := flag.String("agent", "", "agent id (optional; omit for the lobby picker)")
	conversationID := flag.String("conversation", "", "conversation id to resume, or \"new\"")
	uiBin := flag.String("ui-bin", defaultUIBin(), "command to spawn the ui-server")
	uiServer := flag.String("ui-server", defaultUIServerPath(), "path to ui-server.ts in the letta-code fork")
	flag.Parse()

	logDir := filepath.Join(os.Getenv("HOME"), ".letta", "logs")
	os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, "zc-ui-server.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open server log: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	// Resolve the terminal background ONCE, pre-tea, so no adaptive-color
	// query races the input stream mid-session.
	isDark := lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
	if !isDark {
		glamourStyle = "light"
	}
	initTheme(isDark)

	opts := client.Options{
		UIBin:          *uiBin,
		UIServerPath:   *uiServer,
		Agent:          *agentID,
		ConversationID: *conversationID,
		ServerLog:      logFile,
	}
	cli, err := client.Spawn(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spawn ui-server: %v\n", err)
		os.Exit(1)
	}

	m := newModel(cli, logPath, opts)
	m.isDark = isDark
	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tui error: %v\n", err)
	}
	cli.Close()
}

func defaultUIBin() string {
	if b := os.Getenv("ZC_UI_BIN"); b != "" {
		return b
	}
	// The pixi-provided bun; falls back to PATH lookup by name.
	if home := os.Getenv("HOME"); home != "" {
		cand := filepath.Join(home, ".pixi", "envs", "nodejs", "bin", "bun")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return "bun"
}

func defaultUIServerPath() string {
	if p := os.Getenv("ZC_UI_SERVER"); p != "" {
		return p
	}
	return os.ExpandEnv("$HOME/projects/letta-code/src/cli/subcommands/ui-server.ts")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
