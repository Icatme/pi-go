package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Icatme/pi-go/agent/session"
)

func TestMainBatchReplayEveryCommitBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.jsonl")
	entered := make(chan struct{})
	release := make(chan struct{})
	route := func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
		if job.Inputs[0].Trigger.ID == "gate" {
			select {
			case <-entered:
			default:
				close(entered)
			}
			select {
			case <-release:
			case <-ctx.Done():
				return MainBatchOutcome{}, ctx.Err()
			}
		}
		out := MainBatchOutcome{Snapshot: job.Snapshot}
		for _, input := range job.Inputs {
			d := MainDecision{Action: "reply", Text: input.Trigger.ID}
			if input.Kind == "user" && input.Trigger.ID != "gate" {
				d = MainDecision{Action: "new"}
			}
			out.Decisions = append(out.Decisions, MainInputDecision{InputID: input.ID, Decision: d})
		}
		return out, nil
	}
	a, err := OpenMainBatch(t.Context(), path, 1, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, route, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	events := []Trigger{{ID: "gate", Source: "im", Thread: "chat", Kind: "request"}, {ID: "one", Source: "im", Thread: "chat", Kind: "request"}, {ID: "two", Source: "im", Thread: "chat", Kind: "request"}}
	submit(t, a, events[0])
	receive(t, entered)
	policy := AdmissionPolicy{Version: "v1", Rules: []AdmissionRule{{Source: "im", Principal: "owner"}}}
	for _, event := range events[1:] {
		if _, err := a.Admit(policy, AdmissionEnvelope{Trigger: event, Principal: "owner", ExplicitRequest: true}); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	waitIdle(t, a)
	before := a.Replies()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(raw, []byte{'\n'})
	var prefix []byte
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		prefix = append(prefix, line...)
		if i < 1 {
			continue
		}
		var entry session.LogItem
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		var tr transition
		if err := json.Unmarshal(entry.Entry.Custom.Payload, &tr); err != nil {
			t.Fatal(err)
		}
		if tr.State == "main_opened" || tr.State == "main_chat_opened" {
			continue
		}
		t.Run(tr.State+"-"+entry.Entry.ID, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "cut.jsonl")
			if err := os.WriteFile(file, prefix, 0600); err != nil {
				t.Fatal(err)
			}
			b, err := OpenMainBatch(t.Context(), file, 1, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, route, mainWorker)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			// Finish admissions that had not yet reached this crash cut. Existing ones
			// retain their original audit and frozen input identity.
			if _, err := b.Submit(events[0]); err != nil {
				t.Fatal(err)
			}
			for _, event := range events[1:] {
				if _, err := b.Admit(policy, AdmissionEnvelope{Trigger: event, Principal: "owner", ExplicitRequest: true}); err != nil {
					t.Fatal(err)
				}
			}
			waitIdle(t, b)
			if len(b.Status().Tasks) != 2 || len(b.Replies()) != 3 || len(b.MainFailures()) != 0 || len(b.Admissions()) != 2 {
				t.Fatalf("lost/partial replay: %+v %+v %+v", b.Status(), b.Replies(), b.MainFailures())
			}
			seen := map[string]bool{}
			for _, reply := range b.Replies() {
				if seen[reply.InputID] || reply.ID != "reply-"+reply.InputID {
					t.Fatal("duplicate reply identity")
				}
				seen[reply.InputID] = true
			}
			// Full replay must preserve the exact correlation and statuses. Earlier
			// running cuts instead report interruption/review, never fabricated success.
			if bytes.Equal(prefix, raw) && !reflect.DeepEqual(before, b.Replies()) {
				t.Fatal("completed batch replay changed results")
			}
			if tr.State == "running" {
				found := false
				for _, reply := range b.Replies() {
					if reply.Report != nil && reply.Report.Status == "interrupted" {
						found = reply.Report.NeedsReview
					}
				}
				if !found {
					t.Fatal("interrupted effects lost")
				}
			}
		})
	}
}

func TestMainFailedJournalCannotCarryActions(t *testing.T) {
	a, err := openState(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.commit(transition{State: "main_opened", Main: &mainTransition{SessionID: "main", ProviderSessionID: "provider"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.commit(transition{State: "main_chat_opened", Main: &mainTransition{}}); err != nil {
		t.Fatal(err)
	}
	trigger := Trigger{ID: "request", Source: "im", Thread: "chat", Kind: "request"}
	if err := a.commit(transition{State: "received", Index: 0, Trigger: &trigger}); err != nil {
		t.Fatal(err)
	}
	id := a.main.inbox[0].input.ID
	delegation := mainTransition{InputID: id, Decision: MainDecision{Action: "new"}, Assignment: &transition{State: "queued", Index: 0, Task: "task-1", ProviderSessionID: "provider-task"}}
	for _, tr := range []transition{
		{State: "main_failed", Text: "failure", Main: &delegation},
		{State: "main_batch_failed", Text: "failure", Main: &mainTransition{Batch: []mainTransition{delegation}}},
	} {
		if err := a.apply(tr, true); err == nil {
			t.Fatalf("%s replay accepted hidden delegation", tr.State)
		}
		if len(a.tasks) != 0 || a.main.cursor != 0 || len(a.reports) != 0 {
			t.Fatal("failed validation mutated reducer")
		}
	}
}
