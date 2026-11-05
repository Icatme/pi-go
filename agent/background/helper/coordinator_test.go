//go:build linux

package helper

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

func TestSuperAgentRestartPreservesReviewAndLateCompletion(t *testing.T) {
	b, cfg := fixture(t)
	path := filepath.Join(t.TempDir(), "coordinator.jsonl")
	router := func(_ context.Context, j superagent.MainJob) (superagent.MainOutcome, error) {
		action := "ignore"
		if j.Input.Kind == "user" {
			action = "new"
		}
		return superagent.MainOutcome{Decision: superagent.MainDecision{Action: action}, Snapshot: j.Snapshot}, nil
	}
	worker := func(ctx context.Context, j superagent.Job) (superagent.Outcome, error) {
		if j.Trigger.ID == "first" {
			_, e := j.Background.Start(ctx, superagent.BackgroundStart{Tool: "work", CallID: "original-call", Arguments: json.RawMessage(`{"mode":"late"}`)})
			if e != nil {
				return superagent.Outcome{}, e
			}
		}
		return superagent.Outcome{Summary: "handled", Snapshot: agent.AgentSnapshot{SessionID: j.TaskID}}, nil
	}
	opts := func(b *Backend) superagent.BackgroundOptions {
		return superagent.BackgroundOptions{MaxConcurrent: 1, PollInterval: time.Millisecond, Tools: []superagent.BackgroundTool{{Name: "work", Scope: "fixture", PolicyRevision: "v1", Backend: b, Authorize: func(context.Context, superagent.BackgroundAuthorization) error { return nil }}}}
	}
	a, e := superagent.OpenMainBackground(t.Context(), path, 1, router, worker, opts(b))
	if e != nil {
		t.Fatal(e)
	}
	if _, e := a.Submit(superagent.Trigger{ID: "first", Source: "fixture", Thread: "chat", Kind: "request", Text: "start"}); e != nil {
		t.Fatal(e)
	}
	if e := a.WaitIdle(t.Context()); e != nil {
		t.Fatal(e)
	}
	operations := a.BackgroundOperations()
	if len(operations) != 1 {
		t.Fatalf("no durable launch: %+v", operations)
	}
	original := operations[0]
	dir := entered(t, b, original.ID)
	t.Cleanup(func() { marker(t, dir, "release") })
	if original.NeedsReview {
		t.Fatal("normal helper startup quarantined the task")
	}
	if _, e := a.SubmitTask(superagent.Trigger{ID: "followup", Source: "fixture", Thread: "chat", Kind: "request", Text: "continue while helper runs"}, original.TaskID); e != nil {
		t.Fatal(e)
	}
	if e := a.WaitIdle(t.Context()); e != nil || len(a.ReportsSince(0)) != 2 {
		t.Fatalf("startup interrupted normal follow-up: %v", e)
	}
	a.Close()
	b.Close()
	next, e := Open(b.dir, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	a, e = superagent.OpenMainBackground(t.Context(), path, 1, router, worker, opts(next))
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	o := a.BackgroundOperations()[0]
	if o.ID != original.ID || o.TaskID != original.TaskID || o.CallID != "original-call" || o.DeliveryID != original.DeliveryID || !o.NeedsReview || o.Exited {
		t.Fatalf("restart changed identity or cleared review: %+v", o)
	}
	if e := a.ReconcileBackground(o.ID, "a note cannot prove exit"); e == nil {
		t.Fatal("unknown operation reconciled")
	}
	if e := a.StopBackground(t.Context(), o.ID); e != nil {
		t.Fatal(e)
	}
	waitFor(t, func() bool {
		// The fixture reports a late native commit but still holds its body.
		_, e := os.Stat(filepath.Join(dir, "committed"))
		return e == nil
	})
	if a.BackgroundOperations()[0].Exited {
		t.Fatal("stop acknowledged as body exit")
	}
	marker(t, dir, "release")
	waitFor(t, func() bool {
		_ = a.RefreshBackground(t.Context(), o.ID)
		return a.BackgroundOperations()[0].Exited
	})
	o = a.BackgroundOperations()[0]
	if !o.StopRequested || !o.NeedsReview || len(o.Receipts) != 1 || !o.Receipts[0].Committed || !o.Receipts[0].Completed || o.CompletionEventID != o.ID {
		t.Fatalf("completion lost authoritative late facts: %+v", o)
	}
	if e := a.ReconcileBackground(o.ID, "fixture body exited; late commit reviewed"); e != nil {
		t.Fatal(e)
	}
	if e := a.Close(); e != nil {
		t.Fatal(e)
	}
	a, e = superagent.OpenMainBackground(t.Context(), path, 1, router, worker, opts(next))
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	final := a.BackgroundOperations()
	if len(final) != 1 || !final[0].Reconciled || final[0].CompletionEventID != original.ID {
		t.Fatal("replay duplicated or lost completion/review")
	}
}
