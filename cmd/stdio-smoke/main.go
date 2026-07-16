// stdio-smoke: integration test for the stdio ui-server transport.
// Spawns the ui-server, sends hello + input_submit, prints all frames.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/client"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logFile, _ := os.Create("/tmp/stdio-smoke-server.log")
	defer logFile.Close()

	c, err := client.ConnectStdio(ctx, client.StdioOptions{
		Agent:    "agent-local-1eaef76c-8fe6-4f6d-92d3-3b4462a564a6",
		ServerLog: logFile,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect error: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	fmt.Fprintf(os.Stderr, "connected! agent=%s conv=%s\n", c.Runtime.AgentID, c.Runtime.ConversationID)

	// Send a message
	if err := c.SendMessage("Say hello in exactly 3 words."); err != nil {
		fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		os.Exit(1)
	}

	// Read frames until turn_end
	frameCount := 0
	for {
		select {
		case f, ok := <-c.Frames:
			if !ok {
				fmt.Fprintf(os.Stderr, "frames channel closed\n")
				goto done
			}
			frameCount++
			fmt.Printf("frame %d: type=%s\n", frameCount, f.Type)
			if f.Type == "transcript_update" {
				var chunk struct {
					Chunk struct {
						Content []struct {
							Type string `json:"type"`
							Text string `json:"text"`
						} `json:"content"`
					} `json:"chunk"`
				}
				json.Unmarshal(f.Raw, &chunk)
				for _, c := range chunk.Chunk.Content {
					if c.Type == "text" {
						fmt.Printf("  text: %s\n", c.Text)
					}
				}
			}
			if f.Type == "turn_end" {
				goto done
			}
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "timeout\n")
			goto done
		}
	}
done:
	fmt.Fprintf(os.Stderr, "total frames: %d\n", frameCount)
	_ = io.Discard // suppress unused import
}
