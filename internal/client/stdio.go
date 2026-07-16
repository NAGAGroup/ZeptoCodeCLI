// Package client: stdio transport for the zc ui-server protocol.
//
// Spawns `bun ui-server.ts --agent <id>` as a child process and communicates
// over stdin/stdout (JSON-lines). stderr goes to a log writer. This is the
// sole transport for the zc thin-client TUI — the old WebSocket app-server
// transport has been removed.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/protocol"
)

// StdioOptions configures a stdio-based ui-server connection.
type StdioOptions struct {
	Agent          string
	ConversationID string
	Mode           protocol.PermissionMode
	// UIBin is the command to spawn the ui-server. Defaults to "bun".
	UIBin string
	// UIServerPath is the path to ui-server.ts in the letta-code fork.
	UIServerPath string
	// CWD is the working directory for the spawned process.
	CWD string
	// ServerLog receives the spawned process's stderr. Optional.
	ServerLog io.Writer
}

// StdioClient is a client that talks to letta ui-server over stdio.
type StdioClient struct {
	opts    StdioOptions
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.Reader
	Frames  chan protocol.Frame
	Runtime protocol.RuntimeScope

	mu      sync.Mutex
	pending map[string]chan protocol.Frame
	reqSeq  int
	closed  bool
}

// ConnectStdio spawns the ui-server process and performs the hello handshake.
func ConnectStdio(ctx context.Context, opts StdioOptions) (*StdioClient, error) {
	if opts.Agent == "" {
		return nil, fmt.Errorf("stdio: Agent is required")
	}
	if opts.UIBin == "" {
		opts.UIBin = "bun"
	}
	if opts.UIServerPath == "" {
		opts.UIServerPath = os.ExpandEnv("$HOME/projects/letta-code/src/cli/subcommands/ui-server.ts")
	}
	if opts.ServerLog == nil {
		opts.ServerLog = io.Discard
	}

	c := &StdioClient{
		opts:    opts,
		Frames:  make(chan protocol.Frame, 256),
		pending: make(map[string]chan protocol.Frame),
	}

	args := []string{opts.UIServerPath, "--agent", opts.Agent}
	if opts.ConversationID != "" {
		args = append(args, "--conversation", opts.ConversationID)
	}
	if opts.CWD != "" {
		args = append(args, "--cwd", opts.CWD)
	}

	c.cmd = exec.Command(opts.UIBin, args...)
	c.cmd.Stderr = opts.ServerLog

	var err error
	c.stdin, err = c.cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdio: stdin pipe: %w", err)
	}
	c.stdout, err = c.cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdio: stdout pipe: %w", err)
	}

	if err := c.cmd.Start(); err != nil {
		return nil, fmt.Errorf("stdio: start ui-server: %w", err)
	}

	// Start read loop
	go c.readLoop()

	// Hello handshake
	helloReqID := c.nextRequestID()
	hello := map[string]any{
		"type":             "hello",
		"request_id":       helloReqID,
		"protocol_version": 1,
		"client":           map[string]any{"name": "zeptocode-cli", "version": "0.1.0"},
	}
	if err := c.sendRaw(hello); err != nil {
		c.Close()
		return nil, fmt.Errorf("stdio: send hello: %w", err)
	}

	// Wait for hello_response
	helloCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	f, err := c.request(helloCtx, helloReqID, nil)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("stdio: hello handshake: %w", err)
	}

	// Parse hello_response to get runtime info
	if f.HelloResponse != nil {
		c.Runtime = protocol.RuntimeScope{
			AgentID:        f.HelloResponse.AgentID,
			ConversationID: f.HelloResponse.ConversationID,
		}
	}

	return c, nil
}

func (c *StdioClient) readLoop() {
	scanner := bufio.NewScanner(c.stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		debugLogFrame(raw)
		frame, err := protocol.Decode(raw)
		if err != nil {
			continue
		}
		// Correlated response: deliver to pending request channel
		if frame.RequestID != "" {
			c.mu.Lock()
			ch, ok := c.pending[frame.RequestID]
			if ok {
				delete(c.pending, frame.RequestID)
				c.mu.Unlock()
				select {
				case ch <- frame:
				default:
				}
				continue
			}
			c.mu.Unlock()
		}
		// Uncorrelated push frame: deliver to UI
		select {
		case c.Frames <- frame:
		default:
		}
	}
	close(c.Frames)
}

func (c *StdioClient) sendRaw(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("stdio: client closed")
	}
	_, err = c.stdin.Write(append(raw, '\n'))
	return err
}

func (c *StdioClient) request(ctx context.Context, requestID string, v any) (protocol.Frame, error) {
	ch := make(chan protocol.Frame, 1)
	c.mu.Lock()
	c.pending[requestID] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, requestID)
		c.mu.Unlock()
	}()

	if v != nil {
		if err := c.sendRaw(v); err != nil {
			return protocol.Frame{}, err
		}
	}

	select {
	case f := <-ch:
		return f, nil
	case <-ctx.Done():
		return protocol.Frame{}, ctx.Err()
	}
}

