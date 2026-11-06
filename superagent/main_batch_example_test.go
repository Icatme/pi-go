package superagent_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Icatme/pi-go/superagent"
)

func ExampleOpenMainBatch() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "superagent-batch-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	route := func(_ context.Context, job superagent.MainBatchJob) (superagent.MainBatchOutcome, error) {
		out := superagent.MainBatchOutcome{Snapshot: job.Snapshot}
		for _, input := range job.Inputs {
			out.Decisions = append(out.Decisions, superagent.MainInputDecision{InputID: input.ID, Decision: superagent.MainDecision{Action: "reply", Text: "Received: " + input.Trigger.Text}})
		}
		return out, nil
	}
	// This fixture only replies. A production host may supply PiMainBatchRouter
	// and a Worker with independently authorized tools and model settings.
	worker := func(context.Context, superagent.Job) (superagent.Outcome, error) { panic("unexpected delegation") }
	a, err := superagent.OpenMainBatch(ctx, filepath.Join(dir, "state.jsonl"), 2, superagent.MainBatchOptions{MaxItems: 16, MaxInputBytes: 128 << 10}, route, worker)
	if err != nil {
		panic(err)
	}
	defer a.Close()
	policy := superagent.AdmissionPolicy{Version: "chat-policy-v1", Rules: []superagent.AdmissionRule{{Source: "im", Principal: "authenticated-owner"}}}
	input := superagent.AdmissionEnvelope{Trigger: superagent.Trigger{ID: "message-1", Source: "im", Thread: "private-chat", Kind: "request", Text: "Hello"}, Principal: "authenticated-owner", ExplicitRequest: true}
	receipt, err := a.Admit(policy, input)
	if err != nil {
		panic(err)
	}
	// Identity and intent above must come from the host's authenticated adapter,
	// not user-supplied JSON or text. Duplicate delivery preserves the first audit.
	repeated, err := a.Admit(policy, input)
	if err != nil {
		panic(err)
	}
	signal := input
	signal.Trigger.ID = "presence-1"
	signal.Trigger.Kind = "signal"
	signal.ExplicitRequest = false
	filtered, err := a.Admit(policy, signal)
	if err != nil {
		panic(err)
	}
	if err := a.WaitIdle(ctx); err != nil {
		panic(err)
	}
	fmt.Println(receipt.Record.Reason, repeated.Duplicate, filtered.Record.Reason)
	fmt.Println(a.Replies()[0].Text)
	// Output:
	// explicit_request true unassociated_signal
	// Received: Hello
}
