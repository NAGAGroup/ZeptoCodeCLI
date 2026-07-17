// Package protocol mirrors the zc UI wire contract 1:1 from the letta-code
// fork's src/zc/protocol.ts (SPEC draft 5, PROTOCOL_VERSION 2). The server
// pushes JSON state frames; the client is a reducer (state, frame) -> state.
// Client -> server is a small set of fire-and-forget events.
//
// Every JSON field name here matches protocol.ts (and, for TranscriptLine,
// the native accumulator `Line` union in src/cli/helpers/accumulator.ts,
// which is camelCase because it is serialized verbatim). Go has no unions,
// so TranscriptLine is one struct keyed by Kind with every field omitempty.
package protocol

import (
	"encoding/json"
	"fmt"
)

// PROTOCOL_VERSION is the wire version this client speaks. It matches
// protocol.ts `PROTOCOL_VERSION`.
const ProtocolVersion = 2

// PermissionMode is the enforced-below-the-seam permission mode carried by
// the device frame and set by change_mode.
type PermissionMode string

const (
	ModeStandard     PermissionMode = "standard"
	ModeAcceptEdits  PermissionMode = "acceptEdits"
	ModeUnrestricted PermissionMode = "unrestricted"
)

// ─────────────────────────────────────────────────────────────────────
// Server → client frames
// ─────────────────────────────────────────────────────────────────────

// HelloResponse answers `hello`. A non-empty Error is a fatal startup
// failure; the process exits after.
type HelloResponse struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
	RuntimeBuild    string `json:"runtime_build"`
	LettaCodeVer    string `json:"letta_code_version"`
	Error           string `json:"error,omitempty"`
}

// SessionPhase drives the lobby → chat machine.
type SessionPhase struct {
	Type           string `json:"type"`
	Phase          string `json:"phase"` // "lobby" | "starting" | "chat"
	AgentID        string `json:"agent_id,omitempty"`
	AgentName      string `json:"agent_name,omitempty"` // string | null → "" when null
	ConversationID string `json:"conversation_id,omitempty"`
}

// DeviceModel is the current model triple (all fields nullable in TS).
type DeviceModel struct {
	ID     string `json:"id,omitempty"`
	Handle string `json:"handle,omitempty"`
	Label  string `json:"label,omitempty"`
}

// DeviceAgent is the ambient agent identity for the statusline/header.
type DeviceAgent struct {
	ID              string `json:"id"`
	Name            string `json:"name,omitempty"` // string | null
	MemfsEnabled    bool   `json:"memfs_enabled"`
	MemoryDirectory string `json:"memory_directory,omitempty"`
}

// DeviceConversation is the ambient conversation identity.
type DeviceConversation struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

// DeviceUsage carries token/cost counters for the statusline.
type DeviceUsage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
}

// DeviceState is ambient status the statusline/header renders from.
type DeviceState struct {
	Type           string             `json:"type"`
	CWD            string             `json:"cwd"`
	PermissionMode PermissionMode     `json:"permission_mode"`
	Model          DeviceModel        `json:"model"`
	Agent          DeviceAgent        `json:"agent"`
	Conversation   DeviceConversation `json:"conversation"`
	Toolset        []string           `json:"toolset"`
	LettaCodeVer   string             `json:"letta_code_version"`
	ShouldDoctor   bool               `json:"should_doctor,omitempty"`
	Usage          *DeviceUsage       `json:"usage,omitempty"`
}

// Transcript carries the whole transcript as the native Line[] every push.
// The client REPLACES its transcript state with Lines wholesale (never
// merge/append).
type Transcript struct {
	Type           string           `json:"type"`
	ConversationID string           `json:"conversation_id"`
	Lines          []TranscriptLine `json:"lines"`
}

