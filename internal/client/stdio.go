// Package client is the stdio transport to `letta ui-server`.
//
// The transport is a pure state-push pipe: the child pushes JSON state
// frames on stdout (one per line), which the read loop decodes and delivers
// on Frames() verbatim; the client sends discrete fire-and-forget events with
// Send(). There is NO request/response correlation, NO pending maps, NO
// blocking request(), NO IO inside the caller's Update — all child IO lives
// in the read goroutine and in Send. stderr goes to a log writer.
package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/protocol"
)

// Options configures a ui-server child.
type Options struct {
	// UIBin is the command to spawn the ui-server (e.g. "bun"). Required.
	UIBin string
	// UIServerPath is the path to ui-server.ts in the letta-code fork.
	UIServerPath string
	// Agent, if set, is passed as --agent to pre-answer the lobby agent step.
	Agent string
	// ConversationID, if set, is passed as --conversation to pre-answer the
	// lobby conversation step ("new" creates a fresh conversation).
	ConversationID string
	// CWD, if set, is passed as --cwd.
	CWD string
	// ServerLog receives the child's stderr. Optional (defaults to discard).
	ServerLog io.Writer
}

// Disconnected is the sentinel value delivered on Frames() once (right before
// the channel closes) when the child exits.
type Disconnected struct{ Err error }

// Client owns the spawned ui-server child and its stdio pipes.
type Client struct {
	opts   Options
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	frames chan any

	mu     sync.Mutex
	closed bool
}

// Spawn starts the ui-server child, begins the read loop, and sends the
// hello handshake (fire-and-forget — the hello_response arrives as a normal
// frame). It does not block waiting for any reply.
func Spawn(opts Options) (*Client, error) {
	if opts.UIBin == "" {
		return nil, fmt.Errorf("client: UIBin is required")
	}
	if opts.UIServerPath == "" {
		return nil, fmt.Errorf("client: UIServerPath is required")
	}
	if opts.ServerLog == nil {
		opts.ServerLog = io.Discard
	}

	c := &Client{
		opts:   opts,
		frames: make(chan any, 256),
	}

	args := []string{opts.UIServerPath}
	if opts.Agent != "" {
		args = append(args, "--agent", opts.Agent)
	}
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
		return nil, fmt.Errorf("client: stdin pipe: %w", err)
	}
	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("client: stdout pipe: %w", err)
	}
	if err := c.cmd.Start(); err != nil {
		return nil, fmt.Errorf("client: start ui-server: %w", err)
	}

	go c.readLoop(stdout)

	// Fire-and-forget handshake. The hello_response comes back as a frame.
	if err := c.Send(protocol.NewHello("zeptocode-cli", "0.1.0")); err != nil {
		c.Close()
		return nil, fmt.Errorf("client: send hello: %w", err)
	}
	return c, nil
}

// readLoop decodes frames and delivers them on the channel, NEVER dropping a
// frame (it blocks on the channel send). On child exit it delivers a
// Disconnected sentinel then closes the channel.
func (c *Client) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		debugLogFrame(raw)
		frame, err := protocol.Decode(raw)
		if err != nil {
			// Not JSON; ignore this line but keep reading.
			continue
		}
		c.frames <- frame // block; never drop
	}
	err := c.cmd.Wait()
	c.frames <- Disconnected{Err: err}
	close(c.frames)
}

// Frames returns the read-only channel of decoded frames. The final value is
// a Disconnected sentinel, after which the channel is closed.
func (c *Client) Frames() <-chan any { return c.frames }

// Send marshals a client event and writes it as one JSON line. It never waits
// for a reply.
func (c *Client) Send(event any) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("client: closed")
	}
	_, err = c.stdin.Write(append(raw, '\n'))
	return err
}

// Close terminates the child and its pipes.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	if c.stdin != nil {
		c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		c.cmd.Process.Kill()
	}
	return nil
}

// debugLogFrame appends raw frame bytes to $ZC_DEBUG_FRAMES when set.
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