func (c *StdioClient) nextRequestID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqSeq++
	return fmt.Sprintf("zc-%d", c.reqSeq)
}

// debugLogFrame writes raw frame bytes to a debug file if ZC_DEBUG_FRAMES is set.
func debugLogFrame(raw []byte) {
	path := os.Getenv("ZC_DEBUG_FRAMES")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(raw)
	f.Write([]byte("\n"))
}

// ── Public API (thin-client methods) ──

// SendMessage sends an input_submit frame (user message or slash command).
func (c *StdioClient) SendMessage(content string) error {
	return c.sendRaw(map[string]any{
		"type":    "input_submit",
		"content": content,
	})
}

// SendInterrupt sends a control_request to abort the current turn.
func (c *StdioClient) SendInterrupt() error {
	return c.sendRaw(map[string]any{
		"type":       "control_request",
		"request_id": c.nextRequestID(),
		"request":    map[string]any{"subtype": "interrupt"},
	})
}

// SendApprovalResponse sends a control_response for a can_use_tool request.
func (c *StdioClient) SendApprovalResponse(requestID, behavior string, updatedInput map[string]any) error {
	resp := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
		},
		"session_id": c.Runtime.AgentID,
	}
	if behavior == "allow" {
		resp["response"].(map[string]any)["response"] = map[string]any{
			"behavior":     "allow",
			"updatedInput": updatedInput,
		}
	} else {
		resp["response"].(map[string]any)["response"] = map[string]any{
			"behavior": "deny",
			"message":  "Denied by user",
		}
	}
	return c.sendRaw(resp)
}

// ExecuteCommand sends an execute_command frame.
func (c *StdioClient) ExecuteCommand(commandID, args string) error {
	m := map[string]any{
		"type":       "execute_command",
		"command_id": commandID,
		"request_id": c.nextRequestID(),
	}
	if args != "" {
		m["args"] = args
	}
	return c.sendRaw(m)
}

// ChangeMode sends a change_device_state frame with a new permission mode.
func (c *StdioClient) ChangeMode(mode protocol.PermissionMode) error {
	return c.sendRaw(map[string]any{
		"type": "change_device_state",
		"mode": string(mode),
	})
}

// ChangeCWD sends a change_device_state frame with a new working directory.
func (c *StdioClient) ChangeCWD(path string) error {
	return c.sendRaw(map[string]any{
		"type": "change_device_state",
		"cwd":  path,
	})
}

// QueryPanels sends a mod_panels_query and returns the response.
func (c *StdioClient) QueryPanels(ctx context.Context, width int) (json.RawMessage, error) {
	reqID := c.nextRequestID()
	q := map[string]any{
		"type":       "mod_panels_query",
		"request_id": reqID,
		"width":      width,
	}
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	f, err := c.request(queryCtx, reqID, q)
	if err != nil {
		return nil, err
	}
	return f.Raw, nil
}

// ListAgents sends an agents_list query and returns the response.
func (c *StdioClient) ListAgents(ctx context.Context) ([]protocol.AgentSummary, error) {
	reqID := c.nextRequestID()
	q := map[string]any{
		"type":       "agents_list",
		"request_id": reqID,
		"limit":      200,
	}
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	f, err := c.request(listCtx, reqID, q)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Agents []protocol.AgentSummary `json:"agents"`
		Error  string                  `json:"error"`
	}
	json.Unmarshal(f.Raw, &resp)
	if resp.Error != "" {
		return nil, fmt.Errorf("agents_list: %s", resp.Error)
	}
	return resp.Agents, nil
}

// ListConversations sends a conversations_list query.
func (c *StdioClient) ListConversations(ctx context.Context, agentID string) ([]protocol.ConversationSummary, error) {
	reqID := c.nextRequestID()
	q := map[string]any{
		"type":       "conversations_list",
		"request_id": reqID,
		"agent_id":   agentID,
		"limit":      200,
	}
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	f, err := c.request(listCtx, reqID, q)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Conversations []protocol.ConversationSummary `json:"conversations"`
		Error         string                         `json:"error"`
	}
	json.Unmarshal(f.Raw, &resp)
	if resp.Error != "" {
		return nil, fmt.Errorf("conversations_list: %s", resp.Error)
	}
	return resp.Conversations, nil
}

// ListModels sends a models_list query and returns the catalog text.
func (c *StdioClient) ListModels(ctx context.Context) (string, error) {
	reqID := c.nextRequestID()
	q := map[string]any{
		"type":       "models_list",
		"request_id": reqID,
	}
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	f, err := c.request(listCtx, reqID, q)
	if err != nil {
		return "", err
	}
	var resp struct {
		Models string `json:"models"`
		Error  string `json:"error"`
	}
	json.Unmarshal(f.Raw, &resp)
	return resp.Models, nil
}

