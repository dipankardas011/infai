package models

import (
	"strings"
	"testing"
)

func TestClaudeContentStreamFollowsTheProbeFragments(t *testing.T) {
	var deltas []string
	stream := newClaudeContentStream(func(text string) bool {
		deltas = append(deltas, text)
		return false
	})
	// The exact fragment boundaries the CLI produced for a one-word answer.
	for _, fragment := range []string{
		"",
		`{"content": "h`,
		`ello from the probe`,
		`", "tool_calls": []`,
		`}`,
	} {
		if stream.Feed(fragment) {
			t.Fatal("the stream stopped unexpectedly")
		}
	}

	if got, want := stream.Content(), "hello from the probe"; got != want {
		t.Fatalf("content=%q want %q", got, want)
	}
	if got, want := strings.Join(deltas, "|"), "h|ello from the probe"; got != want {
		t.Fatalf("deltas=%q want %q", got, want)
	}
}

func TestClaudeContentStreamDecodesEscapesAcrossFragments(t *testing.T) {
	var deltas []string
	stream := newClaudeContentStream(func(text string) bool {
		deltas = append(deltas, text)
		return false
	})
	for _, fragment := range []string{
		`{"content":"line\n`,
		`umber \u00`,
		`e9 and \ud83d`,
		`\ude00 \"q\" \\ end"}`,
	} {
		stream.Feed(fragment)
	}

	if got, want := stream.Content(), "line\number é and 😀 \"q\" \\ end"; got != want {
		t.Fatalf("content=%q want %q", got, want)
	}
	if len(deltas) != 4 {
		t.Fatalf("deltas=%q", deltas)
	}
}

func TestClaudeContentStreamIgnoresANestedContentKey(t *testing.T) {
	var got strings.Builder
	stream := newClaudeContentStream(func(text string) bool {
		got.WriteString(text)
		return false
	})
	stream.Feed(`{"tool_calls":[{"name":"x","input":{"content":"SECRET"}}],"content":"answer"}`)

	if want := "answer"; got.String() != want {
		t.Fatalf("content=%q want %q", got.String(), want)
	}
	if strings.Contains(got.String(), "SECRET") {
		t.Fatal("a tool input was read as the answer")
	}
}

func TestClaudeContentStreamLeavesANullAnswerEmpty(t *testing.T) {
	var got strings.Builder
	stream := newClaudeContentStream(func(text string) bool {
		got.WriteString(text)
		return false
	})
	stream.Feed(`{"content":null,"tool_calls":[]}`)

	if got.Len() != 0 {
		t.Fatalf("content=%q", got.String())
	}
	if stream.Streamed() {
		t.Fatal("a null answer was reported as streamed")
	}
}

// The fragments can split anywhere: inside an escape, inside a surrogate pair,
// or between keys. Feeding one byte at a time must produce the same answer.
func TestClaudeContentStreamSurvivesEveryBoundary(t *testing.T) {
	const doc = `{"tool_calls":[{"content":"nope"}],"content":"a\u00e9\n\ud83d\ude00 \"q\" \\ end"}`
	const want = "aé\n😀 \"q\" \\ end"

	for split := 0; split <= len(doc); split++ {
		var got strings.Builder
		stream := newClaudeContentStream(func(text string) bool {
			got.WriteString(text)
			return false
		})
		stream.Feed(doc[:split])
		stream.Feed(doc[split:])
		if got.String() != want {
			t.Fatalf("split %d: content=%q want %q", split, got.String(), want)
		}
	}

	var got strings.Builder
	stream := newClaudeContentStream(func(text string) bool {
		got.WriteString(text)
		return false
	})
	for i := 0; i < len(doc); i++ {
		stream.Feed(doc[i : i+1])
	}
	if got.String() != want {
		t.Fatalf("byte-at-a-time: content=%q want %q", got.String(), want)
	}
}

func TestClaudeContentStreamStopsWhenTheConsumerDoes(t *testing.T) {
	stream := newClaudeContentStream(func(string) bool { return true })
	if !stream.Feed(`{"content":"stop"}`) {
		t.Fatal("the stop request was not reported")
	}
}
