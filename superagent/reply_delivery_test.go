package superagent_test

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

func replyHost(t *testing.T, path string, calls *atomic.Int32) *superagent.SuperAgent {
	t.Helper()
	a, err := superagent.OpenMain(t.Context(), path, 1, func(_ context.Context, job superagent.MainJob) (superagent.MainOutcome, error) {
		calls.Add(1)
		decision := superagent.MainDecision{Action: "new"}
		if job.Input.Kind == "worker_result" {
			decision = superagent.MainDecision{Action: "reply", Text: "Work partly failed; a write committed. Review required."}
		}
		return superagent.MainOutcome{Decision: decision, Snapshot: job.Snapshot}, nil
	}, func(_ context.Context, job superagent.Job) (superagent.Outcome, error) {
		calls.Add(1)
		return superagent.Outcome{Summary: "Partial result", Status: "completed_with_tool_errors", NeedsReview: true,
			Snapshot: agent.AgentSnapshot{SessionID: job.TaskID},
			Receipts: []superagent.ToolReceipt{{Token: 1, Domain: "host", CallID: "write-1", ToolName: "write", Committed: true, Completed: true}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func admitReply(t *testing.T, a *superagent.SuperAgent) superagent.Reply {
	t.Helper()
	if ok, err := a.Submit(superagent.Trigger{ID: "request", Source: "im", Thread: "chat", Kind: "request", Text: "Do work"}); !ok || err != nil {
		t.Fatalf("submit: %v %v", ok, err)
	}
	if err := a.WaitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := a.Replies(); len(got) == 1 {
		return got[0]
	}
	t.Fatal("missing reply")
	return superagent.Reply{}
}

func TestReplyDeliveryReconcileRetryAndEchoRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	var calls atomic.Int32
	a := replyHost(t, path, &calls)
	reply := admitReply(t, a)
	first, err := a.BeginReplyDelivery(reply.ID)
	if err != nil || first.ReplyID != reply.ID || first.Attempt != 1 {
		t.Fatalf("claim: %+v %v", first, err)
	}
	unknown := superagent.ReplyDeliveryResult{Status: "unknown", Note: "Connection ended without a receipt."}
	if err := a.ResolveReplyDelivery(first, unknown); err != nil {
		t.Fatal(err)
	}
	if err := a.AcknowledgeReply(reply.ID); err == nil {
		t.Fatal("unscoped acknowledgement cleared an external send")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a = replyHost(t, path, &calls)
	if _, err := a.BeginReplyDelivery(reply.ID); err == nil || a.Replies()[0].Status != "needs_review" {
		t.Fatal("restart automatically retried an uncertain send")
	}
	missing := superagent.ReplyDeliveryResult{Status: "not_delivered", Note: "Authoritative transport lookup confirmed no message was created and the send has ended."}
	if err := a.ResolveReplyDelivery(first, missing); err != nil {
		t.Fatal(err)
	}
	if err := a.AcknowledgeReply(reply.ID); err == nil {
		t.Fatal("memory acknowledgement bypassed retry ownership")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a = replyHost(t, path, &calls)
	second, err := a.BeginReplyDelivery(reply.ID)
	if err != nil || second.ReplyID != first.ReplyID || second.Attempt != 2 {
		t.Fatalf("retry: %+v %v", second, err)
	}
	for _, result := range []superagent.ReplyDeliveryResult{unknown, {Status: "delivered", MessageID: "late"}} {
		if err := a.ResolveReplyDelivery(first, result); err == nil {
			t.Fatal("stale conflicting receipt accepted")
		}
	}
	// An exact repeated prior terminal receipt is harmless, never a reset of
	// the current send. This is safe even when its callback was delayed.
	if err := a.ResolveReplyDelivery(first, missing); err != nil || a.Replies()[0].Status != "needs_review" {
		t.Fatal("duplicate prior receipt affected the current attempt")
	}
	if err := a.ObserveReply("other", "chat", second, "message-1"); err == nil {
		t.Fatal("wrong source authenticated a receipt")
	}
	if err := a.ObserveReply("im", "other", second, "message-1"); err == nil {
		t.Fatal("wrong thread authenticated a receipt")
	}
	// A trusted adapter correlates an early own-message echo to the exact send
	// before submitting any remaining business input.
	if err := a.ObserveReply("im", "chat", second, "message-1"); err != nil {
		t.Fatal(err)
	}
	if err := a.ObserveReply("im", "chat", second, "message-1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a = replyHost(t, path, &calls)
	before := calls.Load()
	if ok, err := a.Submit(superagent.Trigger{ID: "message-1", Source: "im", Thread: "chat", Kind: "request", Text: reply.Text}); ok || err != nil {
		t.Fatalf("self echo not filtered: %v %v", ok, err)
	}
	if err := a.WaitIdle(t.Context()); err != nil || calls.Load() != before {
		t.Fatal("echo reached main or bound Worker")
	}
	got := a.Replies()[0]
	if got.Status != "acknowledged" || len(got.Attempts) != 2 || got.Attempts[0].Status != "not_delivered" || got.Attempts[1].MessageID != "message-1" {
		t.Fatalf("delivery history lost: %+v", got)
	}
	if got.Report.Status != "completed_with_tool_errors" || !got.Report.NeedsReview || !got.Report.Receipts[0].Committed || a.Status().Tasks[0].Status != "needs_review" {
		t.Fatal("delivery confirmation rewrote work facts or cleared review")
	}
	got.Attempts[0].Status = "mutated"
	if a.Replies()[0].Attempts[0].Status != "not_delivered" {
		t.Fatal("outbox read aliases delivery history")
	}
	for _, trigger := range []superagent.Trigger{
		{ID: "message-1", Source: "other", Thread: "chat", Kind: "request", Text: reply.Text},
		{ID: "message-1", Source: "im", Thread: "other", Kind: "request", Text: reply.Text},
		{ID: "different", Source: "im", Thread: "chat", Kind: "request", Text: reply.Text},
	} {
		if ok, err := a.Submit(trigger); !ok || err != nil {
			t.Fatalf("unrelated message filtered: %+v %v", trigger, err)
		}
	}
}

func TestReplyDeliveryClaimHasOneWinner(t *testing.T) {
	var calls atomic.Int32
	a := replyHost(t, filepath.Join(t.TempDir(), "state.jsonl"), &calls)
	reply := admitReply(t, a)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			handle, err := a.BeginReplyDelivery(reply.ID)
			if err == nil {
				winners.Add(1)
				if handle.Attempt != 1 || handle.ReplyID != reply.ID {
					t.Error("invalid winning handle")
				}
			} else if handle != (superagent.ReplyDelivery{}) {
				t.Error("failed claimant received a usable handle")
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 || len(a.Replies()[0].Attempts) != 1 {
		t.Fatal("concurrent send claims were not exclusive")
	}
}

func TestReplyDeliveryInvalidReceiptsDoNotPoisonState(t *testing.T) {
	var calls atomic.Int32
	a := replyHost(t, filepath.Join(t.TempDir(), "state.jsonl"), &calls)
	reply := admitReply(t, a)
	delivery, err := a.BeginReplyDelivery(reply.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []superagent.ReplyDelivery{{}, {ReplyID: reply.ID}, {ReplyID: reply.ID, Attempt: 2}, {ReplyID: "other", Attempt: 1}} {
		if err := a.ResolveReplyDelivery(bad, superagent.ReplyDeliveryResult{Status: "delivered", MessageID: "m1"}); err == nil {
			t.Errorf("invalid handle accepted: %+v", bad)
		}
	}
	for _, bad := range []superagent.ReplyDeliveryResult{
		{}, {Status: "delivered"}, {Status: "not_delivered"}, {Status: "not_delivered", MessageID: "m1", Note: "absent"},
		{Status: "unknown", MessageID: "m1"}, {Status: "sent", MessageID: "m1"}, {Status: "delivered", MessageID: "\xff"},
	} {
		if err := a.ResolveReplyDelivery(delivery, bad); err == nil {
			t.Errorf("invalid result accepted: %+v", bad)
		}
	}
	ok := superagent.ReplyDeliveryResult{Status: "delivered", MessageID: "m1"}
	if err := a.ResolveReplyDelivery(delivery, ok); err != nil {
		t.Fatal(err)
	}
	if err := a.ResolveReplyDelivery(delivery, ok); err != nil {
		t.Fatal("duplicate result is not idempotent")
	}
	if err := a.ResolveReplyDelivery(delivery, superagent.ReplyDeliveryResult{Status: "delivered", MessageID: "different"}); err == nil {
		t.Fatal("conflicting platform identity overwritten")
	}
	if a.Replies()[0].Status != "acknowledged" {
		t.Fatal("invalid receipts poisoned state")
	}
}
