// Package client manages a connection to a Letta Code app-server: spawning a
// dedicated server on loopback, holding the control + stream channels,
// correlating request/response frames, and exposing inbound frames on a
// single channel for the UI layer.
//
// Connection order matters and mirrors the reference clients (letta-broker,
// @letta-ai/letta-code/app-server-client): control first — it claims the
// single seat — then stream.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/protocol"
)

type Options struct {
	// Agent is the agent id OR agent name to start the runtime for. Required.
	// Names are resolved via agent_list with a case-insensitive exact match.
	Agent string
	// ConversationID to resume; empty means create a fresh conversation.
	ConversationID string
	// Port for the spawned app-server (loopback ⇒ no auth needed).
	Port int
	// Mode is the initial permission mode for the runtime. Empty keeps the
	// server default (NB: letta-code's default is "unrestricted").
	Mode protocol.PermissionMode
	// LettaBin is the letta executable name/path. Defaults to "letta".
	LettaBin string
	// ServerLog receives the spawned app-server's stdout/stderr. Optional.
	ServerLog io.Writer
}

type Client struct {
	opts    Options
	cmd     *exec.Cmd
	control *websocket.Conn
	stream  *websocket.Conn

	// Frames delivers every inbound frame that is not consumed by a pending
	// request correlation. Closed when both sockets are gone.
	Frames chan protocol.Frame

	Runtime protocol.RuntimeScope

	mu      sync.Mutex
	pending map[string]chan protocol.Frame
	reqSeq  int
	closed  bool
}

// Start spawns the app-server, connects both channels, and starts the runtime.
func Start(ctx context.Context, opts Options) (*Client, error) {
	if opts.Agent == "" {
		return nil, fmt.Errorf("client: Agent is required")
	}
	c, err := Connect(ctx, opts)
	if err != nil {
		return nil, err
	}
	if err := c.StartRuntime(ctx, opts.Agent); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Connect spawns the app-server and connects both channels without starting
// a runtime — callers can list agents first (e.g. an interactive picker),
// then call StartRuntime.
func Connect(ctx context.Context, opts Options) (*Client, error) {
	if opts.Port == 0 {
		opts.Port = 8493
	}
	if opts.LettaBin == "" {
		opts.LettaBin = "letta"
	}
	if opts.ServerLog == nil {
		opts.ServerLog = io.Discard
	}

	c := &Client{
		opts:    opts,
		Frames:  make(chan protocol.Frame, 256),
		pending: make(map[string]chan protocol.Frame),
	}

	listen := fmt.Sprintf("ws://127.0.0.1:%d", opts.Port)
	c.cmd = exec.Command(opts.LettaBin, "app-server", "--listen", listen)
	c.cmd.Stdout = opts.ServerLog
	c.cmd.Stderr = opts.ServerLog
	if err := c.cmd.Start(); err != nil {
		return nil, fmt.Errorf("client: start app-server: %w", err)
	}

	var err error
	if c.control, err = dialRetry(ctx, listen+"/ws?channel=control"); err != nil {
		c.Close()
		return nil, err
	}
	if c.stream, err = dialRetry(ctx, listen+"/ws?channel=stream"); err != nil {
		c.Close()
		return nil, err
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c.readLoop(c.control) }()
	go func() { defer wg.Done(); c.readLoop(c.stream) }()
	go func() { wg.Wait(); close(c.Frames) }()

	return c, nil
}

// StartRuntime resolves agent (name or id) and starts the runtime for it.
func (c *Client) StartRuntime(ctx context.Context, agent string) error {
	agentID := agent
	if !looksLikeAgentID(agentID) {
		resolved, err := c.resolveAgentName(ctx, agentID)
		if err != nil {
			return err
		}
		agentID = resolved
	}
	return c.runtimeStart(ctx, agentID)
}

// ListAgents returns agents known to the server.
func (c *Client) ListAgents(ctx context.Context) ([]protocol.AgentSummary, error) {
	cmd := protocol.AgentListCommand{Type: "agent_list", RequestID: c.nextRequestID()}
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	f, err := c.request(listCtx, cmd.RequestID, cmd)
	if err != nil {
		return nil, fmt.Errorf("client: agent_list: %w", err)
	}
	resp := f.AgentList
	if resp == nil || !resp.Success {
		msg := "unknown error"
		if resp != nil && resp.Error != "" {
			msg = resp.Error
		}
		return nil, fmt.Errorf("client: agent_list failed: %s", msg)
	}
	return resp.Agents, nil
}

// looksLikeAgentID reports whether s is an agent id rather than a name.
// Local and cloud ids both start with "agent-".
func looksLikeAgentID(s string) bool {
	return strings.HasPrefix(s, "agent-")
}

// resolveAgentName resolves an agent name to its id via agent_list.
// Matching is case-insensitive and exact; ambiguity is an error.
func (c *Client) resolveAgentName(ctx context.Context, name string) (string, error) {
	agents, err := c.ListAgents(ctx)
	if err != nil {
		return "", err
	}
	var matches []protocol.AgentSummary
	var names []string
	for _, a := range agents {
		names = append(names, a.Name)
		if strings.EqualFold(a.Name, name) {
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].ID, nil
	case 0:
		return "", fmt.Errorf("client: no agent named %q (available: %s)",
			name, strings.Join(names, ", "))
	default:
		var ids []string
		for _, m := range matches {
			ids = append(ids, m.ID)
		}
		return "", fmt.Errorf("client: agent name %q is ambiguous, use an id: %s",
			name, strings.Join(ids, ", "))
	}
}

func dialRetry(ctx context.Context, url string) (*websocket.Conn, error) {
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		conn, _, err := websocket.Dial(dialCtx, url, nil)
		cancel()
		if err == nil {
			conn.SetReadLimit(16 << 20)
			return conn, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("client: connect %s: %w", url, lastErr)
}

func (c *Client) readLoop(conn *websocket.Conn) {
	ctx := context.Background()
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}
		frame, err := protocol.Decode(raw)
		if err != nil {
			continue
		}
		if frame.RequestID != "" {
			c.mu.Lock()
			ch, ok := c.pending[frame.RequestID]
			if ok && frame.Type != "control_request" {
				// control_request carries a request_id the SERVER wants
				// answered — never swallow it as one of our replies.
				delete(c.pending, frame.RequestID)
				c.mu.Unlock()
				ch <- frame
				continue
			}
			c.mu.Unlock()
		}
		c.Frames <- frame
	}
}

