package evals

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEvalAllowsTenMinutesForProjectBuilds(t *testing.T) {
	if defaultEvalTimeout != 10*time.Minute {
		t.Fatalf("default eval timeout = %s, want 10m", defaultEvalTimeout)
	}
}

func TestEvalFailureLabelsStderrAndStdout(t *testing.T) {
	err := NewEvalScriptHandler(t.TempDir(), "echo build-error >&2; echo test-summary; exit 1")(context.Background())
	if err == nil {
		t.Fatal("evaluation unexpectedly passed")
	}

	message := err.Error()
	stderr := strings.Index(message, "stderr:\nbuild-error")
	stdout := strings.Index(message, "stdout:\ntest-summary")
	if stderr == -1 || stdout == -1 {
		t.Fatalf("evaluation error did not label both streams: %q", message)
	}
	if stderr > stdout {
		t.Fatalf("stderr should precede stdout: %q", message)
	}
}

func TestEvalFailureWithoutOutputExplainsMissingDiagnostics(t *testing.T) {
	err := NewEvalScriptHandler(t.TempDir(), "exit 1")(context.Background())
	if err == nil {
		t.Fatal("evaluation unexpectedly passed")
	}

	want := "evaluation failed: exit status 1; the acceptance script printed nothing, so no failure details are available"
	if err.Error() != want {
		t.Fatalf("evaluation error = %q, want %q", err, want)
	}
}

func TestEvalStopsWhenItsContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cwd := t.TempDir()
	result := make(chan error, 1)
	go func() {
		result <- NewEvalScriptHandler(cwd, "while :; do :; done")(ctx)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("evaluation error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("evaluation did not stop after context cancellation")
	}
}
