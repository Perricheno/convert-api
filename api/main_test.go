package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCancelTerminatesConverterChildren(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survived")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd := fileeCommand(ctx, "sh", "-c", `(sleep 1; echo survived > "$1") & wait`, "sh", marker)
	if err := cmd.Run(); err == nil {
		t.Fatal("expected cancellation to terminate the converter")
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("expected deadline exceeded, got %v", ctx.Err())
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("converter child survived cancellation: stat error %v", err)
	}
}
