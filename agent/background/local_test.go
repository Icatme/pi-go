package background

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/executionreceipt"
	"github.com/Icatme/pi-go/internal/receiptcapture"
)

func request(id string) Request {
	return Request{ID: id, Scope: "test", Call: agent.ToolCall{ID: id, Name: "work", ParsedArgs: map[string]any{"value": "original"}}}
}
func local(t *testing.T, options agent.RunToolCallOptions) *Local {
	t.Helper()
	l, e := NewLocal(t.Context(), "test", options, 1)
	if e != nil {
		t.Fatal(e)
	}
	return l
}
func inspect(t *testing.T, l *Local, id string) Observation {
	t.Helper()
	o, e := l.Inspect(t.Context(), id)
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func exited(t *testing.T, l *Local, id string) Observation {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		o := inspect(t, l, id)
		if o.State == "exited" {
			return o
		}
		select {
		case <-deadline:
			t.Fatal("body never exited")
		case <-time.After(time.Millisecond):
		}
	}
}
func TestStartIdentitySnapshotsAndScope(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	l := local(t, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{{Name: "work", Execute: func(ctx context.Context, tc agent.ToolExecutionContext) (agent.ToolResult, error) {
		calls.Add(1)
		close(entered)
		<-release
		return agent.ToolResult{Details: tc.Args}, executionreceipt.RequireCapture(ctx)
	}}}})
	r := request("one")
	ctx, cancel := context.WithCancel(t.Context())
	if e := l.Start(ctx, r); e != nil {
		t.Fatal(e)
	}
	cancel()
	<-entered
	if e := l.Start(t.Context(), r); e != nil {
		t.Fatal(e)
	}
	r.Call.ParsedArgs["value"] = "changed"
	if e := l.Start(t.Context(), r); e == nil {
		t.Fatal("conflict accepted")
	}
	bad := request("bad")
	bad.Scope = "other"
	if e := l.Start(t.Context(), bad); e == nil {
		t.Fatal("scope accepted")
	}
	if inspect(t, l, "never").State != "unknown" {
		t.Fatal("missing identity not unknown")
	}
	if inspect(t, l, "one").State != "running" {
		t.Fatal("dispatch cancellation stopped operation")
	}
	close(release)
	o := exited(t, l, "one")
	if calls.Load() != 1 || !strings.Contains(string(o.Output), "original") {
		t.Fatalf("bad snapshot %+v", o)
	}
	o.Output[0] = '!'
	o.Receipts[0].CallID = "mutated"
	next := inspect(t, l, "one")
	if !json.Valid(next.Output) || next.Receipts[0].CallID != "one" {
		t.Fatal("snapshot aliases")
	}
}
func TestStopTombstoneAndValidation(t *testing.T) {
	var calls atomic.Int32
	l := local(t, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{{Name: "work", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		calls.Add(1)
		return agent.ToolResult{}, nil
	}}}})
	if e := l.Stop(t.Context(), "early"); e != nil {
		t.Fatal(e)
	}
	if inspect(t, l, "early").State != "unknown" {
		t.Fatal("unbound tombstone claimed exit")
	}
	if e := l.Start(t.Context(), request("early")); e == nil {
		t.Fatal("tombstone started")
	}
	if calls.Load() != 0 || inspect(t, l, "early").State != "exited" {
		t.Fatal("tombstone lost")
	}
	for _, r := range []Request{{ID: "", Scope: "test"}, {ID: "bad", Scope: "test", Call: agent.ToolCall{Name: "unlisted"}}, {ID: "json", Scope: "test", Call: agent.ToolCall{Name: "work", Arguments: json.RawMessage(`{"broken"`)}}} {
		if e := l.Start(t.Context(), r); e == nil {
			t.Fatalf("invalid accepted %+v", r)
		}
	}
}
func TestLateCommitKeepsQuotaUntilBodyExit(t *testing.T) {
	entered, canceled, commit, committed, release := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	l := local(t, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{{Name: "work", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-commit
		if !executionreceipt.ReportLocalCommit(ctx) {
			t.Error("late commit rejected")
		}
		close(committed)
		<-release
		return agent.ToolResult{}, nil
	}}}})
	if e := l.Start(t.Context(), request("one")); e != nil {
		t.Fatal(e)
	}
	<-entered
	if e := l.Stop(t.Context(), "one"); e != nil {
		t.Fatal(e)
	}
	<-canceled
	if e := l.Start(t.Context(), request("two")); e == nil {
		t.Fatal("released live body quota")
	}
	if inspect(t, l, "one").State != "running" {
		t.Fatal("false exit")
	}
	close(commit)
	<-committed
	close(release)
	o := exited(t, l, "one")
	if len(o.Receipts) != 1 || !o.Receipts[0].Committed || !o.Receipts[0].Completed {
		t.Fatalf("lost late receipt %+v", o)
	}
}
func TestFailureOutputAndReceipts(t *testing.T) {
	for _, mode := range []string{"permission", "panic", "error", "oversize", "encoding", "encoding panic"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			opts := agent.RunToolCallOptions{Tools: []agent.ToolDefinition{{Name: "work", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
				calls.Add(1)
				if e := executionreceipt.RequireCapture(ctx); e != nil {
					return agent.ToolResult{}, e
				}
				executionreceipt.ReportLocalCommit(ctx)
				switch mode {
				case "panic":
					panic("private-token")
				case "error":
					return agent.ToolResult{}, errors.New("private-token")
				case "oversize":
					return agent.ToolResult{Details: strings.Repeat("x", 65537)}, nil
				case "encoding panic":
					return agent.ToolResult{Details: panicJSON{}}, nil
				case "encoding":
					return agent.ToolResult{Details: make(chan int)}, nil
				}
				return agent.ToolResult{}, nil
			}}}}
			if mode == "permission" {
				opts.CheckToolPermission = func(context.Context, agent.BeforeToolCallContext) error { return errors.New("private-token") }
			}
			l := local(t, opts)
			if e := l.Start(t.Context(), request(mode)); e != nil {
				t.Fatal(e)
			}
			o := exited(t, l, mode)
			if !o.Failed || len(o.Output) != 0 {
				t.Fatalf("unsafe failure output %+v", o)
			}
			if mode == "permission" {
				if calls.Load() != 0 || len(o.Receipts) != 0 {
					t.Fatal("permission widened")
				}
			} else if len(o.Receipts) != 1 || !o.Receipts[0].Committed {
				t.Fatal("lost failure receipt")
			}
		})
	}
}

