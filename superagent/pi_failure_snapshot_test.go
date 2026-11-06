package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/executionreceipt"
)

func TestPiFailedRunPersistsDeliveredSteerAndToolReceipt(t *testing.T) {
	const correction = "Use the corrected destination."
	const credential = "synthetic-private-provider-key"
	path := filepath.Join(t.TempDir(), "failure.jsonl")
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	work, err := PiWorker(agent.AgentDefinition{
		MaxTurns:     1,
		DefaultModel: agent.ModelRef{Provider: "example", Model: "offline", ProviderConfig: agent.ProviderConfig{APIKey: credential}},
		Model: agent.StreamFunc(func(ctx context.Context, _ agent.ModelRequest) (agent.AssistantStream, error) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			msg := agent.NewTextMessage(agent.RoleAssistant, "Inspecting the change.")
			msg.StopReason = agent.StopReasonToolUse
			msg.ToolCalls = []agent.ToolCall{{ID: "call-1", Name: "write", Arguments: json.RawMessage(`{}`)}}
			return piReply(msg), nil
		}),
		Tools: []agent.ToolDefinition{{Name: "write", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
			if !executionreceipt.ReportLocalCommit(ctx) {
				return agent.ToolResult{}, errors.New("missing receipt scope")
			}
			return agent.ToolResult{Content: []agent.Part{agent.NewTextPart("private tool output")}}, nil
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	route := func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }
	a, err := Open(t.Context(), path, 1, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	defer resume()
	event := Trigger{ID: "start", Source: "chat", Thread: "test", Kind: "request", Text: "Initial instruction."}
	submit(t, a, event)
	receive(t, entered)
	rcpt, err := a.Steer(RunTarget{TaskID: "task-1", Source: "chat", EventID: "start"}, correction)
	if err != nil {
		t.Fatal(err)
	}
	resume()
	assertSteerDelivery(t, rcpt, true)
	waitIdle(t, a)
	if !errors.Is(a.FailureCause("chat", "start"), agent.ErrMaxTurnsExceeded) {
		t.Fatal(a.FailureCause("chat", "start"))
	}
	check := func(t *testing.T, a *SuperAgent) {
		t.Helper()
		view := a.Status()
		if len(view.Reports) != 1 || len(view.Tasks) != 1 {
			t.Fatalf("view=%+v", view)
		}
		r := view.Reports[0]
		if r.Status != "failed" || !r.NeedsReview || len(r.Receipts) != 1 || !r.Receipts[0].Committed || view.Tasks[0].Status != "needs_review" {
			t.Fatal(view)
		}
		snap, ok, err := a.TaskSnapshot("task-1")
		if err != nil || !ok || len(snap.Messages) < 4 {
			t.Fatalf("partial snapshot present=%v messages=%d err=%v", ok, len(snap.Messages), err)
		}
		last := snap.Messages[len(snap.Messages)-1]
		if last.Role != agent.RoleUser || messageText(last) != correction || snap.Messages[len(snap.Messages)-2].Role != agent.RoleTool || snap.Error != "" || snap.Model.ProviderConfig.APIKey != "" {
			t.Fatalf("unexpected partial snapshot roles, steering, or diagnostic fields")
		}
	}
	check(t, a)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	followup, err := PiWorker(agent.AgentDefinition{Model: agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
		var history []string
		for _, m := range req.Messages {
			if m.Role == agent.RoleUser || m.Role == agent.RoleTool {
				history = append(history, messageText(m))
			}
		}
		if len(history) != 4 || !strings.Contains(history[0], "Initial instruction.") || history[1] != "private tool output" || history[2] != correction || !strings.Contains(history[3], "New instruction.") {
			return nil, errors.New("new run did not receive the reconciled tool and steering history")
		}
		return offlineAnswer("Continued without replaying the write."), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(t.Context(), path, 1, route, followup)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	check(t, b)
	if err := b.Reconcile("task-1", "chat", "start", "Confirmed the write."); err != nil {
		t.Fatal(err)
	}
	submit(t, b, Trigger{ID: "fresh", Source: "chat", Thread: "test", Kind: "request", Text: "New instruction."})
	waitIdle(t, b)
	if reports := b.ReportsSince(1); len(reports) != 1 || reports[0].Status != "completed" || len(reports[0].Receipts) != 0 {
		t.Fatalf("reconciled follow-up=%+v cause=%v", reports, b.FailureCause("chat", "fresh"))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(credential)) {
		t.Fatal("model credential reached journal")
	}
}

func TestPiFailedModelKeepsHistoryWithoutPrivateDiagnostic(t *testing.T) {
	const marker = "synthetic-private-provider-diagnostic"
	cause := errors.New(marker)
	work, err := PiWorker(agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		msg := agent.NewTextMessage(agent.RoleAssistant, "Accepted partial output.")
		msg.StopReason, msg.ErrorMessage = agent.StopReasonError, marker
		events := make(chan agent.AssistantEvent, 1)
		events <- agent.AssistantEvent{Type: agent.AssistantEventError, Message: msg, Err: cause}
		close(events)
		return &offlineStream{final: msg, events: events}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "provider-failure.jsonl")
	a, err := Open(t.Context(), path, 1, func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "e", Source: "s", Thread: "t", Kind: "request", Text: "Preserve this request."})
	waitIdle(t, a)
	if err := a.FailureCause("s", "e"); !errors.Is(err, cause) {
		t.Fatalf("lost trusted provider cause: %v", err)
	}
	if r := a.ReportsSince(0); len(r) != 1 || r[0].Status != "failed" || r[0].NeedsReview {
		t.Fatalf("unexpected report: %+v", r)
	}
	snap, ok, err := a.TaskSnapshot("task-1")
	if err != nil || !ok || len(snap.Messages) < 2 || snap.Messages[len(snap.Messages)-1].StopReason != agent.StopReasonError {
		t.Fatalf("missing failed assistant evidence: ok=%v err=%v", ok, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte("Accepted partial output.")) || bytes.Contains(data, []byte(marker)) {
		t.Fatalf("failed provider transcript missing or diagnostic reached journal: %v", err)
	}
}

func TestPiProgressPanicReturnsFinishedTranscript(t *testing.T) {
	work, err := PiWorker(agent.AgentDefinition{Model: OfflineModel()})
	if err != nil {
		t.Fatal(err)
	}
	out, err := work(t.Context(), Job{TaskID: "T", Trigger: Trigger{Kind: "request", Text: "Keep the final message."}, NotifyProgress: func(kind string) {
		if kind == "turn_end" {
			panic("private observer failure")
		}
	}})
	if err == nil || out.Snapshot.SessionID != "T" || len(out.Snapshot.Messages) < 2 {
		t.Fatalf("progress error=%v snapshot messages=%d", err, len(out.Snapshot.Messages))
	}
	if tail := out.Snapshot.Messages[len(out.Snapshot.Messages)-1]; tail.Role != agent.RoleAssistant || !bytes.Contains([]byte(messageText(tail)), []byte("Keep the final message.")) {
		t.Fatal("lost final assistant data accepted before progress callback failed")
	}
}

func TestPiAbortedRunRetainsAcceptedPromptForReconciliation(t *testing.T) {
	entered := make(chan struct{})
	work, err := PiWorker(agent.AgentDefinition{Model: agent.StreamFunc(func(ctx context.Context, _ agent.ModelRequest) (agent.AssistantStream, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})})
	if err != nil {
		t.Fatal(err)
	}
	a, err := Open(t.Context(), filepath.Join(t.TempDir(), "aborted.jsonl"), 1, func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "work", Source: "host", Thread: "t", Kind: "request", Text: "accepted before cancellation"})
	receive(t, entered)
	if err := a.Abort(RunTarget{TaskID: "task-1", Source: "host", EventID: "work"}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	v := a.Status()
	if len(v.Reports) != 1 || v.Reports[0].Status != "interrupted" || !v.Reports[0].NeedsReview || v.Tasks[0].Status != "needs_review" {
		t.Fatal(v)
	}
	snap, ok, err := a.TaskSnapshot("task-1")
	if err != nil || !ok || len(snap.Messages) == 0 || snap.SessionID != "task-1" {
		t.Fatalf("interrupted partial snapshot present=%v messages=%d err=%v", ok, len(snap.Messages), err)
	}
}

func TestPiRefusedInvocationDoesNotOfferEmptyReplacement(t *testing.T) {
	work, err := PiWorker(agent.AgentDefinition{Model: OfflineModel()})
	if err != nil {
		t.Fatal(err)
	}
	// The native runner rejects this input before it accepts any messages.
	out, err := work(t.Context(), Job{TaskID: "T", Snapshot: agent.AgentSnapshot{SessionID: "T", Messages: []agent.Message{agent.NewUserTextMessage("Earlier turn.")}, PendingToolCalls: []agent.PendingToolCall{{ToolCallID: "p", ToolName: "pending"}}}})
	if !errors.Is(err, agent.ErrPendingToolCallsRequireResume) || out.Snapshot.SessionID != "" || len(out.Snapshot.PendingToolCalls) != 0 {
		t.Fatalf("unexpected replacement of prior snapshot on preflight failure: messages=%d err=%v", len(out.Snapshot.Messages), err)
	}
}

func TestPiRedactedAssistantErrorKeepsFailureMeaningOnFollowup(t *testing.T) {
	for _, original := range []agent.StopReason{agent.StopReasonStop, agent.StopReasonAborted} {
		t.Run(string(original), func(t *testing.T) {
			want := agent.StopReasonError
			if original == agent.StopReasonAborted {
				want = agent.StopReasonAborted
			}
			calls := 0
			model := agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
				calls++
				if calls == 1 {
					m := agent.NewTextMessage(agent.RoleAssistant, "Apparent success.")
					m.StopReason, m.ErrorMessage = original, "private model diagnostic"
					return piReply(m), nil
				}
				for _, m := range req.Messages {
					if m.Role == agent.RoleAssistant && messageText(m) == "Apparent success." && m.StopReason == want && m.ErrorMessage == "" {
						return offlineAnswer("Prior attempt was unsuccessful."), nil
					}
				}
				return nil, errors.New("redaction promoted the failed answer to a successful history entry")
			})
			work, err := PiWorker(agent.AgentDefinition{Model: model})
			if err != nil {
				t.Fatal(err)
			}
			job := Job{TaskID: "T", Trigger: Trigger{Text: "attempt"}}
			first, err := work(t.Context(), job)
			if err == nil {
				t.Fatal("expected model error")
			}
			job.Snapshot = first.Snapshot
			job.Trigger.Text = "follow-up"
			out, err := work(t.Context(), job)
			if err != nil || out.Summary != "Prior attempt was unsuccessful." {
				t.Fatalf("follow-up report=%q err=%v", out.Summary, err)
			}
		})
	}
}

func TestPiObserverFailurePreservesIndependentProviderCause(t *testing.T) {
	entered := make(chan struct{})
	providerErr := errors.New("private concurrent provider cause")
	observerErr := errors.New("private observer cause")
	work, err := PiWorker(agent.AgentDefinition{Model: agent.StreamFunc(func(ctx context.Context, _ agent.ModelRequest) (agent.AssistantStream, error) {
		close(entered)
		<-ctx.Done()
		return nil, errors.Join(providerErr, ctx.Err())
	})})
	if err != nil {
		t.Fatal(err)
	}
	a, err := Open(t.Context(), filepath.Join(t.TempDir(), "observer.jsonl"), 1, func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }, func(ctx context.Context, job Job) (Outcome, error) {
		job.NotifyProgress = func(kind string) {
			if kind == "turn_start" {
				select {
				case <-entered:
					panic(observerErr)
				case <-ctx.Done():
				}
			}
		}
		return work(ctx, job)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "e", Source: "s", Thread: "t", Kind: "request", Text: "start"})
	waitIdle(t, a)
	cause := a.FailureCause("s", "e")
	if !errors.Is(cause, observerErr) || !errors.Is(cause, providerErr) || errors.Is(cause, context.Canceled) {
		t.Fatalf("incorrect cause chain: %v", cause)
	}
	if reports := a.ReportsSince(0); len(reports) != 1 || reports[0].Status != "failed" || reports[0].NeedsReview {
		t.Fatal(reports)
	}
}
