// Package protocol implements the interactive-conversation subset of the
// Letta Code app-server WebSocket protocol (protocol_v2).
//
// Source of truth: @letta-ai/letta-code/app-server-protocol (shipped .d.ts,
// a 1:1 re-export of src/types/protocol_v2.ts). There is no wire-level
// version negotiation; this package is written against letta-code 0.28.x.
// On upstream bumps, diff the shipped .d.ts between npm versions for an
// exact changelog.
//
// Decoding is deliberately tolerant: unknown frame types and unknown fields
// are preserved or skipped, never fatal.
package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ── Shared scopes & enums ──

type RuntimeScope struct {
	AgentID        string `json:"agent_id"`
	ConversationID string `json:"conversation_id"`
	ActingUserID   string `json:"acting_user_id,omitempty"`
}

type PermissionMode string

const (
	ModeStandard     PermissionMode = "standard"
	ModeAcceptEdits  PermissionMode = "acceptEdits"
	ModeUnrestricted PermissionMode = "unrestricted"
)

// LoopStatus values (LoopStatus union in protocol_v2).
const (
	LoopSendingAPIRequest   = "SENDING_API_REQUEST"
	LoopWaitingForResponse  = "WAITING_FOR_API_RESPONSE"
	LoopRetryingAPIRequest  = "RETRYING_API_REQUEST"
	LoopProcessingResponse  = "PROCESSING_API_RESPONSE"
	LoopExecutingClientTool = "EXECUTING_CLIENT_SIDE_TOOL"
	LoopExecutingCommand    = "EXECUTING_COMMAND"
	LoopWaitingOnApproval   = "WAITING_ON_APPROVAL"
	LoopWaitingOnInput      = "WAITING_ON_INPUT"
)

type LoopState struct {
	Status               string   `json:"status"`
	ActiveRunIDs         []string `json:"active_run_ids"`
	ExecutingToolCallIDs []string `json:"executing_tool_call_ids"`
}

// ── Outbound commands (client → app-server, control channel) ──

type ClientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

type CreateConversationOptions struct {
	Body map[string]any `json:"body"`
}

type RuntimeStartCommand struct {
	Type               string                     `json:"type"` // "runtime_start"
	RequestID          string                     `json:"request_id"`
	AgentID            string                     `json:"agent_id,omitempty"`
	ConversationID     string                     `json:"conversation_id,omitempty"`
	CreateConversation *CreateConversationOptions `json:"create_conversation,omitempty"`
	Mode               PermissionMode             `json:"mode,omitempty"`
	ClientInfo         *ClientInfo                `json:"client_info,omitempty"`
}

type MessageCreate struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type InputCreateMessage struct {
	Type    string       `json:"type"` // "input"
	Runtime RuntimeScope `json:"runtime"`
	Payload struct {
		Kind     string          `json:"kind"` // "create_message"
		Messages []MessageCreate `json:"messages"`
	} `json:"payload"`
}

func NewUserMessage(rt RuntimeScope, text string) InputCreateMessage {
	cmd := InputCreateMessage{Type: "input", Runtime: rt}
	cmd.Payload.Kind = "create_message"
	cmd.Payload.Messages = []MessageCreate{{Role: "user", Content: text}}
	return cmd
}

// ApprovalDecision is either allow (optional updated input / persisted
// permission suggestions) or deny (message required by the protocol).
type ApprovalDecision struct {
	Behavior                        string         `json:"behavior"` // "allow" | "deny"
	Message                         string         `json:"message,omitempty"`
	UpdatedInput                    map[string]any `json:"updated_input,omitempty"`
	SelectedPermissionSuggestionIDs []string       `json:"selected_permission_suggestion_ids,omitempty"`
}

type InputApprovalResponse struct {
	Type    string       `json:"type"` // "input"
	Runtime RuntimeScope `json:"runtime"`
	Payload struct {
		Kind      string           `json:"kind"` // "approval_response"
		RequestID string           `json:"request_id"`
		Decision  ApprovalDecision `json:"decision"`
	} `json:"payload"`
}

func NewApprovalResponse(rt RuntimeScope, requestID string, decision ApprovalDecision) InputApprovalResponse {
	cmd := InputApprovalResponse{Type: "input", Runtime: rt}
	cmd.Payload.Kind = "approval_response"
	cmd.Payload.RequestID = requestID
	cmd.Payload.Decision = decision
	return cmd
}

