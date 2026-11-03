package agent_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/codemode"
	"github.com/Icatme/pi-go/executionreceipt"
)

func TestReceiptsCommitSurvivesFinalizationFailure(t *testing.T) {
	for _, mode := range []string{"after error", "after panic", "body panic", "cancel", "validation"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cause := errors.New("fixture failure")
			tool := agent.ToolDefinition{Name: "write", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
				if err := executionreceipt.RequireCapture(ctx); err != nil {
					return agent.ToolResult{}, err
				}
				if !executionreceipt.ReportLocalCommit(ctx) {
					t.Error("reserved executor could not report its commit")
				}
				if mode == "body panic" {
					panic(cause)
				}
				if mode == "cancel" {
					cancel()
					return agent.ToolResult{}, ctx.Err()
				}
				return agent.ToolResult{}, nil
			}}
			options := agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}}
			if mode == "validation" {
				options.Tools[0].ValidateResult = func(agent.ToolResult) error { return cause }
			}
			if mode == "after error" || mode == "after panic" {
				options.AfterToolCall = func(context.Context, agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
					if mode == "after panic" {
						panic(cause)
					}
					return agent.AfterToolCallResult{}, cause
				}
			}
			out, handle, err := agent.RunToolCallWithReceipts(ctx, agent.ToolCall{ID: "direct", Name: tool.Name}, options, 1)
			if err != nil {
				t.Fatal(err)
			}
			if out.Err == nil || !out.IsError {
				t.Fatalf("expected failed outcome: %+v", out)
			}
			if err := handle.Wait(t.Context()); err != nil {
				t.Fatal(err)
			}
			receipts := handle.Snapshot()
			if len(receipts) != 1 {
				t.Fatalf("receipts=%+v", receipts)
			}
			r := receipts[0]
			if r.Token() == 0 || r.ParentToken() != 0 || r.CallID() != "direct" || r.ToolName() != "write" || !r.Committed() || !r.Completed() {
				t.Fatalf("lost executor commit: %+v", r)
			}
		})
	}
}

func TestReceiptsRequiredBeforeIO(t *testing.T) {
	var writes atomic.Int32
	tool := agent.ToolDefinition{Name: "write", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		if err := executionreceipt.RequireCapture(ctx); err != nil {
			return agent.ToolResult{}, err
		}
		writes.Add(1)
		executionreceipt.ReportLocalCommit(ctx)
		return agent.ToolResult{}, nil
	}}
	if executionreceipt.ReportLocalCommit(context.Background()) {
		t.Fatal("unbound report succeeded")
	}
	out := agent.RunToolCall(t.Context(), agent.ToolCall{Name: "write"}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
	if out.Err == nil || writes.Load() != 0 {
		t.Fatalf("unguarded I/O: outcome=%+v writes=%d", out, writes.Load())
	}
}

func TestReceiptsSuccessDoesNotInferCommitAndRejectsLateReports(t *testing.T) {
	var captured context.Context
	tool := agent.ToolDefinition{Name: "read", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		captured = ctx
		return agent.ToolResult{}, executionreceipt.RequireCapture(ctx)
	}}
	out, handle, err := agent.RunToolCallWithReceipts(t.Context(), agent.ToolCall{ID: "read", Name: tool.Name}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}}, 1)
	if err != nil || out.Err != nil {
		t.Fatalf("wrapper=%v outcome=%+v", err, out)
	}
	if err := executionreceipt.RequireCapture(captured); err == nil {
		t.Fatal("completed executor still has capture capability")
	}
	if executionreceipt.ReportLocalCommit(captured) {
		t.Fatal("late report accepted")
	}
	r := handle.Snapshot()[0]
	if !r.Completed() || r.Committed() {
		t.Fatalf("success inferred an effect: %+v", r)
	}
}

func TestReceiptsInvalidCapacityDoesNotExecute(t *testing.T) {
	for _, capacity := range []int{-1, 0, 4097} {
		entered := false
		tool := agent.ToolDefinition{Name: "write", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
			entered = true
			return agent.ToolResult{}, nil
		}}
		_, handle, err := agent.RunToolCallWithReceipts(t.Context(), agent.ToolCall{Name: tool.Name}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}}, capacity)
		if err == nil || handle != nil || entered {
			t.Fatalf("capacity=%d handle=%v error=%v entered=%v", capacity, handle, err, entered)
		}
	}
}

