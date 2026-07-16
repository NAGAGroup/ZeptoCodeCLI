// Management-command wire types: secrets, skills, connect providers,
// memory browser, and crons. Shapes transliterated from letta-code's
// shipped dist/types/types/protocol_v2.d.ts (0.28.8) — request/response
// pairs correlate by request_id, so Decode does not need to know these
// response types (the client unmarshals Frame.Raw directly).
package protocol

// ── secrets ──

type SecretListCommand struct {
	Type      string `json:"type"` // "secret_list"
	RequestID string `json:"request_id"`
	AgentID   string `json:"agent_id"`
}

type SecretKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type SecretListResponse struct {
	Success bool       `json:"success"`
	Secrets []SecretKV `json:"secrets"`
	Error   string     `json:"error"`
}

// SecretApplyCommand applies a batch of mutations atomically:
// (current ∪ set) ∖ unset.
type SecretApplyCommand struct {
	Type      string            `json:"type"` // "secret_apply"
	RequestID string            `json:"request_id"`
	AgentID   string            `json:"agent_id"`
	Set       map[string]string `json:"set"`
	Unset     []string          `json:"unset"`
}

type SecretApplyResponse struct {
	Success bool     `json:"success"`
	Names   []string `json:"names"`
	Error   string   `json:"error"`
}

// ── skills ──
//
// NB (0.28.8): device_status.current_available_skills is hardcoded to []
// server-side and there is no skill-list wire command — enable/disable are
// write-only. Listing is done from the local filesystem (same machine).

type SkillEnableCommand struct {
	Type      string `json:"type"` // "skill_enable"
	RequestID string `json:"request_id"`
	SkillPath string `json:"skill_path"` // absolute path to the skill dir
}

type SkillDisableCommand struct {
	Type      string `json:"type"` // "skill_disable"
	RequestID string `json:"request_id"`
	Name      string `json:"name"` // symlink name in ~/.letta/skills
}

type SkillActionResponse struct {
	Success   bool   `json:"success"`
	SkillName string `json:"skill_name"`
	Error     string `json:"error"`
}

// ── connect providers ──

type ListConnectProvidersCommand struct {
	Type      string `json:"type"` // "list_connect_providers"
	RequestID string `json:"request_id"`
	Target    string `json:"target"` // "local"
}

type ConnectProviderCommand struct {
	Type         string            `json:"type"` // "connect_provider"
	RequestID    string            `json:"request_id"`
	Target       string            `json:"target"`
	ProviderID   string            `json:"provider_id"`
	AuthMethodID string            `json:"auth_method_id,omitempty"`
	Fields       map[string]string `json:"fields"`
}

type DisconnectProviderCommand struct {
	Type         string `json:"type"` // "disconnect_provider"
	RequestID    string `json:"request_id"`
	Target       string `json:"target"`
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name,omitempty"`
}

type ConnectProviderField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder"`
	Secret      bool   `json:"secret"`
	Required    bool   `json:"required"`
}

type ConnectProviderAuthMethod struct {
	ID          string                 `json:"id"`
	Label       string                 `json:"label"`
	Description string                 `json:"description"`
	Fields      []ConnectProviderField `json:"fields"`
}

type ConnectProviderConnectionState struct {
	IsConnected  bool   `json:"is_connected"`
	ProviderName string `json:"provider_name"`
	AuthType     string `json:"auth_type"` // "api" | "oauth"
}

type ConnectProviderEntry struct {
	ID                 string                           `json:"id"`
	DisplayName        string                           `json:"display_name"`
	Description        string                           `json:"description"`
	ProviderType       string                           `json:"provider_type"`
	ProviderName       string                           `json:"provider_name"`
	IsOAuth            bool                             `json:"is_oauth"`
	RequiresAPIKey     bool                             `json:"requires_api_key"`
	Fields             []ConnectProviderField           `json:"fields"`
	AuthMethods        []ConnectProviderAuthMethod      `json:"auth_methods"`
	Connected          ConnectProviderConnectionState   `json:"connected"`
	ConnectedProviders []ConnectProviderConnectionState `json:"connected_providers"`
}

type ConnectProvidersResponse struct {
	Success               bool                   `json:"success"`
	Providers             []ConnectProviderEntry `json:"providers"`
	ModelsMayHaveChanged  bool                   `json:"models_may_have_changed"`
	Error                 string                 `json:"error"`
}

