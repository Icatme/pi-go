package superagent_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

// This is the first M1a acceptance test: a Go host, with no CLI or external
// service, delegates once and receives a durable reply from its original main
// session. Before M1a, OpenMain and the main-session/reply contract do not exist.
func TestMainSessionWorkerReplySurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.jsonl")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var calls, workers atomic.Int32
	var mainID, providerID string
	router := func(_ context.Context, job superagent.MainJob) (superagent.MainOutcome, error) {
		turn := calls.Add(1)
		if turn == 1 {
			mainID, providerID = job.SessionID, job.ProviderSessionID
			if mainID == "" || providerID == "" || mainID == providerID || job.Input.Kind != "user" {
				t.Errorf("invalid initial main identity/input: %+v", job)
			}
			return superagent.MainOutcome{
				Decision: superagent.MainDecision{Action: "new"},
				Snapshot: agent.AgentSnapshot{SessionID: mainID, Metadata: map[string]any{"delegated": job.Input.ID}},
			}, nil
		}
		if job.SessionID != mainID || job.ProviderSessionID != providerID || job.Input.Kind != "worker_result" || job.Input.Report == nil {
			t.Errorf("worker result did not return to original main: %+v", job)
		}
		if job.Snapshot.Metadata["delegated"] == nil || job.Input.DelegationID == "" || job.Input.Report.Status != "completed" {
			t.Errorf("delegation history/report missing: %+v", job)
		}
		return superagent.MainOutcome{Decision: superagent.MainDecision{Action: "reply", Text: "The tests passed."}, Snapshot: job.Snapshot}, nil
	}
	work := func(_ context.Context, job superagent.Job) (superagent.Outcome, error) {
		workers.Add(1)
		return superagent.Outcome{Summary: "Tests passed", Snapshot: agent.AgentSnapshot{SessionID: job.TaskID}}, nil
	}
	a, err := superagent.OpenMain(ctx, path, 1, router, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	event := superagent.Trigger{ID: "request-1", Source: "im", Thread: "original-chat", Kind: "request", Text: "Run the tests"}
	if ok, err := a.Submit(event); !ok || err != nil {
		t.Fatalf("submit: %v %v", ok, err)
	}
	if err := a.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	replies := a.Replies()
	if calls.Load() != 2 || workers.Load() != 1 || len(replies) != 1 {
		t.Fatalf("main=%d workers=%d replies=%+v", calls.Load(), workers.Load(), replies)
	}
	reply := replies[0]
	if reply.ID == "" || reply.DecisionID == "" || reply.TaskID == "" || reply.Source != event.Source || reply.Thread != event.Thread || reply.EventID != event.ID || reply.Status != "pending" || reply.Report == nil {
		t.Fatalf("incomplete delivery association: %+v", reply)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := superagent.OpenMain(ctx, path, 1, router, work)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if ok, err := b.Submit(event); ok || err != nil {
		t.Fatalf("duplicate: %v %v", ok, err)
	}
	if err := b.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if got := b.Replies(); len(got) != 1 || got[0].ID != reply.ID || got[0].DecisionID != reply.DecisionID || got[0].Status != "pending" {
		t.Fatalf("reply changed on reopen: %+v", got)
	}
	if calls.Load() != 2 || workers.Load() != 1 {
		t.Fatal("reopen reissued completed work")
	}
	if err := b.AcknowledgeReply(reply.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.AcknowledgeReply(reply.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := superagent.OpenMain(ctx, path, 1, router, work)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := c.Replies(); len(got) != 1 || got[0].Status != "acknowledged" {
		t.Fatalf("ack lost: %+v", got)
	}
}
