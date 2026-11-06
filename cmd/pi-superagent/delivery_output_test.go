package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/Icatme/pi-go/superagent"
)

func TestOutputSessionFanoutDeliveryIdentitySurvivesReplay(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	router := func(_ context.Context, j superagent.MainJob) (superagent.MainOutcome, error) {
		action := "ignore"
		if j.Input.Kind == "user" {
			action = "new"
		}
		return superagent.MainOutcome{Decision: superagent.MainDecision{Action: action}, Snapshot: j.Snapshot}, nil
	}
	worker := func(_ context.Context, j superagent.Job) (superagent.Outcome, error) {
		return superagent.Outcome{Summary: j.Trigger.Text}, nil
	}
	open := func() *superagent.SuperAgent {
		s, err := superagent.OpenMain(ctx, path, 2, router, worker)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	s := open()
	for _, id := range []string{"a", "b"} {
		if _, err := s.Submit(superagent.Trigger{ID: id, Source: "chat", Thread: "planning", Kind: "request", Text: id}); err != nil {
			t.Fatal(err)
		}
		if err := s.WaitIdle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.Status().Tasks) != 2 {
		t.Fatalf("tasks: %+v", s.Status().Tasks)
	}
	for _, task := range s.Status().Tasks {
		if err := s.SetThreadSubscription(task.ID, "github", "pr", true); err != nil {
			t.Fatal(err)
		}
	}
	event := superagent.Trigger{ID: "review", Source: "github", Thread: "pr", Kind: "signal", Text: "Review"}
	if _, err := s.SubmitThread(event); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	read := func(s *superagent.SuperAgent) map[string]string {
		var out bytes.Buffer
		session := &outputSession{s: s, out: json.NewEncoder(&out)}
		if err := session.flush(); err != nil {
			t.Fatal(err)
		}
		if session.cursor != len(s.ReportsSince(0)) {
			t.Fatalf("cursor=%d", session.cursor)
		}
		dec := json.NewDecoder(&out)
		ids := map[string]string{}
		for {
			var frame map[string]any
			if err := dec.Decode(&frame); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if frame["source"] != "github" {
				continue
			}
			id, _ := frame["delivery_id"].(string)
			if id == "" {
				t.Fatalf("REPORT missing delivery_id: %+v", frame)
			}
			ids[frame["task_id"].(string)] = id
		}
		if len(ids) != 2 {
			t.Fatalf("fanout reports: %+v", ids)
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("collapsed delivery identity: %+v", ids)
			}
			seen[id] = true
		}
		if err := session.flush(); err != nil {
			t.Fatal(err)
		}
		if out.Len() != 0 {
			t.Fatal("count cursor replayed consumed reports")
		}
		return ids
	}
	before := read(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open()
	if accepted, err := s.SubmitThread(event); err != nil || accepted {
		t.Fatalf("duplicate: %v %v", accepted, err)
	}
	after := read(s)
	for task, id := range before {
		if after[task] != id {
			t.Fatalf("identity changed: before=%v after=%v", before, after)
		}
	}
}

type failingReportEncoder struct {
	out       frameEncoder
	remaining int
}

func (e *failingReportEncoder) Encode(frame any) error {
	if e.remaining == 0 {
		return io.ErrClosedPipe
	}
	e.remaining--
	return e.out.Encode(frame)
}

func TestOutputSessionFailedFlushDoesNotAdvanceCursor(t *testing.T) {
	router := func(context.Context, superagent.Trigger, []superagent.TaskInfo) (superagent.Route, error) {
		return superagent.Route{Action: "ignore"}, nil
	}
	worker := func(context.Context, superagent.Job) (superagent.Outcome, error) { return superagent.Outcome{}, nil }
	s, err := superagent.Open(t.Context(), filepath.Join(t.TempDir(), "journal.jsonl"), 1, router, worker)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []string{"noise-a", "noise-b"} {
		if _, err := s.Submit(superagent.Trigger{ID: id, Source: "chat", Thread: "thread", Kind: "noise"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.WaitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	var partial bytes.Buffer
	session := &outputSession{s: s, out: &failingReportEncoder{out: json.NewEncoder(&partial), remaining: 1}}
	if err := session.flush(); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("flush: %v", err)
	}
	if session.cursor != 0 {
		t.Fatalf("failed output advanced cursor to %d", session.cursor)
	}
	var out bytes.Buffer
	session.out = json.NewEncoder(&out)
	if err := session.flush(); err != nil {
		t.Fatal(err)
	}
	before, after := decodeFrames(t, &partial), decodeFrames(t, &out)
	if len(before) != 1 || len(after) != 2 {
		t.Fatalf("partial=%d retry=%d", len(before), len(after))
	}
	if before[0].DeliveryID == "" || before[0].DeliveryID != after[0].DeliveryID {
		t.Fatal("partial write retry changed delivery identity")
	}
}
