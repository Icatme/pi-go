package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/session"
)

func controlRouter(_ context.Context, job MainJob) (MainOutcome, error) {
	d := MainDecision{Action: "new"}
	if job.Input.Kind != "user" {
		d.Action = "ignore"
	} else if strings.HasPrefix(job.Input.Trigger.ID, "control") {
		if len(job.Runs) == 0 {
			return MainOutcome{}, errors.New("no offered run")
		}
		target := job.Runs[0]
		d = MainDecision{Action: job.Input.Trigger.Text, Target: &target}
		if d.Action == "steer" {
			d.Text = "durable correction"
		}
	}
	return MainOutcome{Decision: d, Snapshot: job.Snapshot}, nil
}

func TestMainCancelPreservesLateSuccessAndDropsSteering(t *testing.T) {
	started := make(chan Job, 1)
	a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, controlRouter, func(ctx context.Context, job Job) (Outcome, error) {
		started <- job
		<-ctx.Done()
		// A real executor can finish its write despite cancellation.
		return Outcome{Summary: "late success", Snapshot: agent.AgentSnapshot{SessionID: job.TaskID}, Receipts: []ToolReceipt{{Token: 1, Domain: "host", CallID: "write", ToolName: "write", Committed: true, Completed: true}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "start", Source: "im", Thread: "work", Kind: "request"})
	job := receive(t, started)
	native, err := a.Steer(RunTarget{TaskID: job.TaskID, Source: "im", EventID: "start"}, "native correction")
	if err != nil {
		t.Fatal(err)
	}
	submit(t, a, Trigger{ID: "control-steer", Source: "im", Thread: "steer", Kind: "request", Text: "steer"})
	waitMainControl(t, a, "queued")
	submit(t, a, Trigger{ID: "control-cancel", Source: "im", Thread: "cancel", Kind: "request", Text: "cancel"})
	waitIdle(t, a)
	if delivered, err := native.Wait(t.Context()); delivered || err != nil {
		t.Fatalf("native: %v %v", delivered, err)
	}
	controls := a.MainControls()
	if len(controls) != 2 || controls[0].Status != "not_delivered" || controls[1].Status != "cancel_requested" {
		t.Fatalf("controls: %+v", controls)
	}
	for _, r := range a.Status().Reports {
		if r.EventID == "start" {
			if r.Status != "interrupted" || !r.NeedsReview || len(r.Receipts) != 1 || !r.Receipts[0].Committed {
				t.Fatalf("lost late effects: %+v", r)
			}
		}
	}
	if a.Status().Tasks[0].Status != "needs_review" {
		t.Fatal("lost review barrier")
	}
}

func TestMainNativeAbortAndFullQueueSettleDurableSteer(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(map[bool]string{false: "abort", true: "full"}[full], func(t *testing.T) {
			started := make(chan Job, 1)
			a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, controlRouter, func(ctx context.Context, job Job) (Outcome, error) {
				started <- job
				<-ctx.Done()
				return Outcome{}, ctx.Err()
			})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			submit(t, a, Trigger{ID: "start", Source: "im", Thread: "work", Kind: "request"})
			job := receive(t, started)
			target := RunTarget{TaskID: job.TaskID, Source: "im", EventID: "start"}
			var native []*SteerReceipt
			if full {
				for range maxPendingSteers {
					r, err := a.Steer(target, "fill")
					if err != nil {
						t.Fatal(err)
					}
					native = append(native, r)
				}
			}
			submit(t, a, Trigger{ID: "control", Source: "im", Thread: "control", Kind: "request", Text: "steer"})
			if full {
				waitMainControl(t, a, "not_delivered")
			} else {
				waitMainControl(t, a, "queued")
			}
			if err := a.Abort(target); err != nil {
				t.Fatal(err)
			}
			waitIdle(t, a)
			waitMainControl(t, a, "not_delivered")
			for _, r := range native {
				if ok, err := r.Wait(t.Context()); ok || err != nil {
					t.Fatal("native receipt not dropped")
				}
			}
		})
	}
}

