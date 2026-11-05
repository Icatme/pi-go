package main

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

type liveFrame struct {
	frame
	InputLine int                  `json:"input_line"`
	RequestID string               `json:"request_id"`
	Progress  *superagent.Progress `json:"progress"`
}

type liveOutput chan liveFrame

func (out liveOutput) Write(b []byte) (int, error) {
	var f liveFrame
	if err := json.Unmarshal(b, &f); err != nil {
		return 0, err
	}
	out <- f
	return len(b), nil
}

func TestControlClassificationAndCorrelationLimit(t *testing.T) {
	const over = "界"
	b, err := json.Marshal(map[string]string{"op": "status", "request_id": strings.Repeat(over, 86)})
	if err != nil {
		t.Fatal(err)
	}
	if c, err := parseControl(b, 9); c == nil || err == nil || c.requestID != "" || c.line != 9 {
		t.Fatalf("oversized correlation echoed or accepted: %+v %v", c, err)
	}
	for _, invalid := range []string{`{"op":`, `{"op":"status","op":"abort"}`, `{"op":"status","request_id":4}`} {
		if c, err := parseControl([]byte(invalid), 3); c == nil || err == nil {
			t.Fatalf("invalid control %s: %+v, %v", invalid, c, err)
		}
	}
	if c, _ := parseControl([]byte(`{"text":"op"}`), 1); c != nil {
		t.Fatal("ordinary event classified as a control")
	}
}

func TestFullDuplexTargetedControls(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	started := make(chan struct{})
	poll := make(chan struct{})
	instructions := make(chan []agent.Message, 1)
	route := func(context.Context, superagent.Trigger, []superagent.TaskInfo) (superagent.Route, error) {
		return superagent.Route{Action: "new"}, nil
	}
	s, err := superagent.Open(ctx, filepath.Join(t.TempDir(), "state.jsonl"), 1, route, func(ctx context.Context, j superagent.Job) (superagent.Outcome, error) {
		close(started)
		select {
		case <-ctx.Done():
			return superagent.Outcome{}, ctx.Err()
		case <-poll:
		}
		messages, err := j.TakeSteering(ctx, agent.QueueModeOneAtATime)
		if err != nil {
			return superagent.Outcome{}, err
		}
		instructions <- messages
		j.NotifyProgress("tool_start")
		<-ctx.Done()
		return superagent.Outcome{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	in, send := io.Pipe()
	defer send.Close()
	output := make(liveOutput, 64)
	done := make(chan error, 1)
	go func() { done <- (&outputSession{s: s, out: json.NewEncoder(output)}).run(ctx, in) }()
	write := func(raw string) {
		t.Helper()
		if _, err := io.WriteString(send, raw+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	var seen []liveFrame
	match := func(kind, id string) liveFrame {
		t.Helper()
		for _, f := range seen {
			if f.Type == kind && f.RequestID == id {
				return f
			}
		}
		for {
			select {
			case f := <-output:
				seen = append(seen, f)
				if f.Type == kind && f.RequestID == id {
					return f
				}
			case <-ctx.Done():
				t.Fatalf("waiting for %s/%s, saw %+v", kind, id, seen)
				return liveFrame{}
			}
		}
	}

	write(`{"id":"e1","source":"shell","thread":"ops","kind":"request","text":"start"}`)
	match("ACCEPTED", "")
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("worker did not start")
	}
	write(`{"op":"status","request_id":"inspect"}`)
	status := match("STATUS", "inspect")
	if len(status.Tasks) != 1 || status.Tasks[0].ActiveSource != "shell" || status.Tasks[0].ActiveEventID != "e1" {
		t.Fatal(status)
	}
	write(`{"op":"subscribe","task_id":"task-1","request_id":"watch"}`)
	match("SUBSCRIBED", "watch")
	p := match("PROGRESS", "watch").Progress
	if p == nil || p.Kind != "running" || p.Source != "shell" || p.EventID != "e1" {
		t.Fatal(p)
	}
	write(`{"op":"subscribe","task_id":"task-1","request_id":"again"}`)
	match("CONTROL_ERROR", "again")
	write(`{"op":"abort","task_id":"task-1","source":"shell","event_id":"stale","request_id":"wrong"}`)
	match("CONTROL_ERROR", "wrong")
	write(`{"op":"steer","task_id":"task-1","source":"shell","event_id":"e1","request_id":"first","text":"change scope"}`)
	first := match("STEER_QUEUED", "first")
	write(`{"op":"steer","task_id":"task-1","source":"shell","event_id":"e1","request_id":"second","text":"another change"}`)
	second := match("STEER_QUEUED", "second")
	for _, f := range seen {
		if f.Type == "STEER_DELIVERED" {
			t.Fatal("acknowledged delivery before the worker polled")
		}
	}
	close(poll)
	select {
	case messages := <-instructions:
		if len(messages) != 1 || len(messages[0].Parts) != 1 || messages[0].Parts[0].Text != "change scope" {
			t.Fatal(messages)
		}
	case <-ctx.Done():
		t.Fatal("worker missed steering")
	}
	delivered := match("STEER_DELIVERED", "first")
	if first.InputLine != delivered.InputLine || delivered.EventID != "e1" || delivered.TaskID != "task-1" {
		t.Fatal(first, delivered)
	}
	toolStart := func(f liveFrame) bool {
		return f.RequestID == "watch" && f.Progress != nil && f.Progress.Kind == "tool_start" &&
			f.Progress.Sequence > p.Sequence && f.Progress.EventID == "e1"
	}
	for !slices.ContainsFunc(seen, toolStart) {
		select {
		case f := <-output:
			seen = append(seen, f)
		case <-ctx.Done():
			t.Fatal("worker progress did not reach its subscriber")
		}
	}
	write(`{"op":"unsubscribe","task_id":"task-1","request_id":"finish-watch"}`)
	match("UNSUBSCRIBED", "finish-watch")
	write(`{"op":"abort","task_id":"task-1","source":"shell","event_id":"e1","request_id":"stop"}`)
	if err := send.Close(); err != nil {
		t.Fatal(err)
	}
	match("ABORT_REQUESTED", "stop")
	dropped := match("STEER_NOT_DELIVERED", "second")
	if dropped.InputLine != second.InputLine || dropped.InputLine == first.InputLine {
		t.Fatal(first, second, dropped)
	}
	if f := match("REPORT", ""); f.Status != "interrupted" || !f.NeedsReview || f.EventID != "e1" {
		t.Fatal(f)
	}
	match("IDLE", "")
	unsubscribed := false
	for _, f := range seen {
		if f.Type == "UNSUBSCRIBED" {
			unsubscribed = true
		}
		if unsubscribed && f.Type == "PROGRESS" {
			t.Fatal("progress after unsubscribe:", f)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
