// Management-command client methods: secrets, skills, connect providers,
// memory browser, and crons. All follow the request/response correlation
// pattern; responses are unmarshaled from Frame.Raw (Decode does not need
// typed fields for them).
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/protocol"
)

// mgmtRequest sends a command and unmarshals the correlated response into
// out. Returns the raw frame too, for callers that need it.
func (c *Client) mgmtRequest(ctx context.Context, requestID string, cmd any, out any) error {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	f, err := c.request(reqCtx, requestID, cmd)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(f.Raw, out); err != nil {
		return fmt.Errorf("%s: decode: %w", f.Type, err)
	}
	return nil
}

// ── agent info ──

// AgentRetrieve fetches the current agent's record: display name and model
// handle (the statusline wants names, not ids).
func (c *Client) AgentRetrieve(ctx context.Context) (name, model string, err error) {
	cmd := protocol.AgentRetrieveCommand{Type: "agent_retrieve", RequestID: c.nextRequestID(), AgentID: c.Runtime.AgentID}
	var resp struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Agent   *struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"agent"`
	}
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return "", "", err
	}
	if !resp.Success || resp.Agent == nil {
		return "", "", fmt.Errorf("agent_retrieve failed: %s", orUnknown(resp.Error))
	}
	return resp.Agent.Name, resp.Agent.Model, nil
}

// ── secrets ──

// SecretList fetches secret entries (keys + plaintext values). Values are
// for edit-form prefill ONLY — never render them into the transcript.
func (c *Client) SecretList(ctx context.Context) ([]protocol.SecretKV, error) {
	cmd := protocol.SecretListCommand{Type: "secret_list", RequestID: c.nextRequestID(), AgentID: c.Runtime.AgentID}
	var resp protocol.SecretListResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("secret_list failed: %s", orUnknown(resp.Error))
	}
	return resp.Secrets, nil
}

// SecretApply sets/unsets secrets atomically and returns the resulting names.
func (c *Client) SecretApply(ctx context.Context, set map[string]string, unset []string) ([]string, error) {
	if set == nil {
		set = map[string]string{}
	}
	if unset == nil {
		unset = []string{}
	}
	cmd := protocol.SecretApplyCommand{
		Type: "secret_apply", RequestID: c.nextRequestID(),
		AgentID: c.Runtime.AgentID, Set: set, Unset: unset,
	}
	var resp protocol.SecretApplyResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("secret_apply failed: %s", orUnknown(resp.Error))
	}
	return resp.Names, nil
}

// ── skills ──

// SkillEnable symlinks an absolute skill directory into ~/.letta/skills.
func (c *Client) SkillEnable(ctx context.Context, skillPath string) (string, error) {
	cmd := protocol.SkillEnableCommand{Type: "skill_enable", RequestID: c.nextRequestID(), SkillPath: skillPath}
	var resp protocol.SkillActionResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("skill_enable failed: %s", orUnknown(resp.Error))
	}
	return resp.SkillName, nil
}

// SkillDisable removes a skill symlink from ~/.letta/skills (refuses
// non-symlinks server-side, so bundled skills cannot be disabled).
func (c *Client) SkillDisable(ctx context.Context, name string) (string, error) {
	cmd := protocol.SkillDisableCommand{Type: "skill_disable", RequestID: c.nextRequestID(), Name: name}
	var resp protocol.SkillActionResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("skill_disable failed: %s", orUnknown(resp.Error))
	}
	return resp.SkillName, nil
}

// ── connect providers ──

func (c *Client) ListConnectProviders(ctx context.Context) ([]protocol.ConnectProviderEntry, error) {
	cmd := protocol.ListConnectProvidersCommand{Type: "list_connect_providers", RequestID: c.nextRequestID(), Target: "local"}
	var resp protocol.ConnectProvidersResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("list_connect_providers failed: %s", orUnknown(resp.Error))
	}
	return resp.Providers, nil
}

// ConnectProvider stores API-key credentials for a provider. OAuth
// providers are rejected server-side ("uses OAuth") — the UI filters them.
func (c *Client) ConnectProvider(ctx context.Context, providerID, authMethodID string, fields map[string]string) (*protocol.ConnectProvidersResponse, error) {
	cmd := protocol.ConnectProviderCommand{
		Type: "connect_provider", RequestID: c.nextRequestID(), Target: "local",
		ProviderID: providerID, AuthMethodID: authMethodID, Fields: fields,
	}
	var resp protocol.ConnectProvidersResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("connect_provider failed: %s", orUnknown(resp.Error))
	}
	return &resp, nil
}

func (c *Client) DisconnectProvider(ctx context.Context, providerID, providerName string) (*protocol.ConnectProvidersResponse, error) {
	cmd := protocol.DisconnectProviderCommand{
		Type: "disconnect_provider", RequestID: c.nextRequestID(), Target: "local",
		ProviderID: providerID, ProviderName: providerName,
	}
	var resp protocol.ConnectProvidersResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("disconnect_provider failed: %s", orUnknown(resp.Error))
	}
	return &resp, nil
}

func (c *Client) ChatGPTUsage(ctx context.Context, force bool) (*protocol.ChatGPTUsageSnapshot, error) {
	cmd := protocol.ChatGPTUsageReadCommand{
		Type: "chatgpt_usage_read", RequestID: c.nextRequestID(),
		Target: "local", ForceRefresh: force,
	}
	var resp protocol.ChatGPTUsageReadResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return nil, err
	}
	if !resp.Success || resp.Usage == nil {
		msg := "unknown error"
		if resp.Error != nil {
			msg = resp.Error.Code + ": " + resp.Error.Message
		}
		return nil, fmt.Errorf("chatgpt_usage_read failed: %s", msg)
	}
	return resp.Usage, nil
}

// ── memory browser ──

// MemoryList fetches the agent's MemFS tree with contents. Responses are
// CHUNKED (multiple frames per request_id, done=true on the last), so this
// uses the streaming correlation path.
func (c *Client) MemoryList(ctx context.Context) (*protocol.ListMemoryResponse, error) {
	cmd := protocol.ListMemoryCommand{Type: "list_memory", RequestID: c.nextRequestID(), AgentID: c.Runtime.AgentID}
	reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	frames, unregister, err := c.requestStream(cmd.RequestID, cmd)
	if err != nil {
		return nil, err
	}
	defer unregister()

	out := &protocol.ListMemoryResponse{}
	for {
		select {
		case f := <-frames:
			var chunk protocol.ListMemoryResponse
			if err := json.Unmarshal(f.Raw, &chunk); err != nil {
				return nil, fmt.Errorf("list_memory: decode: %w", err)
			}
			if !chunk.Success {
				return nil, fmt.Errorf("list_memory failed: %s", orUnknown(chunk.Error))
			}
			out.Entries = append(out.Entries, chunk.Entries...)
			out.Success = true
			out.Total = chunk.Total
			if chunk.MemfsEnabled != nil {
				out.MemfsEnabled = chunk.MemfsEnabled
			}
			if chunk.MemfsInitialized != nil {
				out.MemfsInitialized = chunk.MemfsInitialized
			}
			if chunk.Done {
				out.Done = true
				return out, nil
			}
		case <-reqCtx.Done():
			return nil, reqCtx.Err()
		}
	}
}

func (c *Client) MemoryRead(ctx context.Context, path string) (string, error) {
	cmd := protocol.ReadMemoryFileCommand{
		Type: "read_memory_file", RequestID: c.nextRequestID(),
		AgentID: c.Runtime.AgentID, Path: path,
	}
	var resp protocol.ReadMemoryFileResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return "", err
	}
	if !resp.Success || resp.Content == nil {
		return "", fmt.Errorf("read_memory_file failed: %s", orUnknown(resp.Error))
	}
	return *resp.Content, nil
}

func (c *Client) MemoryWrite(ctx context.Context, path, content, commitMessage string) (*protocol.MemoryWriteResponse, error) {
	cmd := protocol.WriteMemoryFileCommand{
		Type: "write_memory_file", RequestID: c.nextRequestID(),
		AgentID: c.Runtime.AgentID, Path: path, Content: content, CommitMessage: commitMessage,
	}
	var resp protocol.MemoryWriteResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("write_memory_file failed: %s", orUnknown(resp.Error))
	}
	return &resp, nil
}

func (c *Client) MemoryDelete(ctx context.Context, path, commitMessage string) (*protocol.MemoryWriteResponse, error) {
	cmd := protocol.DeleteMemoryFileCommand{
		Type: "delete_memory_file", RequestID: c.nextRequestID(),
		AgentID: c.Runtime.AgentID, Path: path, CommitMessage: commitMessage,
	}
	var resp protocol.MemoryWriteResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("delete_memory_file failed: %s", orUnknown(resp.Error))
	}
	return &resp, nil
}

func (c *Client) MemoryHistory(ctx context.Context, filePath string, limit int) ([]protocol.MemoryHistoryCommit, error) {
	cmd := protocol.MemoryHistoryCommand{
		Type: "memory_history", RequestID: c.nextRequestID(),
		AgentID: c.Runtime.AgentID, FilePath: filePath, Limit: limit,
	}
	var resp protocol.MemoryHistoryResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("memory_history failed: %s", orUnknown(resp.Error))
	}
	return resp.Commits, nil
}

func (c *Client) MemoryCommitDiff(ctx context.Context, sha string) (string, error) {
	cmd := protocol.MemoryCommitDiffCommand{
		Type: "memory_commit_diff", RequestID: c.nextRequestID(),
		AgentID: c.Runtime.AgentID, SHA: sha,
	}
	var resp protocol.MemoryCommitDiffResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return "", err
	}
	if !resp.Success || resp.Diff == nil {
		return "", fmt.Errorf("memory_commit_diff failed: %s", orUnknown(resp.Error))
	}
	return *resp.Diff, nil
}

func (c *Client) MemoryFileAtRef(ctx context.Context, filePath, ref string) (string, error) {
	cmd := protocol.MemoryFileAtRefCommand{
		Type: "memory_file_at_ref", RequestID: c.nextRequestID(),
		AgentID: c.Runtime.AgentID, FilePath: filePath, Ref: ref,
	}
	var resp protocol.MemoryFileAtRefResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return "", err
	}
	if !resp.Success || resp.Content == nil {
		return "", fmt.Errorf("memory_file_at_ref failed: %s", orUnknown(resp.Error))
	}
	return *resp.Content, nil
}

func (c *Client) EnableMemfs(ctx context.Context) (string, error) {
	cmd := protocol.EnableMemfsCommand{Type: "enable_memfs", RequestID: c.nextRequestID(), AgentID: c.Runtime.AgentID}
	var resp protocol.EnableMemfsResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("enable_memfs failed: %s", orUnknown(resp.Error))
	}
	return resp.MemoryDirectory, nil
}

// ── crons ──

func (c *Client) CronList(ctx context.Context) ([]protocol.CronTask, error) {
	cmd := protocol.CronListCommand{Type: "cron_list", RequestID: c.nextRequestID(), AgentID: c.Runtime.AgentID}
	var resp protocol.CronListResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("cron_list failed: %s", orUnknown(resp.Error))
	}
	return resp.Tasks, nil
}

func (c *Client) CronRuns(ctx context.Context, taskID string, limit int) (*protocol.CronRunLogPage, error) {
	cmd := protocol.CronRunsCommand{Type: "cron_runs", RequestID: c.nextRequestID(), TaskID: taskID, Limit: limit}
	var resp protocol.CronRunsResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("cron_runs failed: %s", orUnknown(resp.Error))
	}
	return resp.Page, nil
}

func (c *Client) CronTrigger(ctx context.Context, taskID string) error {
	cmd := protocol.CronTriggerCommand{Type: "cron_trigger", RequestID: c.nextRequestID(), TaskID: taskID}
	var resp protocol.CronTriggerResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("cron_trigger failed: %s", orUnknown(resp.Error))
	}
	if !resp.Found {
		return fmt.Errorf("cron task not found: %s", taskID)
	}
	return nil
}

func (c *Client) CronDelete(ctx context.Context, taskID string) error {
	cmd := protocol.CronDeleteCommand{Type: "cron_delete", RequestID: c.nextRequestID(), TaskID: taskID}
	var resp protocol.CronDeleteResponse
	if err := c.mgmtRequest(ctx, cmd.RequestID, cmd, &resp); err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("cron_delete failed: %s", orUnknown(resp.Error))
	}
	if !resp.Found {
		return fmt.Errorf("cron task not found: %s", taskID)
	}
	return nil
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown error"
	}
	return s
}
