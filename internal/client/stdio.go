// Package client: stdio transport for the zc ui-server protocol.
//
// Spawns `bun ui-server.ts --agent <id>` as a child process and communicates
// over stdin/stdout (JSON-lines). stderr goes to a log writer. This replaces
// the WebSocket app-server transport for the zc protocol seam.
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
		// Default: the fork at ~/projects/letta-code
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
	var hr struct {
		Type            string `json:"type"`
		AgentID         string `json:"agent_id"`
		AgentName       string `json:"agent_name"`
		ConversationID  string `json:"conversation_id"`
		Model           string `json:"model"`
		LettaCodeVersion string `json:"letta_code_version"`
	}
	json.Unmarshal(f.Raw, &hr)

	c.Runtime = protocol.RuntimeScope{
		AgentID:        hr.AgentID,
		ConversationID: hr.ConversationID,
	}

	// Consume device_status frame (it arrives right after hello_response)
	// Non-blocking — it'll come through the Frames channel if no pending request
	go func() {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return
		}
	}()

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
		if frame.RequestID != "" {
			c.mu.Lock()
			ch, ok := c.pending[frame.RequestID]
			if ok && frame.Type != "control_request" {
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
	_, err = c.stdin.Write(append(raw, '\n'))
	return err
}

func (c *StdioClient) send(v any) error {
	return c.sendRaw(v)
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
		if err := c.send(v); err != nil {
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

// SendMessage sends an input_submit frame to the server.
func (c *StdioClient) SendMessage(content string) error {
	return c.send(map[string]any{
		"type":    "input_submit",
		"content": content,
	})
}

// SendInterrupt sends a control_request interrupt to abort the current turn.
func (c *StdioClient) SendInterrupt() error {
	return c.send(map[string]any{
		"type":       "control_request",
		"request_id": c.nextRequestID(),
		"request":    map[string]any{"subtype": "interrupt"},
	})
}

// SendApprovalResponse sends a control_response for a can_use_tool request.
func (c *StdioClient) SendApprovalResponse(requestID, behavior string, updatedInput map[string]any) error {
	resp := map[string]any{
		"type":       "control_response",
		"response":   map[string]any{
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
	return c.send(resp)
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

// Close kills the spawned process and cleans up.
func (c *StdioClient) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()

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
