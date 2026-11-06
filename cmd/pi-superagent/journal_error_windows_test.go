//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent/session"
	"github.com/Icatme/pi-go/superagent"
	"golang.org/x/sys/windows"
)

func TestJournalFailureExitsWhileInputRemainsOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	entered, release := make(chan struct{}), make(chan struct{})
	s, err := superagent.Open(ctx, path, 1, func(context.Context, superagent.Trigger, []superagent.TaskInfo) (superagent.Route, error) {
		return superagent.Route{Action: "new"}, nil
	}, func(ctx context.Context, _ superagent.Job) (superagent.Outcome, error) {
		close(entered)
		select {
		case <-ctx.Done():
			return superagent.Outcome{}, ctx.Err()
		case <-release:
			return superagent.Outcome{Summary: "finished"}, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	previous := superagent.Trigger{ID: "previous", Source: "test", Thread: "monitor", Kind: "noise"}
	if ok, err := s.Submit(previous); !ok || err != nil {
		t.Fatalf("submit: %t, %v", ok, err)
	}
	if err := s.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}

	input, send := io.Pipe()
	defer send.Close()
	out := &gatedOutput{kind: "STEER_QUEUED", writing: make(chan struct{}), allow: make(chan struct{})}
	allowOutput := sync.OnceFunc(func() { close(out.allow) })
	defer allowOutput()
	result := make(chan error, 1)
	go func() { result <- (&outputSession{s: s, out: json.NewEncoder(out)}).run(ctx, input) }()
	event := superagent.Trigger{ID: "active", Source: "test", Thread: "release", Kind: "request", Text: "finish"}
	if err := json.NewEncoder(send).Encode(event); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("worker did not enter")
	}
	if _, err := io.WriteString(send, `{"op":"steer","request_id":"change","task_id":"task-1","source":"test","event_id":"active","text":"update"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-out.writing:
	case <-ctx.Done():
		t.Fatal("steering was never queued")
	}

	// The running marker has been synced. Lock the first byte of the next
	// append through a second Windows handle so only its terminal write fails.
	lock, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil {
		t.Fatal(err)
	}
	pos := windows.Overlapped{Offset: uint32(info.Size()), OffsetHigh: uint32(uint64(info.Size()) >> 32)}
	if err := windows.LockFileEx(windows.Handle(lock.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &pos); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-s.Done():
	case <-ctx.Done():
		t.Fatal("append did not stop the supervisor")
	}
	allowOutput()
	select {
	case err := <-result:
		var storage *session.Error
		if !errors.As(err, &storage) || storage.Code != session.ErrorStorage || !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			t.Fatalf("lost terminal write error: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("terminal write failed but CLI waited for more stdin")
	}
	var accepted, prior bool
	var queued, undelivered int
	dec := json.NewDecoder(&out.contents)
	for {
		var f liveFrame
		if err := dec.Decode(&f); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		switch f.Type {
		case "ACCEPTED":
			accepted = f.EventID == event.ID
		case "REPORT":
			if f.EventID != previous.ID || f.Status != "ignored" {
				t.Fatal("uncommitted report:", f)
			}
			prior = true
		case "STEER_QUEUED", "STEER_NOT_DELIVERED":
			if f.RequestID != "change" || f.InputLine != 2 || f.EventID != event.ID {
				t.Fatal("lost steering correlation:", f)
			}
			if f.Type == "STEER_QUEUED" {
				queued++
			} else {
				undelivered++
			}
		default:
			t.Fatal("unexpected output after journal failure:", f)
		}
	}
	if !accepted || !prior || queued != 1 || undelivered != 1 {
		t.Fatalf("accepted=%t prior report=%t queued=%d undelivered=%d", accepted, prior, queued, undelivered)
	}
}