// TurnState drives spinner, input gating, ctrl+c arming, title blink.
type TurnState struct {
	Type       string `json:"type"`
	Status     string `json:"status"` // idle|sending|thinking|streaming|executing_tool|waiting_on_approval|cancelling
	StopReason string `json:"stop_reason,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Active reports whether the turn is non-idle (spinner / interrupt armed).
func (t TurnState) Active() bool {
	return t.Status != "" && t.Status != "idle"
}

// ApprovalQuestion is an AskUserQuestion payload carried by an approval.
type ApprovalQuestion struct {
	ID      string   `json:"id"`
	Prompt  string   `json:"prompt"`
	Options []string `json:"options,omitempty"`
	Multi   bool     `json:"multi,omitempty"`
}

// PermissionSuggestion is a one-tap permission grant option.
type PermissionSuggestion struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// PendingApproval is a single outstanding approval request.
type PendingApproval struct {
	ID                    string                 `json:"id"` // tool_call_id
	ToolName              string                 `json:"tool_name"`
	Input                 map[string]any         `json:"input"`
	Diffs                 json.RawMessage        `json:"diffs,omitempty"`
	PermissionSuggestions []PermissionSuggestion `json:"permission_suggestions,omitempty"`
	BlockedPath           string                 `json:"blocked_path,omitempty"`
	Questions             []ApprovalQuestion     `json:"questions,omitempty"`
}

// PendingApprovals is the approval-dialog state; non-empty renders the modal.
type PendingApprovals struct {
	Type  string            `json:"type"`
	Items []PendingApproval `json:"items"`
}

// SelectionOption is one choice in a server-initiated selection.
type SelectionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────
// Tagged selection (SPEC R23) — stateless: the server returns options in
// response to a query or a picker command; the client renders + picks; the
// client sends selection_choice{tag,...}; the server dispatches on `tag`.
// Mirrors protocol.ts SelectionItem/SelectionGroup/SelectionQuestion/Selection.
// ─────────────────────────────────────────────────────────────────────

// SelectionItem is one pickable row in a tagged selection.
type SelectionItem struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Badge       string `json:"badge,omitempty"` // e.g. "local", "no key", "★"
	Disabled    bool   `json:"disabled,omitempty"`
}

// SelectionGroup is a labeled cluster of selection items ("Pinned", "Recent").
type SelectionGroup struct {
	Label string          `json:"label,omitempty"`
	Items []SelectionItem `json:"items"`
}

// SelectionQuestion is one prompt in a multi-question selection
// (AskUserQuestion). Absent Options ⇒ free-text.
type SelectionQuestion struct {
	ID      string          `json:"id"`
	Prompt  string          `json:"prompt"`
	Options []SelectionItem `json:"options,omitempty"`
	Multi   bool            `json:"multi,omitempty"`
}

// Selection is a stateless tagged picker (grouped items and/or multi-question).
// The server dispatches the returned choice on Tag.
type Selection struct {
	Type      string              `json:"type"`
	Tag       string              `json:"tag"`
	Title     string              `json:"title,omitempty"`
	Multi     bool                `json:"multi,omitempty"`
	Groups    []SelectionGroup    `json:"groups,omitempty"`
	Questions []SelectionQuestion `json:"questions,omitempty"`
}

// ModPanel is one evaluated mod panel's rendered lines.
type ModPanel struct {
	ID    string   `json:"id"`
	Owner string   `json:"owner"`
	Order int      `json:"order"`
	Lines []string `json:"lines"`
}

// ModPanels carries client-polled mod panel lines.
type ModPanels struct {
	Type   string     `json:"type"`
	Panels []ModPanel `json:"panels"`
}

// AgentItem is one row in the lobby / agent picker.
type AgentItem struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"` // string | null
	Model  string `json:"model,omitempty"`
	Pinned bool   `json:"pinned,omitempty"`
}

// Agents is the agent list for the lobby.
type Agents struct {
	Type  string      `json:"type"`
	Items []AgentItem `json:"items"`
}

// ConversationItem is one row in the conversation picker.
type ConversationItem struct {
	ID            string `json:"id"`
	Title         string `json:"title,omitempty"`
	LastMessageAt string `json:"last_message_at,omitempty"`
	Archived      bool   `json:"archived,omitempty"`
	Hidden        bool   `json:"hidden,omitempty"`
}

// Conversations is the conversation list for a given agent.
type Conversations struct {
	Type    string             `json:"type"`
	AgentID string             `json:"agent_id"`
	Items   []ConversationItem `json:"items"`
}

// ModelItem is one entry in the structured model catalog.
type ModelItem struct {
	ID         string `json:"id"`
	Handle     string `json:"handle"`
	Label      string `json:"label,omitempty"`
	Reasoning  string `json:"reasoning,omitempty"`
	KeyMissing bool   `json:"key_missing,omitempty"`
}

