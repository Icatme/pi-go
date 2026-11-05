package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type blockedBrokenPipe struct {
	ctx     context.Context
	entered chan struct{}
	release chan struct{}
}

func (w *blockedBrokenPipe) Write([]byte) (int, error) {
	close(w.entered)
	select {
	case <-w.release:
		return 0, io.ErrClosedPipe
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	}
}

func TestBrokenStdoutCleansQueuedSpillAndStopsInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	journal := filepath.Join(t.TempDir(), "events.jsonl")
	spoolDir := t.TempDir()
	for _, key := range []string{"TMP", "TEMP", "TMPDIR"} {
		t.Setenv(key, spoolDir)
	}
	in, send := io.Pipe()
	defer send.Close()
	out := &blockedBrokenPipe{ctx, make(chan struct{}), make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- command(ctx, []string{"run", "-state", journal}, in, out, io.Discard) }()
	const status = "{\"op\":\"status\"}\n"
	if _, err := io.WriteString(send, status); err != nil {
		t.Fatal(err)
	}
	select {
	case <-out.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for range 300 {
		if _, err := io.WriteString(send, status); err != nil {
			t.Fatal(err)
		}
	}
	if files, err := os.ReadDir(spoolDir); err != nil || len(files) != 1 {
		t.Fatalf("expected one active spill, count=%d err=%v", len(files), err)
	}
	close(out.release)
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("lost writer error: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("broken output waited for more stdin")
	}
	if files, err := os.ReadDir(spoolDir); err != nil || len(files) != 0 {
		t.Fatalf("unreleased CLI output files: count=%d err=%v", len(files), err)
	}
}
