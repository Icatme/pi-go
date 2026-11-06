package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/executionreceipt"
	"github.com/Icatme/pi-go/superagent"
)

// Simulate a client that sent a control frame before it drained previous output.
type gatedOutput struct {
	kind     string
	first    sync.Once
	writing  chan struct{}
	allow    chan struct{}
	contents bytes.Buffer
}

func TestSubscribeThenEOFSendsInitialProgress(t *testing.T) {
	state := filepath.Join(t.TempDir(), "events.jsonl")
	setup := `{"id":"first","source":"shell","thread":"work","kind":"request","text":"hello"}`
	if err := command(t.Context(), []string{"run", "-state", state}, strings.NewReader(setup), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	subscribe := `{"op":"subscribe","task_id":"task-1","request_id":"watch"}`
	if err := command(t.Context(), []string{"run", "-state", state}, strings.NewReader(subscribe), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var subscribed, observed bool
	dec := json.NewDecoder(&out)
	for {
		var f liveFrame
		if err := dec.Decode(&f); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		switch f.Type {
		case "SUBSCRIBED":
			subscribed = f.RequestID == "watch"
		case "PROGRESS":
			if !subscribed || f.RequestID != "watch" || f.Progress == nil || f.Progress.TaskID != "task-1" || f.Progress.EventID != "first" || f.Progress.Kind != "completed" {
				t.Fatal(f)
			}
			observed = true
		case "IDLE":
			if !observed {
				t.Fatal("stream ended before initial task progress")
			}
		}
	}
	if !observed {
		t.Fatal("no initial progress")
	}
}

func (w *gatedOutput) Write(data []byte) (int, error) {
	var header struct{ Type string }
	if err := json.Unmarshal(data, &header); err != nil {
		return 0, err
	}
	if header.Type == w.kind {
		w.first.Do(func() {
			close(w.writing)
			<-w.allow
		})
	}
	return w.contents.Write(data)
}

func TestAbortWhileStdoutIsBlocked(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	dir := t.TempDir()
	target := filepath.Join(dir, "published")
	entered, canceled, publish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	allowPublish := sync.OnceFunc(func() { close(publish) })
	tool := agent.ToolDefinition{Name: "publish", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		close(entered)
		select {
		case <-ctx.Done():
			close(canceled)
			return agent.ToolResult{}, ctx.Err()
		case <-publish:
			if err := os.WriteFile(target, []byte("published"), 0600); err != nil {
				return agent.ToolResult{}, err
			}
			executionreceipt.ReportLocalCommit(ctx)
			return agent.ToolResult{}, nil
		}
	}}
	otherStarted, finishOther := make(chan struct{}), make(chan struct{})
	allowOther := sync.OnceFunc(func() { close(finishOther) })
	worker := func(ctx context.Context, j superagent.Job) (superagent.Outcome, error) {
		if j.Trigger.ID == "other" {
			close(otherStarted)
			select {
			case <-ctx.Done():
				return superagent.Outcome{}, ctx.Err()
			case <-finishOther:
				return superagent.Outcome{Summary: "independent task finished"}, nil
			}
		}
		out, h, err := agent.RunToolCallWithReceipts(ctx, agent.ToolCall{ID: "write", Name: tool.Name, Arguments: json.RawMessage("{}")}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}}, 1)
		if err != nil {
			return superagent.Outcome{}, err
		}
		if err := h.Wait(context.WithoutCancel(ctx)); err != nil {
			return superagent.Outcome{}, err
		}
		return superagent.Outcome{Summary: "finished"}, out.Err
	}
	s, err := superagent.OpenMain(ctx, filepath.Join(dir, "events.jsonl"), 2, controlMainRouter, worker)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer allowPublish()
	defer allowOther()
	output := &gatedOutput{kind: "STATUS", writing: make(chan struct{}), allow: make(chan struct{})}
	allowOutput := sync.OnceFunc(func() { close(output.allow) })
	defer allowOutput()
	input, send := io.Pipe()
	defer send.Close()
	done := make(chan error, 1)
	go func() { done <- (&outputSession{s: s, out: json.NewEncoder(output)}).run(ctx, input) }()
	write := func(raw string) {
		t.Helper()
		if _, err := io.WriteString(send, raw+"\n"); err != nil {
			t.Fatalf("send %s: %v", raw, err)
		}
	}
	write(`{"id":"event","source":"console","thread":"ops","kind":"request","text":"publish"}`)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("tool never started")
	}
	write(`{"id":"other","source":"console","thread":"ops","kind":"request","text":"continue independently"}`)
	select {
	case <-otherStarted:
	case <-ctx.Done():
		t.Fatal("independent worker never started")
	}
	const statusCount = 300
	statusFrame := func(i int) string { return fmt.Sprintf(`{"op":"status","request_id":"inspect-%d"}`, i) }
	write(statusFrame(0))
	select {
	case <-output.writing:
	case <-ctx.Done():
		t.Fatal("status never reached stdout")
	}
	for i := 1; i < statusCount; i++ {
		write(statusFrame(i))
	}
	write(`{"op":"abort","task_id":"task-1","source":"console","event_id":"event","request_id":"stop"}`)
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		// Let the executor cross its commit point while stdout remains blocked.
		// A control queued behind the output write is now too late to prevent it.
		allowPublish()
	}
	allowOther()
	if err := s.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if err := send.Close(); err != nil {
		t.Fatal(err)
	}
	allowOutput()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file published despite abort delivered on stdin: stat err=%v", err)
	}
	var ack, interrupted, otherCompleted bool
	statuses := 0
	dec := json.NewDecoder(&output.contents)
	for {
		var f liveFrame
		err := dec.Decode(&f)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if f.Type == "STATUS" {
			if f.RequestID != fmt.Sprintf("inspect-%d", statuses) || f.InputLine != statuses+3 {
				t.Fatalf("status #%d arrived out of order: request_id=%q input_line=%d", statuses, f.RequestID, f.InputLine)
			}
			statuses++
		}
		if f.Type == "ABORT_REQUESTED" && f.RequestID == "stop" && f.EventID == "event" {
			ack = true
			if statuses != statusCount {
				t.Fatalf("abort acknowledged before all statuses: %d/%d", statuses, statusCount)
			}
		}
		if f.Type == "REPORT" && f.EventID == "event" && f.Status == "interrupted" {
			interrupted = true
		}
		if f.Type == "REPORT" && f.EventID == "other" && f.Status == "completed" {
			otherCompleted = true
		}
	}
	if !ack || !interrupted || !otherCompleted || statuses != statusCount {
		t.Fatalf("abort ack=%t interrupted=%t independent completed=%t statuses=%d/%d", ack, interrupted, otherCompleted, statuses, statusCount)
	}
}