// Models is the model catalog.
type Models struct {
	Type  string      `json:"type"`
	Items []ModelItem `json:"items"`
}

// CommandInfo is one slash-command / palette entry.
type CommandInfo struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	ArgsHint    string `json:"args_hint,omitempty"`
	Routing     string `json:"routing,omitempty"`
	Source      string `json:"source"`
	Namespace   string `json:"namespace,omitempty"`
	Hidden      bool   `json:"hidden,omitempty"`
}

// CommandCatalog is the slash-command / palette catalog.
type CommandCatalog struct {
	Type     string        `json:"type"`
	Commands []CommandInfo `json:"commands"`
}

// Toast is a transient client message.
type Toast struct {
	Type    string `json:"type"`
	Level   string `json:"level"` // info | warning | error
	Message string `json:"message"`
}

// Notification is a terminal-notification trigger.
type Notification struct {
	Type    string `json:"type"`
	Level   string `json:"level"`
	Message string `json:"message"`
	Reason  string `json:"reason"` // awaiting_approval|awaiting_input|turn_complete|hook|other
}

// ErrorFrame is a generic non-fatal error frame.
type ErrorFrame struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	RunID   string `json:"run_id,omitempty"`
}

// ChangeSignal is a "this slice is stale, re-query if showing it" push.
type ChangeSignal struct {
	Type string `json:"type"`
}

// Unknown is any frame whose `type` we don't model. Decode never fails on it.
type Unknown struct {
	Type string
	Raw  json.RawMessage
}

// ─────────────────────────────────────────────────────────────────────
// TranscriptLine: the union of every accumulator Line kind (SPEC §4).
// Field names are the native camelCase; render by Kind.
// ─────────────────────────────────────────────────────────────────────

// StreamingLine is one buffered output line (shell tool streaming).
type StreamingLine struct {
	Text     string `json:"text"`
	IsStderr bool   `json:"isStderr"`
}

// StreamingState is the rolling tail buffer for a streaming shell tool.
type StreamingState struct {
	TailLines       []StreamingLine `json:"tailLines"`
	PartialLine     string          `json:"partialLine"`
	PartialIsStderr bool            `json:"partialIsStderr"`
	TotalLineCount  int             `json:"totalLineCount"`
	StartTime       float64         `json:"startTime"`
}

// EventStats is the compaction-event stats block.
type EventStats struct {
	Trigger             string `json:"trigger,omitempty"`
	ContextTokensBefore int    `json:"contextTokensBefore,omitempty"`
	ContextTokensAfter  int    `json:"contextTokensAfter,omitempty"`
	ContextWindow       int    `json:"contextWindow,omitempty"`
	MessagesCountBefore int    `json:"messagesCountBefore,omitempty"`
	MessagesCountAfter  int    `json:"messagesCountAfter,omitempty"`
}

// TranscriptLine mirrors the accumulator `Line` union. Kind selects which
// fields are meaningful. All fields are omitempty so marshaling round-trips.
type TranscriptLine struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`

	// user | reasoning | assistant | error
	Text           string `json:"text,omitempty"`
	MessageID      string `json:"messageId,omitempty"`
	Otid           string `json:"otid,omitempty"`
	Phase          string `json:"phase,omitempty"`
	IsContinuation bool   `json:"isContinuation,omitempty"`

	// tool_call
	ToolCallID                string          `json:"toolCallId,omitempty"`
	Name                      string          `json:"name,omitempty"`
	ArgsText                  string          `json:"argsText,omitempty"`
	UnifiedExecCommandDisplay string          `json:"unifiedExecCommandDisplay,omitempty"`
	ResultText                string          `json:"resultText,omitempty"`
	ResultOk                  *bool           `json:"resultOk,omitempty"`
	Streaming                 *StreamingState `json:"streaming,omitempty"`

	// event
	EventType string         `json:"eventType,omitempty"`
	EventData map[string]any `json:"eventData,omitempty"`
	Summary   string         `json:"summary,omitempty"`
	Stats     *EventStats    `json:"stats,omitempty"`

	// command | bash_command
	Input        string `json:"input,omitempty"`
	Output       string `json:"output,omitempty"`
	Success      *bool  `json:"success,omitempty"`
	DimOutput    bool   `json:"dimOutput,omitempty"`
	Preformatted bool   `json:"preformatted,omitempty"`

	// status
	Lines []string `json:"lines,omitempty"`

	// trajectory_summary
	DurationMs float64 `json:"durationMs,omitempty"`
	StepCount  int     `json:"stepCount,omitempty"`
	Verb       string  `json:"verb,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────
