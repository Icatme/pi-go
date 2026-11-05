package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/pkg/pigo"
	"github.com/Icatme/pi-go/superagent"
)

type frame struct {
	Type string `json:"type"`
	superagent.Report
	Tasks []superagent.TaskStatus `json:"tasks"`
}

func decodeFrames(t *testing.T, r io.Reader) []frame {
	t.Helper()
	dec := json.NewDecoder(r)
	var frames []frame
	for {
		var f frame
		if err := dec.Decode(&f); errors.Is(err, io.EOF) {
			return frames
		} else if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, f)
	}
}

func TestDemoUsesDistinctTasksAndAttachesCrossSourceSignal(t *testing.T) {
	var out bytes.Buffer
	if err := command(t.Context(), []string{"demo"}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	reports := map[string]frame{}
	accepted, duplicates := 0, 0
	for _, f := range decodeFrames(t, &out) {
		switch f.Type {
		case "ACCEPTED":
			accepted++
		case "DUPLICATE":
			duplicates++
		case "REPORT":
			reports[f.EventID] = f
		case "IDLE":
			if len(f.Tasks) != 2 {
				t.Fatalf("tasks: %+v", f.Tasks)
			}
		default:
			t.Fatalf("unexpected frame %+v", f)
		}
	}
	a, b := reports["demo-1"], reports["demo-2"]
	if accepted != 5 || duplicates != 1 || len(reports) != 5 || a.TaskID == "" || b.TaskID == "" || a.TaskID == b.TaskID || reports["demo-3"].TaskID != a.TaskID || reports["demo-3"].Source == a.Source || reports["demo-4"].TaskID != b.TaskID || reports["demo-5"].Status != "ignored" {
		t.Fatalf("accepted=%d duplicates=%d reports=%+v", accepted, duplicates, reports)
	}
}

// A live pipe stays open while the first worker is blocked, a second request
// is acknowledged and both terminal reports are returned. The reopened CLI
// then replays the same outcomes without creating any additional work.
func TestInteractiveRunThenReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	state := filepath.Join(t.TempDir(), "events.jsonl")
	def := agent.AgentDefinition{Model: superagent.OfflineModel()}
	route, err := superagent.PiRouter(def)
	if err != nil {
		t.Fatal(err)
	}
	work, err := superagent.PiWorker(def)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	s, err := superagent.Open(ctx, state, 1, route, func(ctx context.Context, j superagent.Job) (superagent.Outcome, error) {
		if j.Trigger.ID == "a" {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return superagent.Outcome{}, ctx.Err()
			}
		}
		return work(ctx, j)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	input, send := io.Pipe()
	defer send.Close()
	output, writer := io.Pipe()
	defer output.Close()
	result := make(chan error, 1)
	go func() {
		err := (&outputSession{s: s, out: json.NewEncoder(writer)}).run(ctx, input)
		writer.Close()
		result <- err
	}()
	frames := make(chan frame, 8)
	go func() {
		defer close(frames)
		dec := json.NewDecoder(output)
		for {
			var f frame
			if err := dec.Decode(&f); err != nil {
				return
			}
			frames <- f
		}
	}()
	next := func() frame {
		t.Helper()
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatal("output ended early")
			}
			return f
		case <-ctx.Done():
			t.Fatal("no report while stdin was open")
			return frame{}
		}
	}
	a := superagent.Trigger{ID: "a", Source: "local", Thread: "one", Kind: "request", Text: "alpha"}
	b := superagent.Trigger{ID: "b", Source: "local", Thread: "two", Kind: "request", Text: "beta"}
	enc := json.NewEncoder(send)
	if err := enc.Encode(a); err != nil {
		t.Fatal(err)
	}
	if f := next(); f.Type != "ACCEPTED" || f.EventID != "a" {
		t.Fatal(f)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("worker did not start")
	}
	if err := enc.Encode(b); err != nil {
		t.Fatal(err)
	}
	if f := next(); f.Type != "ACCEPTED" || f.EventID != "b" {
		t.Fatal(f)
	}
	close(release)
	finished := map[string]bool{}
	for range 2 {
		f := next()
		if f.Type != "REPORT" || f.Status != "completed" {
			t.Fatal(f)
		}
		finished[f.EventID] = true
	}
	if !finished["a"] || !finished["b"] {
		t.Fatal(finished)
	}
	if _, err := io.WriteString(send, "{}\n"); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil || !strings.Contains(err.Error(), "input line 3") {
		t.Fatalf("input error=%v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var replay, again bytes.Buffer
	if err := json.NewEncoder(&again).Encode(a); err != nil {
		t.Fatal(err)
	}
	if err := command(ctx, []string{"run", "-state", state}, &again, &replay, io.Discard); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, f := range decodeFrames(t, &replay) {
		counts[f.Type]++
		if f.Type == "REPORT" {
			delete(finished, f.EventID)
		}
	}
	if counts["REPORT"] != 2 || counts["DUPLICATE"] != 1 || counts["ACCEPTED"] != 0 || len(finished) != 0 {
		t.Fatal(counts, finished)
	}
}

func TestReconcileChecksOneEventWithoutDispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	state := filepath.Join(t.TempDir(), "review.jsonl")
	route := func(context.Context, superagent.Trigger, []superagent.TaskInfo) (superagent.Route, error) {
		return superagent.Route{Action: "new"}, nil
	}
	entered := make(chan struct{})
	receipt := superagent.ToolReceipt{Token: 1, Domain: "native", CallID: "call-1", ToolName: "publish", Committed: true, Completed: true}
	s, err := superagent.Open(ctx, state, 1, route, func(ctx context.Context, _ superagent.Job) (superagent.Outcome, error) {
		close(entered)
		<-ctx.Done()
		return superagent.Outcome{NeedsReview: true, Receipts: []superagent.ToolReceipt{receipt}}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	event := superagent.Trigger{ID: "original", Source: "operator", Thread: "release", Kind: "request", Text: "publish"}
	submit := func(s *superagent.SuperAgent, e superagent.Trigger) {
		t.Helper()
		if ok, err := s.Submit(e); !ok || err != nil {
			t.Fatalf("submit %s: ok=%v err=%v", e.ID, ok, err)
		}
	}
	submit(s, event)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("worker never started")
	}
	old := event
	old.ID, old.Text = "old-followup", "continue"
	submit(s, old)
	other := event
	other.ID, other.Thread = "unrelated", "support"
	submit(s, other)
	// Ensure this independent event is queued, outside the routing callback,
	// when the supervisor shuts down with its only worker slot occupied.
	for {
		tasks := s.Status().Tasks
		if len(tasks) == 2 && tasks[1].Status == "queued" {
			break
		}
		select {
		case <-time.After(5 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("independent event never reached the queue")
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	args := []string{"reconcile", "-state", state, "-task", "task-1", "-source", event.Source, "-event", event.ID, "-note", "Verified the release in the registry."}
	wrong := append([]string(nil), args...)
	wrong[8] = old.ID
	var out bytes.Buffer
	if err := command(ctx, wrong, nil, &out, io.Discard); err == nil || out.Len() != 0 {
		t.Fatalf("wrong event: err=%v output=%s", err, &out)
	}
	if err := command(ctx, args, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	frames := decodeFrames(t, &out)
	if len(frames) != 1 || frames[0].Type != "RECONCILED" || frames[0].TaskID != "task-1" || frames[0].EventID != event.ID || frames[0].Source != event.Source {
		t.Fatal(frames)
	}

	// Only a later supervisor may claim the unrelated queued job. The reviewed
	// task needs a fresh event, even with a follow-up admitted before the stop.
	ran := make(chan string, 3)
	reopened, err := superagent.Open(ctx, state, 1, route, func(_ context.Context, j superagent.Job) (superagent.Outcome, error) {
		ran <- j.Trigger.ID
		return superagent.Outcome{Summary: "finished"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	if err := reopened.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 {
		t.Fatalf("resumed %d workers; want only unrelated event", len(ran))
	}
	if got := <-ran; got != other.ID {
		t.Fatalf("resumed %q", got)
	}
	fresh := event
	fresh.ID, fresh.Text = "fresh", "continue after manual check"
	submit(reopened, fresh)
	if err := reopened.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || <-ran != fresh.ID {
		t.Fatal("fresh instruction did not run")
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	var replay bytes.Buffer
	if err := command(ctx, []string{"run", "-state", state}, strings.NewReader(""), &replay, io.Discard); err != nil {
		t.Fatal(err)
	}
	frames = decodeFrames(t, &replay)
	found := false
	for _, f := range frames {
		if f.Type == "REPORT" && f.EventID == event.ID {
			found = true
			if !f.NeedsReview || len(f.Receipts) != 1 || f.Receipts[0] != receipt {
				t.Fatalf("missing historical review/receipt: %+v", f)
			}
		}
	}
	last := frames[len(frames)-1]
	if !found || last.Type != "IDLE" || len(last.Tasks) == 0 || last.Tasks[0].ReviewEventID != "" || last.Tasks[0].Status != "completed" {
		t.Fatalf("current state: %+v", last)
	}
}

func TestRealProviderSelectionUsesRegisteredModel(t *testing.T) {
	models := pigo.GetModels(pigo.Provider("openai"))
	if len(models) == 0 {
		t.Fatal("missing built-in openai catalog")
	}
	model := models[0]
	def, err := modelDefinition(string(model.Provider), model.ID)
	if err != nil {
		t.Fatal(err)
	}
	stream, ref, err := def.ResolveModel(t.Context(), agent.AgentSnapshot{})
	if err != nil || stream == nil || def.Model != nil || ref.Model != model.ID || ref.Provider != string(model.Provider) {
		t.Fatalf("ref=%+v err=%v", ref, err)
	}
	for _, pair := range [][2]string{{"openai", ""}, {"", "model"}, {"unknown", "model"}} {
		if _, err := modelDefinition(pair[0], pair[1]); err == nil {
			t.Fatal(pair)
		}
	}
}