func (c *Client) send(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.control.Write(ctx, websocket.MessageText, raw)
}

// request sends a command carrying request_id and waits for the correlated
// response frame.
func (c *Client) request(ctx context.Context, requestID string, v any) (protocol.Frame, error) {
	ch := make(chan protocol.Frame, 1)
	c.mu.Lock()
	c.pending[requestID] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, requestID)
		c.mu.Unlock()
	}()
	if err := c.send(v); err != nil {
		return protocol.Frame{}, err
	}
	select {
	case f := <-ch:
		return f, nil
	case <-ctx.Done():
		return protocol.Frame{}, ctx.Err()
	}
}

func (c *Client) nextRequestID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqSeq++
	return fmt.Sprintf("zc-%d", c.reqSeq)
}

func (c *Client) runtimeStart(ctx context.Context, agentID string) error {
	cmd := protocol.RuntimeStartCommand{
		Type:      "runtime_start",
		RequestID: c.nextRequestID(),
		AgentID:   agentID,
		Mode:      c.opts.Mode,
		ClientInfo: &protocol.ClientInfo{
			Name:    "zeptocode-cli",
			Title:   "ZeptoCodeCLI",
			Version: "0.1.0",
		},
	}
	if c.opts.ConversationID != "" {
		cmd.ConversationID = c.opts.ConversationID
	} else {
		cmd.CreateConversation = &protocol.CreateConversationOptions{Body: map[string]any{}}
	}
	startCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	f, err := c.request(startCtx, cmd.RequestID, cmd)
	if err != nil {
		return fmt.Errorf("client: runtime_start: %w", err)
	}
	resp := f.RuntimeStartResponse
	if resp == nil || !resp.Success || resp.Runtime == nil {
		msg := "unknown error"
		if resp != nil && resp.Error != "" {
			msg = resp.Error
		}
		return fmt.Errorf("client: runtime_start failed: %s", msg)
	}
	c.Runtime = *resp.Runtime
	return nil
}

