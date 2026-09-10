package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func benchmarkChatWithLongTranscript(b *testing.B) *chatModel {
	b.Helper()
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	for i := range 50 {
		m.blocks = append(m.blocks, block{
			role: "assistant",
			text: fmt.Sprintf("## Answer %d\n\n%s\n\n```go\nfunc example%d() {}\n```", i, strings.Repeat("A representative markdown paragraph with formatting. ", 10), i),
		})
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return m
}

func BenchmarkChatInputWithLongTranscript(b *testing.B) {
	m := benchmarkChatWithLongTranscript(b)
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
	m := benchmarkChatWithLongTranscript(b)
	keys := []tea.KeyPressMsg{
		{Code: tea.KeyPgUp},
		{Code: tea.KeyPgDown},
	}

	b.ResetTimer()
	for i := range b.N {
		_, _ = m.Update(keys[i%len(keys)])
	}
}
