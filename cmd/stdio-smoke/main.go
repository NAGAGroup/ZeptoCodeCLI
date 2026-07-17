// stdio-smoke: a headless driver for the new stdio ui-server transport.
// It spawns the ui-server agentless, walks the lobby (auto-picks the first
// agent + a new conversation), submits one message, and prints every decoded
// frame's type until the turn returns to idle. This exercises the Stage 0/1
// reducer path without a terminal.
//
//	ZC_UI_BIN=/path/to/bun ZC_UI_SERVER=/path/to/ui-server.ts \
//	  [ZC_SMOKE_AGENT=<id>] go run ./cmd/stdio-smoke
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/client"
	"github.com/NAGAGroup/ZeptoCodeCLI/internal/protocol"
)

func main() {
	uiBin := os.Getenv("ZC_UI_BIN")
	if uiBin == "" {
		uiBin = "bun"
	}
	uiServer := os.Getenv("ZC_UI_SERVER")
	if uiServer == "" {
		uiServer = os.ExpandEnv("$HOME/projects/letta-code/src/cli/subcommands/ui-server.ts")
	}

	logFile, _ := os.Create("/tmp/stdio-smoke-server.log")
	defer logFile.Close()

	c, err := client.Spawn(client.Options{
		UIBin:          uiBin,
		UIServerPath:   uiServer,
		Agent:          os.Getenv("ZC_SMOKE_AGENT"),
		ConversationID: os.Getenv("ZC_SMOKE_CONVERSATION"),
		ServerLog:      logFile,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "spawn error: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	deadline := time.After(90 * time.Second)
	phase := ""
	pickedAgent := false
	pickedConv := false
	submitted := false

	for {
		select {
		case <-deadline:
			fmt.Fprintln(os.Stderr, "timeout")
			return
		case f, ok := <-c.Frames():
			if !ok {
				fmt.Fprintln(os.Stderr, "frames channel closed")
				return
			}
			switch fr := f.(type) {
			case *protocol.HelloResponse:
				fmt.Printf("hello_response: build=%s version=%s err=%q\n", fr.RuntimeBuild, fr.LettaCodeVer, fr.Error)
			case *protocol.SessionPhase:
				phase = fr.Phase
				fmt.Printf("session_phase: %s\n", fr.Phase)
			case *protocol.Agents:
				fmt.Printf("agents: %d\n", len(fr.Items))
				if !pickedAgent && phase == "lobby" && len(fr.Items) > 0 {
					pickedAgent = true
					fmt.Printf("  -> select_agent %s\n", fr.Items[0].ID)
					c.Send(protocol.NewSelectAgent(fr.Items[0].ID))
				}
			case *protocol.Conversations:
				fmt.Printf("conversations: %d\n", len(fr.Items))
				if !pickedConv && phase == "lobby" {
					pickedConv = true
					fmt.Println("  -> select_conversation new")
					c.Send(protocol.NewSelectConversation("new"))
				}
			case *protocol.DeviceState:
				fmt.Printf("device: cwd=%s mode=%s model=%s agent=%s\n",
					fr.CWD, fr.PermissionMode, fr.Model.Handle, fr.Agent.Name)
			case *protocol.Transcript:
				fmt.Printf("transcript: %d lines\n", len(fr.Lines))
				for _, l := range fr.Lines {
					fmt.Printf("  [%s] %.60s\n", l.Kind, oneLine(l.Text))
				}
			case *protocol.TurnState:
				fmt.Printf("turn: %s %s\n", fr.Status, fr.StopReason)
				if fr.Status == "idle" {
					if !submitted && phase == "chat" {
						submitted = true
						fmt.Println("  -> input_submit")
						c.Send(protocol.NewInputSubmit("Say hello in exactly three words."))
					} else if submitted {
						fmt.Println("done.")
						return
					}
				}
			case *protocol.PendingApprovals:
				fmt.Printf("pending_approvals: %d\n", len(fr.Items))
			case *protocol.Toast:
				fmt.Printf("toast[%s]: %s\n", fr.Level, fr.Message)
			case *protocol.ErrorFrame:
				fmt.Printf("error: %s\n", fr.Message)
			case *protocol.ModPanels:
				fmt.Printf("mod_panels: %d\n", len(fr.Panels))
			case client.Disconnected:
				fmt.Fprintf(os.Stderr, "disconnected: %v\n", fr.Err)
				return
			case *protocol.Unknown:
				fmt.Printf("unknown: %s\n", fr.Type)
			default:
				fmt.Printf("frame: %T\n", f)
			}
		}
	}
}

func oneLine(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' {
			r = ' '
		}
		out = append(out, r)
	}
	return string(out)
}
