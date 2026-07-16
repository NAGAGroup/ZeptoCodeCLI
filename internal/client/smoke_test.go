package client

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/protocol"
)

// TestSmoke runs a real end-to-end turn against a spawned app-server.
// Skipped unless ZC_SMOKE_AGENT is set (requires `letta` on PATH and a
// configured local backend). This is the de-facto integration test for the
// protocol + client layers.
func TestSmoke(t *testing.T) {
	agentID := os.Getenv("ZC_SMOKE_AGENT")
	if agentID == "" {
		t.Skip("set ZC_SMOKE_AGENT=<agent-id> to run the smoke test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cli, err := Start(ctx, Options{AgentID: agentID, Port: 8494})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer cli.Close()

	if cli.Runtime.ConversationID == "" {
		t.Fatal("runtime_start returned empty conversation id")
	}
	t.Logf("runtime: %s / %s", cli.Runtime.AgentID, cli.Runtime.ConversationID)

	if err := cli.SendUserMessage("Reply with the single word: pong. Do not use any tools."); err != nil {
		t.Fatalf("SendUserMessage: %v", err)
	}

	var text strings.Builder
	for {
		select {
		case f, ok := <-cli.Frames:
			if !ok {
				t.Fatal("frame channel closed before stop_reason")
			}
			if f.ControlRequest != nil && f.ControlRequest.Request.Subtype == "can_use_tool" {
				t.Logf("denying tool %s", f.ControlRequest.Request.ToolName)
				_ = cli.RespondApproval(f.ControlRequest.RequestID, protocol.ApprovalDecision{
					Behavior: "deny",
					Message:  "smoke test: no tools",
				})
				continue
			}
			if f.StreamDelta == nil {
				continue
			}
			d := &f.StreamDelta.Delta
			switch d.MessageType {
			case "assistant_message":
				text.WriteString(d.Text())
			case "stop_reason":
				got := strings.ToLower(strings.TrimSpace(text.String()))
				t.Logf("assistant said %q, stop_reason=%s", got, d.StopReason)
				if !strings.Contains(got, "pong") {
					t.Errorf("expected pong in assistant reply, got %q", got)
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for turn completion")
		}
	}
}