// MessagesList fetches the conversation transcript (LettaMessage history).
func (c *Client) MessagesList(ctx context.Context) ([]protocol.Delta, error) {
	cmd := protocol.ConversationMessagesListCommand{
		Type:           "conversation_messages_list",
		RequestID:      c.nextRequestID(),
		ConversationID: c.Runtime.ConversationID,
	}
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	f, err := c.request(listCtx, cmd.RequestID, cmd)
	if err != nil {
		return nil, fmt.Errorf("client: conversation_messages_list: %w", err)
	}
	resp := f.MessagesList
	if resp == nil || !resp.Success {
		msg := "unknown error"
		if resp != nil && resp.Error != "" {
			msg = resp.Error
		}
		return nil, fmt.Errorf("client: conversation_messages_list failed: %s", msg)
	}
	return resp.Messages, nil
}

// ConversationsList fetches conversations for the current agent (the local
// backend filters by agent_id and defaults to limit 20 — raise it).
func (c *Client) ConversationsList(ctx context.Context) ([]protocol.ConversationSummary, error) {
	cmd := protocol.ConversationListCommand{
		Type:      "conversation_list",
		RequestID: c.nextRequestID(),
		Query: map[string]any{
			"agent_id": c.Runtime.AgentID,
			"limit":    200,
		},
	}
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	f, err := c.request(listCtx, cmd.RequestID, cmd)
	if err != nil {
		return nil, fmt.Errorf("client: conversation_list: %w", err)
	}
	resp := f.ConversationList
	if resp == nil || !resp.Success {
		msg := "unknown error"
		if resp != nil && resp.Error != "" {
			msg = resp.Error
		}
		return nil, fmt.Errorf("client: conversation_list failed: %s", msg)
	}
	return resp.Conversations, nil
}

// ExecuteCommand dispatches a slash command; output arrives as
// command_start/command_end stream deltas.
func (c *Client) ExecuteCommand(commandID, args string) error {
	return c.send(protocol.ExecuteCommandCommand{
		Type:      "execute_command",
		CommandID: commandID,
		RequestID: c.nextRequestID(),
		Runtime:   c.Runtime,
		Args:      args,
	})
}

// ChangeMode switches the runtime's permission mode; confirmation arrives
// via update_device_status.
func (c *Client) ChangeMode(mode protocol.PermissionMode) error {
	return c.send(protocol.ChangeDeviceStateCommand{
		Type:    "change_device_state",
		Runtime: c.Runtime,
		Payload: protocol.ChangeDeviceStatePayload{Mode: mode},
	})
}

// SetMode sets the permission mode used for subsequent runtime_starts.
func (c *Client) SetMode(mode protocol.PermissionMode) {
	c.opts.Mode = mode
}

// SwitchConversation restarts the runtime on a different conversation
// (empty id ⇒ create a new conversation) and updates c.Runtime.
func (c *Client) SwitchConversation(ctx context.Context, conversationID string) error {
	c.opts.ConversationID = conversationID
	return c.runtimeStart(ctx, c.Runtime.AgentID)
}

// SendUserMessage submits a user turn.
func (c *Client) SendUserMessage(text string) error {
	return c.send(protocol.NewUserMessage(c.Runtime, text))
}

// RespondApproval answers a can_use_tool control request.
func (c *Client) RespondApproval(requestID string, decision protocol.ApprovalDecision) error {
	return c.send(protocol.NewApprovalResponse(c.Runtime, requestID, decision))
}

// Abort requests cancellation of the active turn.
func (c *Client) Abort() error {
	return c.send(protocol.AbortMessageCommand{Type: "abort_message", Runtime: c.Runtime})
}

// Close tears down sockets and the spawned app-server.
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()

	if c.control != nil {
		_ = c.control.Close(websocket.StatusNormalClosure, "client closing")
	}
	if c.stream != nil {
		_ = c.stream.Close(websocket.StatusNormalClosure, "client closing")
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_, _ = c.cmd.Process.Wait()
	}
}

// OpenServerLog is a helper for callers that want the app-server log on disk.
func OpenServerLog() (*os.File, string, error) {
	path := fmt.Sprintf("%s/zeptocode-cli-appserver.log", os.TempDir())
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	return f, path, err
}