type AbortMessageCommand struct {
	Type      string       `json:"type"` // "abort_message"
	Runtime   RuntimeScope `json:"runtime"`
	RequestID string       `json:"request_id,omitempty"`
}

type AgentListCommand struct {
	Type      string `json:"type"` // "agent_list"
	RequestID string `json:"request_id"`
}

type ConversationMessagesListCommand struct {
	Type           string         `json:"type"` // "conversation_messages_list"
	RequestID      string         `json:"request_id"`
	ConversationID string         `json:"conversation_id"`
	Query          map[string]any `json:"query,omitempty"`
}

type ConversationListCommand struct {
	Type      string         `json:"type"` // "conversation_list"
	RequestID string         `json:"request_id"`
	Query     map[string]any `json:"query,omitempty"`
}

// ExecuteCommandCommand dispatches a slash command (built-in or mod-provided);
// results arrive as command_start/command_end stream deltas correlated by
// request_id.
type ExecuteCommandCommand struct {
	Type      string       `json:"type"` // "execute_command"
	CommandID string       `json:"command_id"`
	RequestID string       `json:"request_id"`
	Runtime   RuntimeScope `json:"runtime"`
	Args      string       `json:"args,omitempty"`
}

type ChangeDeviceStatePayload struct {
	Mode PermissionMode `json:"mode,omitempty"`
	CWD  string         `json:"cwd,omitempty"`
}

type ChangeDeviceStateCommand struct {
	Type    string                   `json:"type"` // "change_device_state"
	Runtime RuntimeScope             `json:"runtime"`
	Payload ChangeDeviceStatePayload `json:"payload"`
}

type ConversationForkCommand struct {
	Type           string         `json:"type"` // "conversation_fork"
	RequestID      string         `json:"request_id"`
	ConversationID string         `json:"conversation_id"`
	Body           map[string]any `json:"body,omitempty"`
}

type ConversationUpdateCommand struct {
	Type           string         `json:"type"` // "conversation_update"
	RequestID      string         `json:"request_id"`
	ConversationID string         `json:"conversation_id"`
	Body           map[string]any `json:"body"`
}

type ConversationRecompileCommand struct {
	Type           string `json:"type"` // "conversation_recompile"
	RequestID      string `json:"request_id"`
	ConversationID string `json:"conversation_id"`
}

// GetExperimentsCommand / SetExperimentCommand toggle local experiments.
type GetExperimentsCommand struct {
	Type      string `json:"type"` // "get_experiments"
	RequestID string `json:"request_id"`
}

type SetExperimentCommand struct {
	Type         string `json:"type"` // "set_experiment"
	RequestID    string `json:"request_id"`
	ExperimentID string `json:"experiment_id"`
	Enabled      *bool  `json:"enabled"` // null clears the override
}

type ExperimentSnapshot struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	EnvVar      string `json:"envVar"`
	Enabled     bool   `json:"enabled"`
	Source      string `json:"source"`
}

// Reflection (sleeptime) settings: trigger off|step-count|compaction-event.
// Both commands are runtime-scoped (validated server-side — an agent_id
// field alone fails validation and the request is silently dropped).
type GetReflectionSettingsCommand struct {
	Type      string       `json:"type"` // "get_reflection_settings"
	RequestID string       `json:"request_id"`
	Runtime   RuntimeScope `json:"runtime"`
}

type ReflectionSettingsBody struct {
	Trigger   string `json:"trigger"`
	StepCount int    `json:"step_count"`
}

type SetReflectionSettingsCommand struct {
	Type      string                 `json:"type"` // "set_reflection_settings"
	RequestID string                 `json:"request_id"`
	Runtime   RuntimeScope           `json:"runtime"`
	Settings  ReflectionSettingsBody `json:"settings"`
	Scope     string                 `json:"scope,omitempty"` // local_project|global|both
}

type ReflectionSettingsSnapshot struct {
	AgentID   string `json:"agent_id"`
	Trigger   string `json:"trigger"`
	StepCount int    `json:"step_count"`
}

