// Protocol de-risking spike for ZeptoCodeCLI.
//
// Spawns a dedicated `letta app-server` on a loopback port, connects the
// control + stream WebSocket channels, starts a runtime on a fresh
// conversation for the given agent, sends one user message, and dumps every
// protocol frame to stdout. Denies any tool-approval requests.
//
// Run: pixi run spike -- --agent <agent-id> [--message "..."] [--port 8493]
//
// Success criteria: runtime_start round-trips, stream_delta frames render,
// and the turn ends with a stop_reason frame.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/coder/websocket"
)

type frame = map[string]any

type taggedFrame struct {
	channel string
	data    frame
	raw     string
}

func main() {
	agentID := flag.String("agent", "", "agent id to run the turn against (required)")
	message := flag.String("message", "Reply with the single word: pong. Do not use any tools.", "user message to send")
	port := flag.Int("port", 8493, "loopback port for the spawned app-server")
	timeout := flag.Duration("timeout", 3*time.Minute, "overall spike timeout")
	flag.Parse()

	if *agentID == "" {
		fmt.Fprintln(os.Stderr, "usage: spike --agent <agent-id>")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	listen := fmt.Sprintf("ws://127.0.0.1:%d", *port)

	// 1. Spawn a dedicated app-server. Loopback ⇒ no auth required.
	logf("spawning: letta app-server --listen %s", listen)
	cmd := exec.Command("letta", "app-server", "--listen", listen)
	cmd.Stdout = prefixWriter("[app-server] ")
	cmd.Stderr = prefixWriter("[app-server!] ")
	if err := cmd.Start(); err != nil {
		fatal("failed to start app-server: %v", err)
	}
	defer func() {
		logf("terminating app-server (pid %d)", cmd.Process.Pid)
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// 2. Connect control first (claims the seat), then stream — same order as
	// the ZeptoCode broker.
	control := dialWithRetry(ctx, listen+"/ws?channel=control")
	defer control.Close(websocket.StatusNormalClosure, "spike done")
	logf("control channel connected")
	stream := dialWithRetry(ctx, listen+"/ws?channel=stream")
	defer stream.Close(websocket.StatusNormalClosure, "spike done")
	logf("stream channel connected")

	frames := make(chan taggedFrame, 64)
	go readLoop(ctx, "control", control, frames)
	go readLoop(ctx, "stream", stream, frames)

	send := func(f frame) {
		raw, err := json.Marshal(f)
		if err != nil {
			fatal("marshal: %v", err)
		}
		if err := control.Write(ctx, websocket.MessageText, raw); err != nil {
			fatal("write: %v", err)
		}
		logf(">> %s", compact(raw, 200))
	}

	// 3. runtime_start on a fresh conversation, correlated by request_id.
	const startReqID = "spike-1"
	send(frame{
		"type":                "runtime_start",
		"request_id":          startReqID,
		"agent_id":            *agentID,
		"create_conversation": frame{"body": frame{}},
		"client_info": frame{
			"name":    "zeptocode-cli-spike",
			"title":   "ZeptoCodeCLI spike",
			"version": "0.1.0",
		},
	})

	var rtAgent, rtConv string
	for rtConv == "" {
		tf := nextFrame(ctx, frames)
		printFrame(tf)
		if tf.data["request_id"] == startReqID {
			if ok, _ := tf.data["success"].(bool); !ok {
				fatal("runtime_start failed: %v", tf.data["error"])
			}
			rt, _ := tf.data["runtime"].(map[string]any)
			rtAgent, _ = rt["agent_id"].(string)
			rtConv, _ = rt["conversation_id"].(string)
		}
	}
	logf("runtime started: agent=%s conversation=%s", rtAgent, rtConv)

	// 4. One user message.
	send(frame{
		"type":    "input",
		"runtime": frame{"agent_id": rtAgent, "conversation_id": rtConv},
		"payload": frame{
			"kind":     "create_message",
			"messages": []frame{{"role": "user", "content": *message}},
		},
	})

	// 5. Pump frames until the turn's stop_reason arrives.
	var assistantText strings.Builder
	for {
		tf := nextFrame(ctx, frames)
		printFrame(tf)

		if tf.data["type"] == "control_request" {
			req, _ := tf.data["request"].(map[string]any)
			if req["subtype"] == "can_use_tool" {
				logf("denying tool approval for %v", req["tool_name"])
				send(frame{
					"type":    "input",
					"runtime": frame{"agent_id": rtAgent, "conversation_id": rtConv},
					"payload": frame{
						"kind":       "approval_response",
						"request_id": tf.data["request_id"],
						"decision": frame{
							"behavior": "deny",
							"message":  "Spike run: no tools permitted. Answer directly.",
						},
					},
				})
			}
			continue
		}

		if tf.data["type"] != "stream_delta" {
			continue
		}
		delta, _ := tf.data["delta"].(map[string]any)
		switch delta["message_type"] {
		case "assistant_message":
			assistantText.WriteString(extractText(delta["content"]))
		case "stop_reason":
			logf("turn finished: stop_reason=%v", delta["stop_reason"])
			logf("assistant said: %q", assistantText.String())
			logf("SPIKE OK")
			return
		}
	}
}

func dialWithRetry(ctx context.Context, url string) *websocket.Conn {
	deadline := time.Now().Add(30 * time.Second)
	for {
		dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		conn, _, err := websocket.Dial(dialCtx, url, nil)
		cancel()
		if err == nil {
			conn.SetReadLimit(16 << 20)
			return conn
		}
		if time.Now().After(deadline) {
			fatal("could not connect to %s within 30s: %v", url, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func readLoop(ctx context.Context, channel string, conn *websocket.Conn, out chan<- taggedFrame) {
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			logf("%s channel closed: %v", channel, err)
			return
		}
		var f frame
		if err := json.Unmarshal(raw, &f); err != nil {
			logf("%s: unparseable frame: %s", channel, compact(raw, 120))
			continue
		}
		out <- taggedFrame{channel: channel, data: f, raw: string(raw)}
	}
}

func nextFrame(ctx context.Context, frames <-chan taggedFrame) taggedFrame {
	select {
	case tf := <-frames:
		return tf
	case <-ctx.Done():
		fatal("timed out waiting for frames")
		panic("unreachable")
	}
}

func printFrame(tf taggedFrame) {
	kind, _ := tf.data["type"].(string)
	detail := ""
	if kind == "stream_delta" {
		if delta, ok := tf.data["delta"].(map[string]any); ok {
			detail = fmt.Sprintf(" message_type=%v", delta["message_type"])
			if delta["message_type"] == "assistant_message" {
				detail += fmt.Sprintf(" text=%q", extractText(delta["content"]))
			}
		}
	}
	logf("<< [%s] %s%s | %s", tf.channel, kind, detail, compact([]byte(tf.raw), 160))
}

// extractText handles content as plain string or [{type:"text",text:"..."}].
func extractText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, part := range c {
			if m, ok := part.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	return ""
}

func compact(raw []byte, max int) string {
	s := string(raw)
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func prefixWriter(prefix string) *lineWriter { return &lineWriter{prefix: prefix} }

type lineWriter struct {
	prefix string
	buf    strings.Builder
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		s := w.buf.String()
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			break
		}
		fmt.Printf("%s%s\n", w.prefix, s[:i])
		w.buf.Reset()
		w.buf.WriteString(s[i+1:])
	}
	return len(p), nil
}

func logf(format string, args ...any) {
	fmt.Printf("[spike] "+format+"\n", args...)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[spike] FATAL: "+format+"\n", args...)
	os.Exit(1)
}
