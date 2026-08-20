package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
)

// ChatReply is what the client surfaces: the answer plus any reasoning the
// model produced (empty when the provider returns none).
type ChatReply struct {
	Reply            string
	ReasoningContent string
}

// Client sends a prompt to an agent and returns the reply.
// RemoteClient is the HTTP transport to a running <binary> server.
type Client interface {
	Chat(ctx context.Context, prompt string) (*ChatReply, error)
}

// Run is the line-based chat REPL: prompts are read from in and the assistant
// reply is printed to out. Plain stdio, no full-screen rendering.
func Run(ctx context.Context, c Client, in io.Reader, out io.Writer) error {
	fmt.Fprintln(out, "infai agent — type a message, or exit/quit to leave.")

	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprint(out, "> ")
		if !scanner.Scan() {
			return nil
		}

		prompt := strings.TrimSpace(scanner.Text())
		if prompt == "" {
			continue
		}
		if prompt == "exit" || prompt == "quit" {
			return nil
		}

		reply, err := c.Chat(ctx, prompt)
		if err != nil {
			Notice(out, "Error", err.Error())
			continue
		}
		if reply.ReasoningContent != "" {
			fmt.Fprintln(out, "─ thinking ─")
			fmt.Fprintln(out, reply.ReasoningContent)
			fmt.Fprintln(out, "────────────")
		}
		fmt.Fprintln(out, reply.Reply)
	}
}