func TestMainControlsRejectUntrustedOrMutatedTargets(t *testing.T) {
	for _, mode := range []string{"signal", "wrong_event", "wrong_task", "wrong_source", "mutate_offered", "extra_task", "cancel_text", "long_text", "result"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan Job, 2)
			route := func(ctx context.Context, job MainJob) (MainOutcome, error) {
				if job.Input.Trigger.ID != "control" && !(mode == "result" && job.Input.Kind == "worker_result") {
					return controlRouter(ctx, job)
				}
				target := RunTarget{TaskID: "task-1", Source: "im", EventID: "start"}
				if len(job.Runs) > 0 {
					target = job.Runs[0]
				}
				d := MainDecision{Action: "steer", Target: &target, Text: "correct"}
				switch mode {
				case "wrong_event":
					target.EventID = "invented"
				case "wrong_task":
					target.TaskID = "invented"
				case "wrong_source":
					target.Source = "other-domain"
				case "mutate_offered":
					job.Runs[0].EventID = "invented"
					target = job.Runs[0]
				case "extra_task":
					d.TaskID = "task-1"
				case "cancel_text":
					d.Action = "cancel"
				case "long_text":
					d.Text = strings.Repeat("x", maxSteerBytes+1)
				}
				return MainOutcome{Decision: d, Snapshot: job.Snapshot}, nil
			}
			a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, route, func(ctx context.Context, job Job) (Outcome, error) {
				started <- job
				if mode == "result" {
					return mainWorker(ctx, job)
				}
				<-ctx.Done()
				return Outcome{}, ctx.Err()
			})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			submit(t, a, Trigger{ID: "start", Source: "im", Thread: "work", Kind: "request"})
			receive(t, started)
			if mode != "result" {
				kind := "request"
				if mode == "signal" {
					kind = "signal"
				}
				submit(t, a, Trigger{ID: "control", Source: "im", Thread: "control", Kind: kind})
			}
			waitMainFailure(t, a)
			if len(a.MainControls()) != 0 {
				t.Fatal("invalid control committed")
			}
			if mode != "result" {
				if err := a.Abort(RunTarget{TaskID: "task-1", Source: "im", EventID: "start"}); err != nil {
					t.Fatal(err)
				}
			}
			waitIdle(t, a)
		})
	}
}

