package client

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/protocol"
)

// TestSmokeApproval verifies the can_use_tool → approval_response wire path:
// provoke a tool call, deny it, and confirm the turn still completes.
// These are exactly the client calls the TUI approval modal makes.
func TestSmokeApproval(t *testing.T) {
	agentID := os.Getenv("ZC_SMOKE_AGENT")
	if agentID == "" {
		t.Skip("set ZC_SMOKE_AGENT=<agent-id> to run the smoke test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// standard mode: letta-code's server default is "unrestricted", which
	// auto-allows every tool and never emits can_use_tool.
	cli, err := Start(ctx, Options{Agent: agentID, Port: 8495, Mode: protocol.ModeStandard})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer cli.Close()

	// NB: the command must have a write side effect. Read-only commands
	// (echo, ls, …) are auto-allowed server-side by letta-code's permission
	// analyzer and never generate a can_use_tool control_request.
	err = cli.SendUserMessage(
		"Use the Bash tool to run this exact command: touch /tmp/zc_smoke_probe.txt. You must actually attempt the tool call before responding.")
	if err != nil {
		t.Fatalf("SendUserMessage: %v", err)
	}

	denied := false
	for {
		select {
		case f, ok := <-cli.Frames:
			if !ok {
				t.Fatal("frame channel closed before stop_reason")
			}
			if f.ControlRequest != nil && f.ControlRequest.Request.Subtype == "can_use_tool" {
				t.Logf("control_request for %s (tool_call_id=%s, %d permission suggestions)",
					f.ControlRequest.Request.ToolName,
					f.ControlRequest.Request.ToolCallID,
					len(f.ControlRequest.Request.PermissionSuggestions))
				if err := cli.RespondApproval(f.ControlRequest.RequestID, protocol.ApprovalDecision{
					Behavior: "deny",
					Message:  "smoke test: denied on purpose",
				}); err != nil {
					t.Fatalf("RespondApproval: %v", err)
				}
				denied = true
				continue
			}
			if f.StreamDelta != nil && f.StreamDelta.Delta.MessageType == "stop_reason" {
				// requires_approval is NOT terminal: the listener records it
				// just before emitting control_request, then resumes the turn
				// after our approval_response (see letta-code
				// src/websocket/listener/approval.ts).
				if f.StreamDelta.Delta.StopReason == "requires_approval" {
					continue
				}
				if !denied {
					t.Error("turn finished without any can_use_tool control_request")
				}
				t.Logf("turn completed after denial, stop_reason=%s", f.StreamDelta.Delta.StopReason)
				return
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for turn completion")
		}
	}
}
