package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// ChatReply is what the client surfaces: the answer plus any reasoning the
// model produced (empty when the provider returns none).
type ChatReply struct {
	Reply            string
	ReasoningContent string
}

// Client sends a prompt to an agent and returns the reply.
// onDelta receives streamed text fragments (typed by kind) as they arrive;
// nil to disable. RemoteClient is the HTTP transport to a <binary> server.
type Client interface {
	Chat(ctx context.Context, prompt string, onDelta func(kind contracts.DeltaKind, text string)) (*ChatReply, error)
}

// Run is the line-based chat REPL: prompts are read from in and the assistant
// reply is printed to out. Plain stdio, no full-screen rendering.
func Run(ctx context.Context, c Client, in io.Reader, out io.Writer) error {
	fmt.Fprintln(out, "infai agent — type a message, or exit/quit to leave.")

	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		fmt.Fprint(out, "> ")

		prompt := strings.TrimSpace(scanner.Text())
		if prompt == "" {
			continue
		}
		if prompt == "exit" || prompt == "quit" {
			return nil
		}

		thinkingOpen := false
		thinkingEndedNewline := false
		closeThinking := func() {
			if thinkingOpen {
				if !thinkingEndedNewline {
					fmt.Fprintln(out)
				}
				fmt.Fprintln(out, "────────────")
				thinkingOpen = false
			}
		}
		_, err := c.Chat(ctx, prompt, func(kind contracts.DeltaKind, text string) {
			switch kind {
			case contracts.DeltaReasoning:
				if !thinkingOpen {
					fmt.Fprintln(out, "─ thinking ─")
					thinkingOpen = true
				}
				fmt.Fprint(out, text)
				thinkingEndedNewline = strings.HasSuffix(text, "\n")
			case contracts.DeltaContent:
				closeThinking()
				fmt.Fprint(out, text)
			}
		})
		if err != nil {
			Notice(out, "Error", err.Error())
			continue
		}
		closeThinking()
		fmt.Fprintln(out)
	}

	return scanner.Err()
}