type ChatGPTUsageReadCommand struct {
	Type         string `json:"type"` // "chatgpt_usage_read"
	RequestID    string `json:"request_id"`
	Target       string `json:"target"` // "local" | "api"
	ForceRefresh bool   `json:"force_refresh,omitempty"`
}

type ChatGPTUsageWindow struct {
	Label       string   `json:"label"`
	UsedPercent *float64 `json:"usedPercent"`
	ResetsAt    *int64   `json:"resetsAt"`
}

type ChatGPTUsageSnapshot struct {
	ProviderName string               `json:"providerName"`
	FetchedAt    string               `json:"fetchedAt"`
	Summary      string               `json:"summary"`
	PlanType     *string              `json:"planType"`
	Primary      *ChatGPTUsageWindow  `json:"primary"`
	Secondary    *ChatGPTUsageWindow  `json:"secondary"`
	Additional   []ChatGPTUsageWindow `json:"additional"`
}

type ChatGPTUsageError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ChatGPTUsageReadResponse struct {
	Success bool                  `json:"success"`
	Usage   *ChatGPTUsageSnapshot `json:"usage"`
	Error   *ChatGPTUsageError    `json:"error"`
}

// ── memory browser ──

type ListMemoryCommand struct {
	Type      string `json:"type"` // "list_memory"
	RequestID string `json:"request_id"`
	AgentID   string `json:"agent_id"`
}

type MemoryFileEntry struct {
	RelativePath string `json:"relative_path"`
	IsSystem     bool   `json:"is_system"`
	Description  string `json:"description"`
	Content      string `json:"content"`
	Size         int64  `json:"size"`
	Kind         string `json:"kind"` // "markdown" | "image"
}

// ListMemoryResponse arrives CHUNKED: multiple frames share the request_id;
// the last one has done=true. Entries accumulate across chunks.
type ListMemoryResponse struct {
	Entries          []MemoryFileEntry `json:"entries"`
	Done             bool              `json:"done"`
	Total            int               `json:"total"`
	Success          bool              `json:"success"`
	Error            string            `json:"error"`
	MemfsEnabled     *bool             `json:"memfs_enabled"`
	MemfsInitialized *bool             `json:"memfs_initialized"`
}

type ReadMemoryFileCommand struct {
	Type      string `json:"type"` // "read_memory_file"
	RequestID string `json:"request_id"`
	AgentID   string `json:"agent_id"`
	Path      string `json:"path"`
	Encoding  string `json:"encoding,omitempty"` // "utf8" (default) | "base64"
}

type ReadMemoryFileResponse struct {
	Path     string  `json:"path"`
	Content  *string `json:"content"`
	Encoding string  `json:"encoding"`
	Success  bool    `json:"success"`
	Error    string  `json:"error"`
}

type WriteMemoryFileCommand struct {
	Type          string `json:"type"` // "write_memory_file"
	RequestID     string `json:"request_id"`
	AgentID       string `json:"agent_id"`
	Path          string `json:"path"`
	Content       string `json:"content"`
	Encoding      string `json:"encoding,omitempty"`
	CommitMessage string `json:"commit_message,omitempty"`
}

type DeleteMemoryFileCommand struct {
	Type          string `json:"type"` // "delete_memory_file"
	RequestID     string `json:"request_id"`
	AgentID       string `json:"agent_id"`
	Path          string `json:"path"`
	CommitMessage string `json:"commit_message,omitempty"`
}

type MemoryWriteResponse struct {
	Path      string `json:"path"`
	Success   bool   `json:"success"`
	Committed bool   `json:"committed"`
	CommitSHA string `json:"commit_sha"`
	Error     string `json:"error"`
}

type MemoryHistoryCommand struct {
	Type      string `json:"type"` // "memory_history"
	RequestID string `json:"request_id"`
	AgentID   string `json:"agent_id"`
	FilePath  string `json:"file_path,omitempty"` // omit for global history
	Limit     int    `json:"limit,omitempty"`
}

type MemoryHistoryCommit struct {
	SHA        string `json:"sha"`
	Message    string `json:"message"`
	Timestamp  string `json:"timestamp"`
	AuthorName string `json:"author_name"`
}

