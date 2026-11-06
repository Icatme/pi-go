package superagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

// Build the exact durable decision boundary without starting a scheduler.
func replyJournal(t *testing.T) (*SuperAgent, string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.jsonl")
	a, err := openState(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	trigger := Trigger{ID: "request", Source: "im", Thread: "chat", Kind: "request"}
	for _, tr := range []transition{
		{State: "main_opened", Main: &mainTransition{SessionID: "main-test", ProviderSessionID: "provider-test"}},
		{State: "received", Index: 0, Trigger: &trigger},
		{State: "main_decided", Main: &mainTransition{InputID: "main-test/input-1", DecisionID: "main-decision-main-test/input-1", Decision: MainDecision{Action: "reply", Text: "A reply"}, Snapshot: json.RawMessage(`{"sessionId":"main-test"}`), ReplyID: "reply-main-test/input-1"}},
	} {
		if err := a.commit(tr); err != nil {
			t.Fatal(err)
		}
	}
	return a, path, a.Replies()[0].ID
}

func TestReplyDeliveryReplayEveryTransition(t *testing.T) {
	a, path, id := replyJournal(t)
	check := func(name string) {
		t.Helper()
		want := a.Replies()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "boundary.jsonl")
			if err := os.WriteFile(file, data, 0o600); err != nil {
				t.Fatal(err)
			}
			b, err := OpenMain(t.Context(), file, 1, func(context.Context, MainJob) (MainOutcome, error) {
				t.Error("delivery transition woke main")
				return MainOutcome{}, nil
			}, func(context.Context, Job) (Outcome, error) {
				t.Error("delivery transition dispatched Worker")
				return Outcome{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			waitIdle(t, b)
			if got := b.Replies(); !reflect.DeepEqual(got, want) {
				t.Fatalf("replay changed delivery: got=%+v want=%+v", got, want)
			}
			if got := b.Status(); len(got.Tasks) != 0 || len(got.Reports) != 1 {
				t.Fatal("delivery affected work history")
			}
		})
	}
	check("committed_decision_before_send")
	first, err := a.BeginReplyDelivery(id)
	if err != nil {
		t.Fatal(err)
	}
	check("send_claimed_before_result")
	if err := a.ResolveReplyDelivery(first, ReplyDeliveryResult{Status: "unknown"}); err != nil {
		t.Fatal(err)
	}
	check("send_unknown")
	if err := a.ResolveReplyDelivery(first, ReplyDeliveryResult{Status: "not_delivered", Note: "Transport confirmed no delivery and no in-flight send."}); err != nil {
		t.Fatal(err)
	}
	check("verified_not_delivered")
	second, err := a.BeginReplyDelivery(id)
	if err != nil {
		t.Fatal(err)
	}
	check("retry_claimed")
	if err := a.ObserveReply("im", "chat", second, "message-1"); err != nil {
		t.Fatal(err)
	}
	check("delivered")
	before := len(a.store.Log())
	if err := a.ObserveReply("im", "chat", second, "message-1"); err != nil || len(a.store.Log()) != before {
		t.Fatal("duplicate receipt appended another record")
	}
}

func TestReplyDeliveryAppendFailureDoesNotPublish(t *testing.T) {
	for _, resolve := range []bool{false, true} {
		t.Run(map[bool]string{false: "claim", true: "result"}[resolve], func(t *testing.T) {
			a, _, id := replyJournal(t)
			var delivery ReplyDelivery
			if resolve {
				var err error
				delivery, err = a.BeginReplyDelivery(id)
				if err != nil {
					t.Fatal(err)
				}
			}
			before := a.Replies()
			if err := a.store.Close(); err != nil {
				t.Fatal(err)
			}
			if resolve {
				if err := a.ResolveReplyDelivery(delivery, ReplyDeliveryResult{Status: "delivered", MessageID: "m1"}); err == nil {
					t.Fatal("failed result append succeeded")
				}
			} else if handle, err := a.BeginReplyDelivery(id); err == nil || handle != (ReplyDelivery{}) {
				t.Fatal("failed claim handed off a usable send handle")
			}
			if !reflect.DeepEqual(a.Replies(), before) || a.fatal == nil {
				t.Fatal("failed append changed state or did not fail stop")
			}
		})
	}
}

func TestReplyDeliveryLegacyJournalUpgrade(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		t.Run(map[bool]string{false: "unconfirmed", true: "acknowledged"}[acknowledged], func(t *testing.T) {
			a, path, id := replyJournal(t)
			if err := a.commit(transition{State: "reply_started", Main: &mainTransition{ReplyID: id}}); err != nil {
				t.Fatal(err)
			}
			if acknowledged {
				if err := a.commit(transition{State: "reply_acknowledged", Main: &mainTransition{ReplyID: id}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			b, err := OpenMain(t.Context(), path, 1, mainFixture, mainWorker)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			got := b.Replies()[0]
			if len(got.Attempts) != 1 || got.Attempts[0].Delivery != (ReplyDelivery{ReplyID: id, Attempt: 1}) || got.Attempts[0].MessageID != "" {
				t.Fatal("legacy delivery did not retain original identity")
			}
			if _, err := b.BeginReplyDelivery(id); err == nil {
				t.Fatal("legacy delivery was automatically retryable")
			}
			if acknowledged {
				if got.Status != "acknowledged" {
					t.Fatal("legacy acknowledgement lost")
				}
			} else {
				if got.Status != "needs_review" {
					t.Fatal("legacy unknown was not preserved")
				}
				if err := b.ObserveReply(got.Source, got.Thread, got.Attempts[0].Delivery, "legacy-confirmed"); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestReplyDeliveryIdentityCollisionDoesNotPoisonJournal(t *testing.T) {
	a, _, id := replyJournal(t)
	first, err := a.BeginReplyDelivery(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ObserveReply("im", "chat", first, "platform-message"); err != nil {
		t.Fatal(err)
	}
	trigger := Trigger{ID: "second", Source: "im", Thread: "chat", Kind: "request"}
	if err := a.commit(transition{State: "received", Index: 1, Trigger: &trigger}); err != nil {
		t.Fatal(err)
	}
	input := a.main.inbox[1].input.ID
	secondID := "reply-" + input
	if err := a.commit(transition{State: "main_decided", Main: &mainTransition{InputID: input, DecisionID: "main-decision-" + input, Decision: MainDecision{Action: "reply", Text: "Another reply"}, Snapshot: json.RawMessage(`{"sessionId":"main-test"}`), ReplyID: secondID}}); err != nil {
		t.Fatal(err)
	}
	second, err := a.BeginReplyDelivery(secondID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"platform-message", "second"} {
		before := len(a.store.Log())
		if err := a.ObserveReply("im", "chat", second, message); err == nil || len(a.store.Log()) != before || a.fatal != nil {
			t.Fatal("colliding identity changed or poisoned journal")
		}
	}
	if err := a.ObserveReply("im", "chat", second, "another-message"); err != nil {
		t.Fatal(err)
	}
}

func TestReplyDeliveryUncertainSendDoesNotBlockWork(t *testing.T) {
	a, path, id := replyJournal(t)
	if _, err := a.BeginReplyDelivery(id); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMain(t.Context(), path, 1, mainFixture, func(_ context.Context, job Job) (Outcome, error) {
		return Outcome{Summary: "independent work", Snapshot: agent.AgentSnapshot{SessionID: job.TaskID}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	submit(t, b, Trigger{ID: "other", Source: "im", Thread: "other", Kind: "request"})
	waitIdle(t, b)
	if len(b.Replies()) != 2 || b.Replies()[0].Status != "needs_review" || b.Replies()[1].Report.Status != "completed" {
		t.Fatal("unknown delivery blocked unrelated work or lost review")
	}
}