// UpdateModel sends an update_model frame.
func (c *StdioClient) UpdateModel(ctx context.Context, modelID, modelHandle string) error {
	reqID := c.nextRequestID()
	m := map[string]any{
		"type":       "update_model",
		"request_id": reqID,
	}
	if modelID != "" {
		m["model_id"] = modelID
	}
	if modelHandle != "" {
		m["model_handle"] = modelHandle
	}
	updCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	f, err := c.request(updCtx, reqID, m)
	if err != nil {
		return err
	}
	var resp struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	json.Unmarshal(f.Raw, &resp)
	if !resp.Success && resp.Error != "" {
		return fmt.Errorf("update_model: %s", resp.Error)
	}
	return nil
}

// ListMessages sends a conversation_messages_list query.
func (c *StdioClient) ListMessages(ctx context.Context) ([]protocol.Delta, error) {
	reqID := c.nextRequestID()
	q := map[string]any{
		"type":            "conversation_messages_list",
		"request_id":      reqID,
		"conversation_id": c.Runtime.ConversationID,
	}
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	f, err := c.request(listCtx, reqID, q)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Messages []protocol.Delta `json:"messages"`
		Error    string           `json:"error"`
	}
	json.Unmarshal(f.Raw, &resp)
	return resp.Messages, nil
}

// Fork sends a conversation_fork request and returns the new conversation id.
func (c *StdioClient) Fork(ctx context.Context) (string, error) {
	reqID := c.nextRequestID()
	q := map[string]any{
		"type":       "conversation_fork",
		"request_id": reqID,
	}
	forkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	f, err := c.request(forkCtx, reqID, q)
	if err != nil {
		return "", err
	}
	var resp struct {
		Success        bool   `json:"success"`
		ConversationID string `json:"conversation_id"`
		Error          string `json:"error"`
	}
	json.Unmarshal(f.Raw, &resp)
	if !resp.Success {
		return "", fmt.Errorf("fork: %s", resp.Error)
	}
	return resp.ConversationID, nil
}

// UpdateConversation sends a conversation_update frame.
func (c *StdioClient) UpdateConversation(ctx context.Context, body map[string]any) error {
	reqID := c.nextRequestID()
	m := map[string]any{
		"type":            "conversation_update",
		"request_id":      reqID,
		"conversation_id": c.Runtime.ConversationID,
	}
	for k, v := range body {
		m[k] = v
	}
	updCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	f, err := c.request(updCtx, reqID, m)
	if err != nil {
		return err
	}
	var resp struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	json.Unmarshal(f.Raw, &resp)
	if !resp.Success && resp.Error != "" {
		return fmt.Errorf("conversation_update: %s", resp.Error)
	}
	return nil
}

// UpdateAgent sends an agent_update frame.
func (c *StdioClient) UpdateAgent(ctx context.Context, body map[string]any) error {
	reqID := c.nextRequestID()
	m := map[string]any{
		"type":       "agent_update",
		"request_id": reqID,
		"updates":    body,
	}
	updCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	f, err := c.request(updCtx, reqID, m)
	if err != nil {
		return err
	}
	var resp struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	json.Unmarshal(f.Raw, &resp)
	if !resp.Success && resp.Error != "" {
		return fmt.Errorf("agent_update: %s", resp.Error)
	}
	return nil
}

// AgentRetrieve sends an agent_retrieve query.
func (c *StdioClient) AgentRetrieve(ctx context.Context) (name, model string, err error) {
	reqID := c.nextRequestID()
	q := map[string]any{
		"type":       "agent_retrieve",
		"request_id": reqID,
	}
	retCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	f, err := c.request(retCtx, reqID, q)
	if err != nil {
		return "", "", err
	}
	var resp struct {
		Agent struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"agent"`
		Error string `json:"error"`
	}
	json.Unmarshal(f.Raw, &resp)
	if resp.Error != "" {
		return "", "", fmt.Errorf("agent_retrieve: %s", resp.Error)
	}
	return resp.Agent.Name, resp.Agent.Model, nil
}

// ── Lifecycle ──

// Close kills the spawned process and cleans up.
func (c *StdioClient) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()

	if c.stdin != nil {
		c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		c.cmd.Process.Kill()
		c.cmd.Wait()
	}
}

// SetMode sets the permission mode (stored for future use).
func (c *StdioClient) SetMode(mode protocol.PermissionMode) {
	c.opts.Mode = mode
}

// SetConversation sets the conversation ID (stored for future use).
func (c *StdioClient) SetConversation(conversationID string) {
	c.opts.ConversationID = conversationID
}

// GetOpts returns the current options (for reconnect with new agent/conv).
func (c *StdioClient) GetOpts() StdioOptions {
	return c.opts
}

// IsClosed reports whether the client has been closed.
func (c *StdioClient) IsClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}
