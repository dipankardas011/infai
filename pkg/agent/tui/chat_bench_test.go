package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/google/uuid"
)

// benchmarkChatWithTranscriptSize builds a session of count markdown-heavy
// blocks and lays it out, so a benchmark starts from a transcript that has been
// drawn once.
func benchmarkChatWithTranscriptSize(b *testing.B, count int) *chatModel {
	b.Helper()
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	for i := range count {
		m.blocks = append(m.blocks, block{
			role: "assistant",
			text: fmt.Sprintf("## Answer %d\n\n%s\n\n```go\nfunc example%d() {}\n```", i, strings.Repeat("A representative markdown paragraph with formatting. ", 10), i),
		})
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return m
}

// BenchmarkChatStreamingRefreshWithTranscriptSize measures one refresh of a
// turn that is still streaming: the interval the stream tick drives.
func BenchmarkChatStreamingRefreshWithTranscriptSize(b *testing.B) {
	for _, count := range []int{50, 500} {
		b.Run(fmt.Sprintf("blocks_%d", count), func(b *testing.B) {
			m := benchmarkChatWithTranscriptSize(b, count)
			m.blocks = append(m.blocks, block{role: "assistant", text: strings.Repeat("streaming words ", 400)})
			m.streaming, m.streamingAt = true, len(m.blocks)-1
			m.refreshTranscript(true)

			b.ResetTimer()
			for range b.N {
				m.refreshTranscript(true)
			}
		})
	}
}

// BenchmarkChatStreamEventWithTranscriptSize measures one token of a streamed
// answer: the work the update loop does per event, with the layout pass that
// follows it.
func BenchmarkChatStreamEventWithTranscriptSize(b *testing.B) {
	for _, count := range []int{50, 500} {
		b.Run(fmt.Sprintf("blocks_%d", count), func(b *testing.B) {
			m := benchmarkChatWithTranscriptSize(b, count)
			m.session.ID = uuid.New()
			token := "token "
			event := sessionEventMsg{
				sessionID:  m.session.ID,
				observerID: m.sessionObserverID,
				event:      contracts.EventStream{Kind: contracts.DeltaContent, Content: &token},
			}

			b.ResetTimer()
			for range b.N {
				_, _ = m.Update(event)
			}
		})
	}
}

// BenchmarkChatOpenTranscriptWithTranscriptSize measures the first frame of a
// session: the blocks are built, then the layout asks for the transcript.
func BenchmarkChatOpenTranscriptWithTranscriptSize(b *testing.B) {
	for _, count := range []int{200, 1000} {
		b.Run(fmt.Sprintf("blocks_%d", count), func(b *testing.B) {
			for range b.N {
				_ = benchmarkChatWithTranscriptSize(b, count)
			}
		})
	}
}

func BenchmarkChatInputWithLongTranscript(b *testing.B) {
	benchmarkChatInput(b, 50)
}

func BenchmarkChatInputWithTranscriptSize(b *testing.B) {
	for _, count := range []int{200, 1000} {
		b.Run(fmt.Sprintf("blocks_%d", count), func(b *testing.B) {
			benchmarkChatInput(b, count)
		})
	}
}

func BenchmarkChatViewWithTranscriptSize(b *testing.B) {
	for _, count := range []int{200, 1000} {
		b.Run(fmt.Sprintf("blocks_%d", count), func(b *testing.B) {
			m := benchmarkChatWithTranscriptSize(b, count)
			b.ResetTimer()
			for range b.N {
				_ = m.View()
			}
		})
	}
}

func BenchmarkChatRefreshTranscriptWithTranscriptSize(b *testing.B) {
	for _, count := range []int{200, 1000} {
		b.Run(fmt.Sprintf("blocks_%d", count), func(b *testing.B) {
			m := benchmarkChatWithTranscriptSize(b, count)
			b.ResetTimer()
			for range b.N {
				m.refreshTranscript(false)
			}
		})
	}
}

func BenchmarkChatResizeWithTranscriptSize(b *testing.B) {
	for _, count := range []int{200, 1000} {
		widths := []int{120, 121}
		b.Run(fmt.Sprintf("blocks_%d/same_width", count), func(b *testing.B) {
			m := benchmarkChatWithTranscriptSize(b, count)
			b.ResetTimer()
			for range b.N {
				m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
			}
		})
		b.Run(fmt.Sprintf("blocks_%d/wider", count), func(b *testing.B) {
			m := benchmarkChatWithTranscriptSize(b, count)
			b.ResetTimer()
			for i := range b.N {
				m.Update(tea.WindowSizeMsg{Width: widths[i%len(widths)], Height: 40})
			}
		})
	}
}

func benchmarkChatInput(b *testing.B, count int) {
	m := benchmarkChatWithTranscriptSize(b, count)
	keys := []tea.KeyPressMsg{
		{Code: 'x', Text: "x"},
		{Code: tea.KeyBackspace},
	}

	b.ResetTimer()
	for i := range b.N {
		_, _ = m.Update(keys[i%len(keys)])
	}
}

func BenchmarkChatScrollWithLongTranscript(b *testing.B) {
	benchmarkChatScroll(b, 50)
}

func BenchmarkChatScrollWithTranscriptSize(b *testing.B) {
	for _, count := range []int{200, 1000} {
		b.Run(fmt.Sprintf("blocks_%d", count), func(b *testing.B) {
			benchmarkChatScroll(b, count)
		})
	}
}

func benchmarkChatScroll(b *testing.B, count int) {
	m := benchmarkChatWithTranscriptSize(b, count)
	keys := []tea.KeyPressMsg{
		{Code: tea.KeyPgUp},
		{Code: tea.KeyPgDown},
	}

	b.ResetTimer()
	for i := range b.N {
		_, _ = m.Update(keys[i%len(keys)])
	}
}