// BackgroundProcessSummary is a union: kind "bash" (command/exit_code) or
// "agent_task" (task_type/description). Delivered inside device_status.
type BackgroundProcessSummary struct {
	ProcessID   string `json:"process_id"`
	Kind        string `json:"kind"`
	Command     string `json:"command"`
	TaskType    string `json:"task_type"`
	Description string `json:"description"`
	StartedAtMs int64  `json:"started_at_ms"`
	Status      string `json:"status"`
	ExitCode    *int   `json:"exit_code"`
}

type AgentUpdateCommand struct {
	Type      string         `json:"type"` // "agent_update"
	RequestID string         `json:"request_id"`
	AgentID   string         `json:"agent_id"`
	Body      map[string]any `json:"body"`
}

type ListModelsCommand struct {
	Type      string `json:"type"` // "list_models"
	RequestID string `json:"request_id"`
	Force     bool   `json:"force,omitempty"`
}

type UpdateModelPayload struct {
	ModelID     string `json:"model_id,omitempty"`
	ModelHandle string `json:"model_handle,omitempty"`
}

type UpdateModelCommand struct {
	Type      string             `json:"type"` // "update_model"
	RequestID string             `json:"request_id"`
	Runtime   RuntimeScope       `json:"runtime"`
	Payload   UpdateModelPayload `json:"payload"`
}

type ModelEntry struct {
	ID          string `json:"id"`
	Handle      string `json:"handle"`
	Label       string `json:"label"`
	Description string `json:"description"`
	IsDefault   bool   `json:"isDefault"`
	IsFeatured  bool   `json:"isFeatured"`
	Free        bool   `json:"free"`
}

type ListModelsResponse struct {
	Success          bool         `json:"success"`
	Error            string       `json:"error"`
	Entries          []ModelEntry `json:"entries"`
	AvailableHandles []string     `json:"available_handles"`
}

type UpdateModelResponse struct {
	Success     bool   `json:"success"`
	Error       string `json:"error"`
	AppliedTo   string `json:"applied_to"`
	ModelID     string `json:"model_id"`
	ModelHandle string `json:"model_handle"`
}

// Ack is the generic success/error envelope shared by CRUD responses.
type Ack struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	// conversation_fork_response carries the new conversation reference.
	Conversation *ConversationSummary `json:"conversation"`
}

// ── Inbound frames (app-server → client) ──

// Frame is a decoded inbound frame. Exactly one typed field is non-nil;
// Raw always holds the original JSON.
type Frame struct {
	Type      string
	RequestID string
	Raw       json.RawMessage

	RuntimeStartResponse *RuntimeStartResponse
	ControlRequest       *ControlRequest
	StreamDelta          *StreamDeltaMessage
	LoopStatus           *LoopStatusUpdate
	DeviceStatus         *DeviceStatusUpdate
	AgentList            *AgentListResponse
	MessagesList         *ConversationMessagesListResponse
	ConversationList     *ConversationListResponse
	QueueUpdate          *QueueUpdate
	SubagentUpdate       *SubagentUpdate
	ListModels           *ListModelsResponse
	UpdateModel          *UpdateModelResponse
}