func waitMainFailure(t *testing.T, a *SuperAgent) {
	t.Helper()
	for {
		a.mu.Lock()
		ch := a.changed
		a.mu.Unlock()
		if len(a.MainFailures()) > 0 {
			return
		}
		select {
		case <-ch:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
}

// Construct exact durable boundaries without starting callbacks. Replay is
// then exercised through the public OpenMain API with a real scheduler.
func controlJournal(t *testing.T, action string, chat ...bool) (*SuperAgent, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.jsonl")
	a, err := openState(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	commit := func(tr transition) {
		t.Helper()
		if err := a.commit(tr); err != nil {
			t.Fatal(err)
		}
	}
	commit(transition{State: "main_opened", Main: &mainTransition{SessionID: "main-test", ProviderSessionID: "provider-test"}})
	if len(chat) > 0 && chat[0] {
		commit(transition{State: "main_chat_opened", Main: &mainTransition{}})
	}
	start := Trigger{ID: "start", Source: "im", Thread: "work", Kind: "request"}
	commit(transition{State: "received", Index: 0, Trigger: &start})
	input := a.main.inbox[0].input.ID
	commit(transition{State: "main_decided", Main: &mainTransition{InputID: input, DecisionID: "main-decision-" + input, Decision: MainDecision{Action: "new"}, Snapshot: json.RawMessage(`{"session_id":"main-test"}`), Assignment: &transition{Index: 0, State: "queued", Task: "task-1", ProviderSessionID: "worker-provider"}}})
	commit(transition{State: "running", Index: 0})
	control := Trigger{ID: "control", Source: "im", Thread: "control", Kind: "request", Text: action}
	commit(transition{State: "received", Index: 1, Trigger: &control})
	input = a.main.inbox[1].input.ID
	d := MainDecision{Action: action, Target: &RunTarget{TaskID: "task-1", Source: "im", EventID: "start"}}
	if action == "steer" {
		d.Text = "correct"
	}
	commit(transition{State: "main_decided", Main: &mainTransition{InputID: input, DecisionID: "main-decision-" + input, Decision: d, Snapshot: json.RawMessage(`{"session_id":"main-test"}`)}})
	return a, path
}

func TestMainControlReplayEveryActionBoundary(t *testing.T) {
	for _, action := range []string{"steer", "cancel"} {
		t.Run(action, func(t *testing.T) {
			a, path := controlJournal(t, action)
			id := a.main.controls[0].ID
			if action == "steer" {
				if err := a.commitControls("queued", []string{id}); err != nil {
					t.Fatal(err)
				}
				if err := a.commitControls("delivered", []string{id}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := a.commitControls("cancel_requested", []string{id}); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var prefix []byte
			for _, line := range bytes.SplitAfter(data, []byte{'\n'}) {
				if len(line) == 0 {
					continue
				}
				prefix = append(prefix, line...)
				var log session.LogItem
				if err := json.Unmarshal(line, &log); err != nil {
					t.Fatal(err)
				}
				if log.Entry == nil || log.Entry.Custom == nil {
					continue
				}
				var tr transition
				if err := json.Unmarshal(log.Entry.Custom.Payload, &tr); err != nil {
					t.Fatal(err)
				}
				if tr.State != "control_queued" && tr.State != "control_delivered" && tr.State != "control_cancel_requested" && !(tr.State == "main_decided" && tr.Main.Decision.Action == action) {
					continue
				}
				t.Run(tr.State, func(t *testing.T) {
					file := filepath.Join(t.TempDir(), "replay.jsonl")
					if err := os.WriteFile(file, prefix, 0600); err != nil {
						t.Fatal(err)
					}
					b, err := OpenMain(t.Context(), file, 1, controlRouter, func(ctx context.Context, job Job) (Outcome, error) {
						if job.Trigger.ID != "next" {
							t.Error("replayed uncertain invocation")
						}
						messages, err := job.TakeSteering(ctx, agent.QueueModeAll)
						if len(messages) != 0 || err != nil {
							t.Errorf("old control escaped: %+v %v", messages, err)
						}
						return mainWorker(ctx, job)
					})
					if err != nil {
						t.Fatal(err)
					}
					defer b.Close()
					waitIdle(t, b)
					controls := b.MainControls()
					want := "not_delivered"
					if tr.State == "control_delivered" {
						want = "needs_review"
					}
					if tr.State == "control_cancel_requested" {
						want = "cancel_requested"
					}
					if len(controls) != 1 || controls[0].ID != id || controls[0].Status != want || b.Status().Tasks[0].Status != "needs_review" {
						t.Fatalf("replay: %+v %+v", controls, b.Status())
					}
					if ok, err := b.Submit(Trigger{ID: "control", Source: "im", Thread: "control", Kind: "request", Text: action}); ok || err != nil {
						t.Fatal("control admitted twice")
					}
					if err := b.Reconcile("task-1", "im", "start", "effects inspected"); err != nil {
						t.Fatal(err)
					}
					// Same task's next invocation must not receive an old control.
					if accepted, err := b.SubmitTask(Trigger{ID: "next", Source: "im", Thread: "work", Kind: "request"}, "task-1"); !accepted || err != nil {
						t.Fatalf("task input: %v %v", accepted, err)
					}
					waitIdle(t, b)
				})
			}
		})
	}
}