type MemoryHistoryResponse struct {
	FilePath string                `json:"file_path"`
	Commits  []MemoryHistoryCommit `json:"commits"`
	Success  bool                  `json:"success"`
	Error    string                `json:"error"`
}

type MemoryCommitDiffCommand struct {
	Type      string `json:"type"` // "memory_commit_diff"
	RequestID string `json:"request_id"`
	AgentID   string `json:"agent_id"`
	SHA       string `json:"sha"`
}

type MemoryCommitDiffResponse struct {
	SHA     string  `json:"sha"`
	Diff    *string `json:"diff"`
	Success bool    `json:"success"`
	Error   string  `json:"error"`
}

type MemoryFileAtRefCommand struct {
	Type      string `json:"type"` // "memory_file_at_ref"
	RequestID string `json:"request_id"`
	AgentID   string `json:"agent_id"`
	FilePath  string `json:"file_path"`
	Ref       string `json:"ref"`
}

type MemoryFileAtRefResponse struct {
	FilePath string  `json:"file_path"`
	Ref      string  `json:"ref"`
	Content  *string `json:"content"`
	Success  bool    `json:"success"`
	Error    string  `json:"error"`
}

type EnableMemfsCommand struct {
	Type      string `json:"type"` // "enable_memfs"
	RequestID string `json:"request_id"`
	AgentID   string `json:"agent_id"`
}

type EnableMemfsResponse struct {
	Success         bool   `json:"success"`
	MemoryDirectory string `json:"memory_directory"`
	Error           string `json:"error"`
}

// ── crons ──

type CronListCommand struct {
	Type      string `json:"type"` // "cron_list"
	RequestID string `json:"request_id"`
	AgentID   string `json:"agent_id,omitempty"`
}

type CronTask struct {
	ID             string  `json:"id"`
	AgentID        string  `json:"agent_id"`
	ConversationID string  `json:"conversation_id"`
	Name           string  `json:"name"`
	Description    string  `json:"description"`
	Cron           string  `json:"cron"`
	Timezone       string  `json:"timezone"`
	Recurring      bool    `json:"recurring"`
	Prompt         string  `json:"prompt"`
	Status         string  `json:"status"`
	CreatedAt      string  `json:"created_at"`
	LastFiredAt    *string `json:"last_fired_at"`
	FireCount      int     `json:"fire_count"`
	LastRunOutcome *string `json:"last_run_outcome"`
	LastRunError   *string `json:"last_run_error"`
	MissedCount    int     `json:"missed_count"`
	FailedCount    int     `json:"failed_count"`
	ScheduledFor   *string `json:"scheduled_for"`
}

type CronListResponse struct {
	Tasks   []CronTask `json:"tasks"`
	Success bool       `json:"success"`
	Error   string     `json:"error"`
}

type CronRunsCommand struct {
	Type      string `json:"type"` // "cron_runs"
	RequestID string `json:"request_id"`
	TaskID    string `json:"task_id"`
	Limit     int    `json:"limit,omitempty"`
}

type CronRunLogEntry struct {
	TS      int64  `json:"ts"`
	Status  string `json:"status"`  // ok | error | skipped
	Outcome string `json:"outcome"` //
	Error   string `json:"error"`
	Summary string `json:"summary"`
}

type CronRunLogPage struct {
	Entries []CronRunLogEntry `json:"entries"`
	Total   int               `json:"total"`
}

type CronRunsResponse struct {
	Success bool            `json:"success"`
	Page    *CronRunLogPage `json:"page"`
	Error   string          `json:"error"`
}

type CronTriggerCommand struct {
	Type      string `json:"type"` // "cron_trigger"
	RequestID string `json:"request_id"`
	TaskID    string `json:"task_id"`
}

type CronTriggerResponse struct {
	Success bool      `json:"success"`
	Found   bool      `json:"found"`
	Task    *CronTask `json:"task"`
	Error   string    `json:"error"`
}

type CronDeleteCommand struct {
	Type      string `json:"type"` // "cron_delete"
	RequestID string `json:"request_id"`
	TaskID    string `json:"task_id"`
}

type CronDeleteResponse struct {
	Success bool   `json:"success"`
	Found   bool   `json:"found"`
	Error   string `json:"error"`
}