type ConversationSummary struct {
	ID            string `json:"id"`
	AgentID       string `json:"agent_id"`
	Title         string `json:"title"`
	Summary       string `json:"summary"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	LastMessageAt string `json:"last_message_at"`
	Archived      bool   `json:"archived"`
	// Context accounting (local backend): ids currently in context plus the
	// per-conversation window limit override when set.
	InContextMessageIDs []string `json:"in_context_message_ids"`
	ModelSettings       struct {
		ContextWindowLimit int64 `json:"context_window_limit"`
	} `json:"model_settings"`
}

type ConversationListResponse struct {
	Success       bool                  `json:"success"`
	Error         string                `json:"error"`
	Conversations []ConversationSummary `json:"conversations"`
}

type QueueItem struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Content    json.RawMessage `json:"content"` // string | content parts
	EnqueuedAt string          `json:"enqueued_at"`
}

// ContentText renders queue content that may be a string or content parts.
func (q *QueueItem) ContentText() string {
	d := Delta{Content: q.Content}
	return d.Text()
}

type QueueUpdate struct {
	Runtime RuntimeScope `json:"runtime"`
	Queue   []QueueItem  `json:"queue"`
}

type SubagentSnapshot struct {
	SubagentID   string `json:"subagent_id"`
	SubagentType string `json:"subagent_type"`
	Description  string `json:"description"`
	Status       string `json:"status"` // pending|running|completed|error
	IsBackground bool   `json:"is_background"`
	TotalTokens  int64  `json:"total_tokens"`
	DurationMs   int64  `json:"duration_ms"`
	Error        string `json:"error"`
}

type SubagentUpdate struct {
	Runtime   RuntimeScope       `json:"runtime"`
	Subagents []SubagentSnapshot `json:"subagents"`
}

type AgentSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type AgentListResponse struct {
	Success bool           `json:"success"`
	Error   string         `json:"error"`
	Agents  []AgentSummary `json:"agents"`
}

// ConversationMessagesListResponse carries history as LettaMessage objects,
// which share the message_type discrimination of stream deltas — Delta
// decodes them.
type ConversationMessagesListResponse struct {
	Success  bool    `json:"success"`
	Error    string  `json:"error"`
	Messages []Delta `json:"messages"`
}

type RuntimeStartResponse struct {
	Success bool          `json:"success"`
	Error   string        `json:"error"`
	Runtime *RuntimeScope `json:"runtime"`
}

type PermissionSuggestion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type DiffHunkLine struct {
	Type    string `json:"type"` // "context" | "add" | "remove"
	Content string `json:"content"`
}

type DiffHunk struct {
	OldStart int            `json:"oldStart"`
	OldLines int            `json:"oldLines"`
	NewStart int            `json:"newStart"`
	NewLines int            `json:"newLines"`
	Lines    []DiffHunkLine `json:"lines"`
}

// DiffPreview: mode "advanced" carries hunks; "fallback"/"unpreviewable"
// carry a reason.
type DiffPreview struct {
	Mode     string     `json:"mode"`
	FileName string     `json:"fileName"`
	Hunks    []DiffHunk `json:"hunks"`
	Reason   string     `json:"reason"`
}

type ControlRequestBody struct {
	Subtype               string                 `json:"subtype"` // "can_use_tool"
	ToolName              string                 `json:"tool_name"`
	Input                 map[string]any         `json:"input"`
	ToolCallID            string                 `json:"tool_call_id"`
	PermissionSuggestions []PermissionSuggestion `json:"permission_suggestions"`
	BlockedPath           *string                `json:"blocked_path"`
	Diffs                 []DiffPreview          `json:"diffs"`
}

type ControlRequest struct {
	RequestID      string             `json:"request_id"`
	Request        ControlRequestBody `json:"request"`
	AgentID        string             `json:"agent_id"`
	ConversationID string             `json:"conversation_id"`
}

// Delta is a tolerant union of every stream_delta message type we render:
// Letta streaming messages (assistant/reasoning/tool call/return/stop_reason)
// plus UMI lifecycle events (client tool + command + status/retry/loop_error).
type Delta struct {
	ID          string          `json:"id"`
	Date        string          `json:"date"` // ISO timestamp; used to normalize history order
	MessageType string          `json:"message_type"`
	Content     json.RawMessage `json:"content"`   // string | [{type:"text",text}]
	Reasoning   string          `json:"reasoning"` // reasoning_message
	StopReason  string          `json:"stop_reason"`
	RunID       string          `json:"run_id"`

	// tool_call_message (letta streaming): arguments stream incrementally.
	ToolCall *struct {
		Name       string `json:"name"`
		Arguments  string `json:"arguments"`
		ToolCallID string `json:"tool_call_id"`
	} `json:"tool_call"`
	// tool_return_message
	ToolReturn json.RawMessage `json:"tool_return"`
	Status     string          `json:"status"` // tool_return_message | client_tool_end
	ToolCallID string          `json:"tool_call_id"`
	ToolName   string          `json:"tool_name"` // client_tool_start
	ToolArgs   string          `json:"tool_args"`

	// usage_statistics
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	StepCount        int64 `json:"step_count"`

	// command / slash command lifecycle + status / retry / loop_error
	CommandID    string `json:"command_id"`
	Input        string `json:"input"`
	Output       string `json:"output"`
	Success      *bool  `json:"success"`
	Preformatted bool   `json:"preformatted"`
	DimOutput    bool   `json:"dim_output"`
	Message      string `json:"message"`
	Level        string `json:"level"`
}

// ToolReturnText renders the tool_return payload (string or structured).
func (d *Delta) ToolReturnText() string {
	if len(d.ToolReturn) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(d.ToolReturn, &s); err == nil {
		return s
	}
	return string(d.ToolReturn)
}

type StreamDeltaMessage struct {
	Runtime    RuntimeScope `json:"runtime"`
	Delta      Delta        `json:"delta"`
	SubagentID string       `json:"subagent_id"`
	EventSeq   int64        `json:"event_seq"`
}

type LoopStatusUpdate struct {
	Runtime    RuntimeScope `json:"runtime"`
	LoopStatus LoopState    `json:"loop_status"`
}

type PendingControlRequestSnapshot struct {
	RequestID string             `json:"request_id"`
	Request   ControlRequestBody `json:"request"`
}

type ModCommandInfo struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Args        string `json:"args"`
}

type DeviceStatusUpdate struct {
	Runtime      RuntimeScope `json:"runtime"`
	DeviceStatus struct {
		ConnectionName         string                          `json:"connection_name"`
		Mode                   PermissionMode                  `json:"current_permission_mode"`
		CWD                    string                          `json:"current_working_directory"`
		IsProcessing           bool                            `json:"is_processing"`
		LettaCodeVersion       string                          `json:"letta_code_version"`
		SupportedCommands      []string                        `json:"supported_commands"`
		ModCommands            []ModCommandInfo                `json:"mod_commands"`
		PendingControlRequests []PendingControlRequestSnapshot `json:"pending_control_requests"`
		// MemoryDirectory is the agent's MemFS root on this machine (null
		// until memfs is enabled). Used by /skills to find agent skills.
		MemoryDirectory     string                     `json:"memory_directory"`
		BackgroundProcesses []BackgroundProcessSummary `json:"background_processes"`
		Experiments         []ExperimentSnapshot       `json:"experiments"`
	} `json:"device_status"`
}

// Decode parses an inbound frame. Unknown types yield a Frame with only
// Type/Raw set — callers should skip what they do not understand.
func Decode(raw []byte) (Frame, error) {
	var head struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return Frame{}, fmt.Errorf("frame header: %w", err)
	}
	f := Frame{Type: head.Type, RequestID: head.RequestID, Raw: append([]byte(nil), raw...)}
	var err error
	switch head.Type {
	case "runtime_start_response":
		f.RuntimeStartResponse = &RuntimeStartResponse{}
		err = json.Unmarshal(raw, f.RuntimeStartResponse)
	case "control_request":
		f.ControlRequest = &ControlRequest{}
		err = json.Unmarshal(raw, f.ControlRequest)
	case "stream_delta":
		f.StreamDelta = &StreamDeltaMessage{}
		err = json.Unmarshal(raw, f.StreamDelta)
	case "update_loop_status":
		f.LoopStatus = &LoopStatusUpdate{}
		err = json.Unmarshal(raw, f.LoopStatus)
	case "update_device_status":
		f.DeviceStatus = &DeviceStatusUpdate{}
		err = json.Unmarshal(raw, f.DeviceStatus)
	case "agent_list_response":
		f.AgentList = &AgentListResponse{}
		err = json.Unmarshal(raw, f.AgentList)
	case "conversation_messages_list_response":
		f.MessagesList = &ConversationMessagesListResponse{}
		err = json.Unmarshal(raw, f.MessagesList)
	case "conversation_list_response":
		f.ConversationList = &ConversationListResponse{}
		err = json.Unmarshal(raw, f.ConversationList)
	case "update_queue":
		f.QueueUpdate = &QueueUpdate{}
		err = json.Unmarshal(raw, f.QueueUpdate)
	case "update_subagent_state":
		f.SubagentUpdate = &SubagentUpdate{}
		err = json.Unmarshal(raw, f.SubagentUpdate)
	case "list_models_response":
		f.ListModels = &ListModelsResponse{}
		err = json.Unmarshal(raw, f.ListModels)
	case "update_model_response":
		f.UpdateModel = &UpdateModelResponse{}
		err = json.Unmarshal(raw, f.UpdateModel)
	}
	return f, err
}

// Text renders delta content that may be a plain string or a list of
// {type:"text", text:"..."} parts.
func (d *Delta) Text() string {
	if len(d.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(d.Content, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(d.Content, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return ""
}