func TestReceiptsCodemodeChildren(t *testing.T) {
	for _, dynamic := range []bool{false, true} {
		for _, capacity := range []int{1, 2, 3} {
			name := "static"
			if dynamic {
				name = "dynamic"
			}
			if capacity < 3 {
				name += fmt.Sprintf("/exhausted-%d", capacity)
			} else {
				name += "/captured"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				sandbox, err := codemode.NewSandbox(ctx, codemode.DefaultConfig())
				if err != nil {
					t.Fatal(err)
				}
				defer sandbox.Close(context.Background())
				var writes atomic.Int32
				leaf := agent.ToolDefinition{Name: "write", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
					if err := executionreceipt.RequireCapture(ctx); err != nil {
						return agent.ToolResult{}, err
					}
					writes.Add(1)
					if !executionreceipt.ReportLocalCommit(ctx) {
						return agent.ToolResult{}, errors.New("commit report rejected")
					}
					return agent.ToolResult{}, nil
				}}
				bindings := []codemodetool.Binding{codemodetool.Native(leaf, "fixture")}
				tool, err := codemodetool.New(sandbox, bindings, codemodetool.Options{})
				if dynamic {
					tool, err = codemodetool.NewDynamic(sandbox, func(context.Context) ([]codemodetool.Binding, []codemode.Namespace, error) { return bindings, nil, nil }, codemodetool.Options{})
				}
				if err != nil {
					t.Fatal(err)
				}
				out, handle, err := agent.RunToolCallWithReceipts(ctx, agent.ToolCall{ID: "script", Name: tool.Name, ParsedArgs: map[string]any{"code": "await tools.write({});"}}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}}, capacity)
				if err != nil {
					t.Fatal(err)
				}
				if err := handle.Wait(ctx); err != nil {
					t.Fatal(err)
				}
				receipts := handle.Snapshot()
				if len(receipts) != capacity {
					t.Fatalf("receipts=%+v outcome=%+v", receipts, out)
				}
				parent := receipts[0]
				if parent.Committed() || !parent.Completed() || parent.ParentToken() != 0 {
					t.Fatalf("parent borrowed child commit: %+v", parent)
				}
				if capacity < 3 {
					if writes.Load() != 0 {
						t.Fatal("child body ran after ledger capacity exhaustion")
					}
				} else {
					if out.Err != nil || writes.Load() != 1 {
						t.Fatalf("outcome=%+v writes=%d", out, writes.Load())
					}
					child := receipts[2]
					host := receipts[1]
					if host.Domain() != "codemode" || host.Committed() || !host.Completed() || host.ParentToken() != parent.Token() {
						t.Fatalf("host receipt=%+v", host)
					}
					if !child.Committed() || !child.Completed() || child.Token() == parent.Token() || child.ParentToken() != parent.Token() || child.CallID() == parent.CallID() || child.ToolName() != "write" || child.Domain() != "agent" {
						t.Fatalf("child identity/commit lost: %+v", child)
					}
					receipts[0] = child
					if handle.Snapshot()[0].Token() != parent.Token() {
						t.Fatal("snapshot aliases ledger storage")
					}
				}
			})
		}
	}
}

func TestReceiptsWaitTracksActualChildReturn(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	childDone := make(chan struct{})
	var releaseOnce sync.Once
	releaseChild := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseChild()
	leaf := agent.ToolDefinition{Name: "slow", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		close(entered)
		<-release
		executionreceipt.ReportLocalCommit(ctx)
		return agent.ToolResult{}, nil
	}}
	parent := agent.ToolDefinition{Name: "parent", ChildTools: []agent.ToolDefinition{leaf}, Execute: func(ctx context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
		go func() { defer close(childDone); in.ChildCaller.Call(ctx, agent.ToolCall{Name: "slow"}) }()
		<-entered
		return agent.ToolResult{}, nil
	}}
	_, handle, err := agent.RunToolCallWithReceipts(t.Context(), agent.ToolCall{ID: "parent", Name: "parent"}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{parent}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	before := handle.Snapshot()
	if len(before) != 2 || !before[0].Completed() || before[1].Completed() || before[1].Committed() {
		t.Fatalf("premature completion: %+v", before)
	}
	waitCtx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := handle.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait returned before actual executor: %v", err)
	}
	releaseChild()
	if err := handle.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-childDone
	after := handle.Snapshot()
	if !after[1].Completed() || !after[1].Committed() || after[0].Committed() {
		t.Fatalf("late child commit lost: %+v", after)
	}
	if before[1].Completed() || before[1].Committed() {
		t.Fatal("old snapshot changed after executor completed")
	}
}
