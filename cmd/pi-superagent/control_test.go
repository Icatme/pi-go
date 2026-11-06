package main

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

// Controls exercise the persistent Main session without a model or provider.
// Result notifications must not spawn another business task.
func controlMainRouter(_ context.Context, j superagent.MainJob) (superagent.MainOutcome, error) {
	action := "ignore"
	if j.Input.Kind == "user" {
		action = "new"
	}
	return superagent.MainOutcome{Decision: superagent.MainDecision{Action: action}, Snapshot: j.Snapshot}, nil
}

func TestFullDuplexTargetedControls(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	started := make(chan struct{})
	poll := make(chan struct{})
	instructions := make(chan []agent.Message, 1)
	otherStarted, finishOther := make(chan struct{}), make(chan struct{})
	otherFinished := make(chan error, 1)
	allowOther := sync.OnceFunc(func() { close(finishOther) })
	defer allowOther()
	s, err := superagent.OpenMain(ctx, filepath.Join(t.TempDir(), "state.jsonl"), 2, controlMainRouter, func(ctx context.Context, j superagent.Job) (superagent.Outcome, error) {
		if j.Trigger.ID == "e2" {
			close(otherStarted)
			select {
			case <-ctx.Done():
				otherFinished <- ctx.Err()
				return superagent.Outcome{}, ctx.Err()
			case <-finishOther:
			}
			messages, err := j.TakeSteering(ctx, agent.QueueModeAll)
			if len(messages) != 0 || len(j.Snapshot.Messages) != 0 {
				t.Errorf("independent task received steering/context: %+v %+v", messages, j.Snapshot)
			}
			otherFinished <- err
			return superagent.Outcome{Summary: "independent task finished"}, err
		}
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
	write(`{"id":"e2","source":"shell","thread":"ops","kind":"request","text":"independent work"}`)
	select {
	case <-otherStarted:
	case <-ctx.Done():
		t.Fatal("same-chat independent task did not start")
	}
	write(`{"op":"status","request_id":"two"}`)
	if f := match("STATUS", "two"); len(f.Tasks) != 2 || f.Tasks[1].ActiveEventID != "e2" {
		t.Fatal(f)
	}
	if err := s.SetThreadSubscription("task-1", "github", "pr-27", true); err != nil {
		t.Fatal(err)
	}
	assertThreadRouting := func() {
		t.Helper()
		if got := s.ThreadSubscriptions("shell", "ops"); len(got) != 0 {
			t.Fatalf("progress subscription granted chat routing: %v", got)
		}
		if got := s.ThreadSubscriptions("github", "pr-27"); !slices.Equal(got, []string{"task-1"}) {
			t.Fatalf("progress subscription changed authorized Thread routing: %v", got)
		}
	}
	assertThreadRouting()
	write(`{"op":"subscribe","task_id":"task-1","request_id":"watch"}`)
	match("SUBSCRIBED", "watch")
	assertThreadRouting()
	p := match("PROGRESS", "watch").Progress
	if p == nil || p.Kind != "running" || p.Source != "shell" || p.EventID != "e1" {
		t.Fatal(p)
	}
	write(`{"op":"subscribe","task_id":"task-1","request_id":"again"}`)
	match("CONTROL_ERROR", "again")
	write(`{"op":"abort","task_id":"task-1","source":"shell","event_id":"stale","request_id":"wrong"}`)
	match("CONTROL_ERROR", "wrong")
	write(`{"op":"abort","task_id":"task-2","source":"shell","event_id":"e1","request_id":"cross-abort"}`)
	match("CONTROL_ERROR", "cross-abort")
	write(`{"op":"steer","task_id":"task-2","source":"shell","event_id":"e1","request_id":"cross-steer","text":"must not cross"}`)
	match("CONTROL_ERROR", "cross-steer")
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
	assertThreadRouting()
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
	allowOther()
	select {
	case err := <-otherFinished:
		if err != nil {
			t.Fatalf("targeted abort affected independent task: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("independent task did not finish")
	}
	match("IDLE", "")
	var independentReport bool
	for _, f := range seen {
		if f.Type == "REPORT" && f.EventID == "e2" && f.TaskID == "task-2" && f.Status == "completed" {
			independentReport = true
		}
	}
	if !independentReport {
		t.Fatal("missing independent completion report")
	}
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