type panicJSON struct{}

func (panicJSON) MarshalJSON() ([]byte, error) { panic("private-token") }

func TestHostLifetimeAndRealExitQuota(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, seenCancel, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	l, e := NewLocal(root, "test", agent.RunToolCallOptions{Tools: []agent.ToolDefinition{{Name: "work", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		close(seenCancel)
		<-release
		return agent.ToolResult{}, nil
	}}}}, 1)
	if e != nil {
		t.Fatal(e)
	}
	if e = l.Start(t.Context(), request("one")); e != nil {
		t.Fatal(e)
	}
	<-entered
	cancel()
	<-seenCancel
	if inspect(t, l, "one").State != "running" {
		t.Fatal("host cancellation claimed exit")
	}
	close(release)
	exited(t, l, "one")
	if e = l.Start(t.Context(), request("two")); e == nil {
		t.Fatal("canceled host admitted work")
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected body")
	}
}
func TestQuotaReleasedAfterExit(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	l := local(t, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{{Name: "work", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			<-release
		}
		return agent.ToolResult{}, nil
	}}}})
	if e := l.Start(t.Context(), request("one")); e != nil {
		t.Fatal(e)
	}
	<-entered
	l.Stop(t.Context(), "one")
	if e := l.Start(t.Context(), request("two")); e == nil {
		t.Fatal("early quota reuse")
	}
	close(release)
	exited(t, l, "one")
	if e := l.Start(t.Context(), request("two")); e != nil {
		t.Fatal(e)
	}
	exited(t, l, "two")
	if calls.Load() != 2 {
		t.Fatal("quota not reused")
	}
}

func TestReceiptWrapperGuardRejectsNestedHost(t *testing.T) {
	root, h, e := receiptcapture.Begin(t.Context(), 1)
	if e != nil {
		t.Fatal(e)
	}
	defer receiptcapture.CloseRegistration(h)
	var calls atomic.Int32
	l, e := NewLocal(root, "test", agent.RunToolCallOptions{Tools: []agent.ToolDefinition{{Name: "work", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		calls.Add(1)
		return agent.ToolResult{}, nil
	}}}}, 1)
	if e != nil {
		t.Fatal(e)
	}
	if e = l.Start(t.Context(), request("guard")); e != nil {
		t.Fatal(e)
	}
	o := exited(t, l, "guard")
	if !o.Failed || calls.Load() != 0 || len(o.Receipts) != 0 || len(o.Output) != 0 {
		t.Fatalf("receipt guard bypassed %+v", o)
	}
}
func TestConcurrentDuplicateAdmission(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	l := local(t, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{{Name: "work", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return agent.ToolResult{}, nil
	}}}})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Go(func() {
			if e := l.Start(t.Context(), request("shared")); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	<-entered
	close(release)
	exited(t, l, "shared")
	if calls.Load() != 1 {
		t.Fatal("duplicate physical executor")
	}
}

func TestLostInstanceStopNeverProvesExit(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	options := agent.RunToolCallOptions{Tools: []agent.ToolDefinition{{Name: "work", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		close(entered)
		<-release
		return agent.ToolResult{}, nil
	}}}}
	original := local(t, options)
	if err := original.Start(t.Context(), request("lost")); err != nil {
		t.Fatal(err)
	}
	<-entered
	replacement := local(t, options)
	for i := 0; i < 2; i++ {
		if err := replacement.Stop(t.Context(), "lost"); err != nil {
			t.Fatal(err)
		}
		if inspect(t, replacement, "lost").State != "unknown" {
			t.Fatal("replacement stop fabricated evidence of prior exit")
		}
	}
	if inspect(t, original, "lost").State != "running" {
		t.Fatal("original body unexpectedly exited")
	}
	close(release)
	exited(t, original, "lost")
	if inspect(t, replacement, "lost").State != "unknown" {
		t.Fatal("replacement inferred exit without evidence")
	}
}
func TestStoppedDispatchBindsImmutableIdentity(t *testing.T) {
	var calls atomic.Int32
	l := local(t, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{{Name: "work", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		calls.Add(1)
		return agent.ToolResult{}, nil
	}}}})
	if e := l.Stop(t.Context(), "early"); e != nil {
		t.Fatal(e)
	}
	r := request("early")
	for i := 0; i < 2; i++ {
		if e := l.Start(t.Context(), r); e == nil {
			t.Fatal("stopped dispatch accepted")
		}
	}
	if o := inspect(t, l, "early"); o.State != "exited" || !o.Failed {
		t.Fatalf("stopped dispatch not confirmed %+v", o)
	}
	r.Call.ParsedArgs["value"] = "conflict"
	if e := l.Start(t.Context(), r); e == nil {
		t.Fatal("tombstone identity replaced")
	}
	if calls.Load() != 0 {
		t.Fatal("stopped body entered")
	}
}