// Decode: envelope-sniff on `type`, tolerant (unknown → Unknown, never fatal)
// ─────────────────────────────────────────────────────────────────────

// Decode parses one JSON-lines frame into its typed Go value. Unknown types
// decode to *Unknown so the read loop never drops or dies on a frame it
// doesn't model. A JSON syntax error is the only returned error.
func Decode(line []byte) (any, error) {
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, fmt.Errorf("protocol: decode envelope: %w", err)
	}

	var out any
	switch env.Type {
	case "hello_response":
		out = new(HelloResponse)
	case "session_phase":
		out = new(SessionPhase)
	case "device":
		out = new(DeviceState)
	case "transcript":
		out = new(Transcript)
	case "turn":
		out = new(TurnState)
	case "pending_approvals":
		out = new(PendingApprovals)
	case "selection":
		out = new(Selection)
	case "mod_panels":
		out = new(ModPanels)
	case "agents":
		out = new(Agents)
	case "conversations":
		out = new(Conversations)
	case "models":
		out = new(Models)
	case "command_catalog":
		out = new(CommandCatalog)
	case "toast":
		out = new(Toast)
	case "notification":
		out = new(Notification)
	case "error":
		out = new(ErrorFrame)
	case "settings_updated", "mods_updated", "memory_updated",
		"skills_updated", "crons_updated", "conversations_updated":
		return &ChangeSignal{Type: env.Type}, nil
	default:
		raw := make(json.RawMessage, len(line))
		copy(raw, line)
		return &Unknown{Type: env.Type, Raw: raw}, nil
	}

	if err := json.Unmarshal(line, out); err != nil {
		// A structurally-broken but type-known frame still shouldn't kill
		// the loop; surface it as Unknown.
		raw := make(json.RawMessage, len(line))
		copy(raw, line)
		return &Unknown{Type: env.Type, Raw: raw}, nil
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────
// Client → server events. Each carries its own `type`; constructors set it
// so callers can't forget. Client.Send marshals any of these to one line.
// ─────────────────────────────────────────────────────────────────────

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Hello is the handshake event.
type Hello struct {
	Type            string     `json:"type"`
	ProtocolVersion int        `json:"protocol_version"`
	Client          ClientInfo `json:"client"`
}

func NewHello(name, version string) Hello {
	return Hello{Type: "hello", ProtocolVersion: ProtocolVersion, Client: ClientInfo{Name: name, Version: version}}
}

// SelectAgent picks an agent in the lobby.
type SelectAgent struct {
	Type    string `json:"type"`
	AgentID string `json:"agent_id"`
}

func NewSelectAgent(agentID string) SelectAgent {
	return SelectAgent{Type: "select_agent", AgentID: agentID}
}

// SelectConversation picks (or creates, id="new") a conversation.
type SelectConversation struct {
	Type           string `json:"type"`
	ConversationID string `json:"conversation_id"`
}

func NewSelectConversation(conversationID string) SelectConversation {
	return SelectConversation{Type: "select_conversation", ConversationID: conversationID}
}

// Attachment is a submitted image reference.
type Attachment struct {
	ImageRef string `json:"image_ref"`
}

// InputSubmit is the submit event — raw text; the server routes it.
type InputSubmit struct {
	Type        string       `json:"type"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

func NewInputSubmit(text string) InputSubmit {
	return InputSubmit{Type: "input_submit", Text: text}
}

// InputPaste sends pasted multi-line content as a user message.
type InputPaste struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func NewInputPaste(text string) InputPaste {
	return InputPaste{Type: "input_paste", Text: text}
}

// ApprovalResponse answers a pending_approvals item.
type ApprovalResponse struct {
	Type                  string         `json:"type"`
	ID                    string         `json:"id"` // tool_call_id
	Decision              string         `json:"decision"`
	Message               string         `json:"message,omitempty"`
	UpdatedInput          map[string]any     `json:"updated_input,omitempty"`
	SelectedSuggestionIDs []string           `json:"selected_permission_suggestion_ids,omitempty"`
	Answers               map[string]string  `json:"answers,omitempty"` // AskUserQuestion: question id -> answer
}

func NewApprovalResponse(id, decision string) ApprovalResponse {
	return ApprovalResponse{Type: "approval_response", ID: id, Decision: decision}
}

// Interrupt aborts the active turn (esc).
type Interrupt struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
}

func NewInterrupt() Interrupt { return Interrupt{Type: "interrupt"} }

// ChangeMode sets the permission mode (shift+tab / /cd).
type ChangeMode struct {
	Type           string         `json:"type"`
	PermissionMode PermissionMode `json:"permission_mode"`
}

func NewChangeMode(mode PermissionMode) ChangeMode {
	return ChangeMode{Type: "change_mode", PermissionMode: mode}
}

// SessionEvent reports lifecycle events (conversation_open/close, session_start/end).
type SessionEvent struct {
	Type       string `json:"type"`
	Event      string `json:"event"`
	DurationMs int    `json:"duration_ms,omitempty"`
}

func NewSessionEvent(event string) SessionEvent {
	return SessionEvent{Type: "session_event", Event: event}
}

// ChangeCwd sets the working directory.
type ChangeCwd struct {
	Type string `json:"type"`
	CWD  string `json:"cwd"`
}

func NewChangeCwd(cwd string) ChangeCwd {
	return ChangeCwd{Type: "change_cwd", CWD: cwd}
}

// QueueDeferSet toggles whether queued messages defer until end_turn.
type QueueDeferSet struct {
	Type     string `json:"type"`
	Deferred bool   `json:"deferred"`
}

func NewQueueDeferSet(deferred bool) QueueDeferSet {
	return QueueDeferSet{Type: "queue_defer_set", Deferred: deferred}
}

// ExecuteCommand runs a command explicitly. CommandID is the catalog id with
// its leading "/" stripped (e.g. "model", "jobs").
type ExecuteCommand struct {
	Type      string `json:"type"`
	CommandID string `json:"command_id"`
	Args      string `json:"args,omitempty"`
}

func NewExecuteCommand(commandID string) ExecuteCommand {
	return ExecuteCommand{Type: "execute_command", CommandID: commandID}
}

// AgentsQuery asks the server to push the agents list.
type AgentsQuery struct {
	Type string `json:"type"`
}

func NewAgentsQuery() AgentsQuery {
	return AgentsQuery{Type: "agents_query"}
}

// ConversationsQuery asks the server to push conversations for an agent.
type ConversationsQuery struct {
	Type    string `json:"type"`
	AgentID string `json:"agent_id,omitempty"`
}

func NewConversationsQuery(agentID string) ConversationsQuery {
	return ConversationsQuery{Type: "conversations_query", AgentID: agentID}
}

// SelectionChoice is the committed result of a tagged selection (SPEC R23).
// The Tag (echoed from the Selection) tells the server how to dispatch.
type SelectionChoice struct {
	Type    string            `json:"type"`
	Tag     string            `json:"tag"`
	Choice  string            `json:"choice,omitempty"`  // single-select
	Choices []string          `json:"choices,omitempty"` // multi-select
	Answers map[string]string `json:"answers,omitempty"` // multi-question
}

func NewSelectionChoice(tag, choice string) SelectionChoice {
	return SelectionChoice{Type: "selection_choice", Tag: tag, Choice: choice}
}

// NewSelectionChoices builds a multi-select result.
func NewSelectionChoices(tag string, choices []string) SelectionChoice {
	return SelectionChoice{Type: "selection_choice", Tag: tag, Choices: choices}
}

// NewSelectionAnswers builds a multi-question result.
func NewSelectionAnswers(tag string, answers map[string]string) SelectionChoice {
	return SelectionChoice{Type: "selection_choice", Tag: tag, Answers: answers}
}

// ModPanelsQuery polls evaluated panel lines at a width.
type ModPanelsQuery struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	Width     int    `json:"width"`
}

func NewModPanelsQuery(width int) ModPanelsQuery {
	return ModPanelsQuery{Type: "mod_panels_query", Width: width}
}

// Query is a fire-and-forget slice refresh.
type Query struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
}

func NewQuery(kind string) Query { return Query{Type: kind} }
