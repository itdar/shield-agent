package process

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func newRunnerLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestRunCommandNotFound(t *testing.T) {
	err := Run(context.Background(), []string{"__binary_that_does_not_exist__"}, newRunnerLogger())
	if err == nil {
		t.Fatal("expected error for non-existent binary, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "not found") {
		t.Errorf("expected error to contain 'not found', got: %q", msg)
	}
}

func TestRunEcho(t *testing.T) {
	err := Run(context.Background(), []string{"echo", "hello"}, newRunnerLogger())
	if err != nil {
		t.Fatalf("expected nil error for 'echo hello', got: %v", err)
	}
}
