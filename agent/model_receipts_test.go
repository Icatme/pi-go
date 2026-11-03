package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/checkpoint"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/codemode"
	"github.com/Icatme/pi-go/executionreceipt"
)

func modelReceiptReply(message agent.Message) agent.AssistantStream {
	events := make(chan agent.AssistantEvent, 1)
	events <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: message}
	close(events)
	return &receiptModelStream{message: message, events: events}
}
func modelReceiptScript(calls []agent.ToolCall, requests *atomic.Int32) agent.StreamModel {
	return agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		n := int(requests.Add(1)) - 1
		message := agent.NewTextMessage(agent.RoleAssistant, "done")
		message.StopReason = agent.StopReasonStop
		if n < len(calls) {
			message.Parts = nil
			message.StopReason = agent.StopReasonToolUse
			message.ToolCalls = []agent.ToolCall{calls[n]}
		}
		return modelReceiptReply(message), nil
	})
}
func modelReceiptRunner(t *testing.T, definition agent.AgentDefinition) *agent.Runner {
	t.Helper()
	r, err := agent.NewRunner(definition)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func modelReceiptDrain(stream *agent.RunStream) (agent.AgentSnapshot, error) {
	for range stream.Events() {
	}
	return stream.Wait()
}

func TestModelReceiptsFileCommitSurvivesFailures(t *testing.T) {
	for _, mode := range []string{"after error", "after panic", "body panic", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			path := filepath.Join(t.TempDir(), "committed.txt")
			cause := errors.New("post-commit failure")
			var requests atomic.Int32
			tool := agent.ToolDefinition{Name: "write", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
				if err := executionreceipt.RequireCapture(ctx); err != nil {
					return agent.ToolResult{}, err
				}
				if err := os.WriteFile(path, []byte("committed"), 0600); err != nil {
					return agent.ToolResult{}, err
				}
				if !executionreceipt.ReportLocalCommit(ctx) {
					return agent.ToolResult{}, errors.New("capture unavailable")
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
			definition := agent.AgentDefinition{Tools: []agent.ToolDefinition{tool}, Model: modelReceiptScript([]agent.ToolCall{{ID: "write-1", Name: tool.Name}}, &requests)}
			if mode == "after error" || mode == "after panic" {
				definition.AfterToolCall = func(context.Context, agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
					if mode == "after panic" {
						panic(cause)
					}
					return agent.AfterToolCallResult{}, cause
				}
			}
			stream, h, err := modelReceiptRunner(t, definition).RunWithReceipts(ctx, agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("write")}, agent.LoopHooks{}, 1)
			if err != nil {
				t.Fatal(err)
			}
			_, runErr := modelReceiptDrain(stream)
			if mode == "cancel" && !errors.Is(runErr, context.Canceled) {
				t.Fatalf("cancel=%v", runErr)
			}
			if err := h.Wait(t.Context()); err != nil {
				t.Fatal(err)
			}
			facts := h.Snapshot()
			if len(facts) != 1 || !facts[0].Committed() || !facts[0].Completed() || facts[0].CallID() != "write-1" {
				t.Fatalf("facts=%+v runErr=%v", facts, runErr)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "committed" {
				t.Fatalf("effect=%q err=%v", data, err)
			}
		})
	}
}

func TestModelReceiptsCapacityAccumulatesAcrossTurns(t *testing.T) {
	var requests, writes atomic.Int32
	dir := t.TempDir()
	tool := agent.ToolDefinition{Name: "write", Execute: func(ctx context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
		if err := executionreceipt.RequireCapture(ctx); err != nil {
			return agent.ToolResult{}, err
		}
		if err := os.WriteFile(filepath.Join(dir, in.ToolCall.ID), []byte("committed"), 0600); err != nil {
			return agent.ToolResult{}, err
		}
		writes.Add(1)
		executionreceipt.ReportLocalCommit(ctx)
		return agent.ToolResult{}, nil
	}}
	var calls []agent.ToolCall
	for i := 1; i <= 3; i++ {
		calls = append(calls, agent.ToolCall{ID: fmt.Sprint(i), Name: tool.Name})
	}
	stream, h, err := modelReceiptRunner(t, agent.AgentDefinition{Tools: []agent.ToolDefinition{tool}, Model: modelReceiptScript(calls, &requests)}).RunWithReceipts(t.Context(), agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("write three")}, agent.LoopHooks{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := modelReceiptDrain(stream); err != nil {
		t.Fatal(err)
	}
	if err := h.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	facts := h.Snapshot()
	if writes.Load() != 2 || requests.Load() != 4 || len(facts) != 2 {
		t.Fatalf("writes=%d requests=%d facts=%+v", writes.Load(), requests.Load(), facts)
	}
	if _, err := os.Stat(filepath.Join(dir, "3")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capacity allowed third write: %v", err)
	}
	for _, f := range facts {
		if !f.Committed() || !f.Completed() || f.ParentToken() != 0 {
			t.Fatalf("turn receipt=%+v", f)
		}
	}
}

func TestModelReceiptsPermissionDenialBeforeBody(t *testing.T) {
	var requests, bodies atomic.Int32
	denied := errors.New("authorization revoked")
	tool := agent.ToolDefinition{Name: "write", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		bodies.Add(1)
		return agent.ToolResult{}, nil
	}}
	definition := agent.AgentDefinition{Tools: []agent.ToolDefinition{tool}, Model: modelReceiptScript([]agent.ToolCall{{ID: "denied", Name: tool.Name}}, &requests), CheckToolPermission: func(context.Context, agent.BeforeToolCallContext) error { return denied }}
	stream, h, err := modelReceiptRunner(t, definition).RunWithReceipts(t.Context(), agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("try")}, agent.LoopHooks{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	modelReceiptDrain(stream)
	if err := h.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if bodies.Load() != 0 || len(h.Snapshot()) != 0 {
		t.Fatalf("denied executor entered: %d %+v", bodies.Load(), h.Snapshot())
	}
}

func TestModelReceiptsCodemodeLateHostAfterStreamCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sandbox, err := codemode.NewSandbox(ctx, codemode.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close(context.Background())
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseHost := func() { once.Do(func() { close(release) }) }
	defer releaseHost()
	path := filepath.Join(t.TempDir(), "late.txt")
	leaf := agent.ToolDefinition{Name: "write", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		if err := executionreceipt.RequireCapture(ctx); err != nil {
			return agent.ToolResult{}, err
		}
		close(entered)
		<-release
		if err := os.WriteFile(path, []byte("late"), 0600); err != nil {
			return agent.ToolResult{}, err
		}
		executionreceipt.ReportLocalCommit(ctx)
		return agent.ToolResult{}, nil
	}}
	tool, err := codemodetool.New(sandbox, []codemodetool.Binding{codemodetool.Native(leaf, "fixture")}, codemodetool.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	runner := modelReceiptRunner(t, agent.AgentDefinition{Tools: []agent.ToolDefinition{tool}, Model: modelReceiptScript([]agent.ToolCall{{ID: "code", Name: tool.Name, ParsedArgs: map[string]any{"code": "await tools.write({});"}}}, &requests)})
	stream, h, err := runner.RunWithReceipts(ctx, agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("write")}, agent.LoopHooks{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := modelReceiptDrain(stream); done <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("leaf never entered")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stream waited for uncooperative host")
	}
	before := h.Snapshot()
	if len(before) != 3 || before[2].Completed() {
		t.Fatalf("stream completion prematurely sealed host: %+v", before)
	}
	waitCtx, stop := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer stop()
	if err := h.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait returned early: %v", err)
	}
	releaseHost()
	if err := h.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	facts := h.Snapshot()
	if !facts[2].Committed() || !facts[2].Completed() || facts[0].Committed() || facts[1].Committed() || facts[2].ParentToken() != facts[0].Token() || facts[1].Domain() != "codemode" {
		t.Fatalf("late facts=%+v", facts)
	}
	if before[2].Completed() || before[2].Committed() {
		t.Fatal("earlier snapshot mutated")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "late" {
		t.Fatalf("late file=%q err=%v", data, err)
	}
}

func TestModelReceiptsLegacyNestedRunnersCannotInheritCapture(t *testing.T) {
	for _, kind := range []string{"Runner", "Engine"} {
		t.Run(kind, func(t *testing.T) {
			var nestedRequests, nestedBodies, outerRequests atomic.Int32
			leaf := agent.ToolDefinition{Name: "inner", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
				nestedBodies.Add(1)
				return agent.ToolResult{}, nil
			}}
			inner := agent.AgentDefinition{Tools: []agent.ToolDefinition{leaf}, Model: modelReceiptScript([]agent.ToolCall{{ID: "inner", Name: leaf.Name}}, &nestedRequests)}
			outer := agent.ToolDefinition{Name: "outer", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
				if err := executionreceipt.RequireCapture(ctx); err != nil {
					return agent.ToolResult{}, err
				}
				if kind == "Runner" {
					modelReceiptDrain(modelReceiptRunner(t, inner).Run(ctx, agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("inner")}))
				} else {
					_, _ = agent.NewEngine().Run(ctx, inner, &agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("inner")}, nil)
				}
				if !executionreceipt.ReportLocalCommit(ctx) {
					return agent.ToolResult{}, errors.New("nested legacy run poisoned outer context")
				}
				return agent.ToolResult{}, nil
			}}
			runner := modelReceiptRunner(t, agent.AgentDefinition{Tools: []agent.ToolDefinition{outer}, Model: modelReceiptScript([]agent.ToolCall{{ID: "outer", Name: outer.Name}}, &outerRequests)})
			stream, h, err := runner.RunWithReceipts(t.Context(), agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("outer")}, agent.LoopHooks{}, 4)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := modelReceiptDrain(stream); err != nil {
				t.Fatal(err)
			}
			facts := h.Snapshot()
			if nestedRequests.Load() != 2 || nestedBodies.Load() != 0 || len(facts) != 1 || !facts[0].Committed() {
				t.Fatalf("requests=%d bodies=%d facts=%+v", nestedRequests.Load(), nestedBodies.Load(), facts)
			}
		})
	}
}

type modelReceiptJournal struct{}

func (modelReceiptJournal) Record(context.Context, agent.JournalRecord) error { return nil }
func (modelReceiptJournal) LoadState(string, string) (json.RawMessage, error) { return nil, nil }

func TestModelReceiptsRejectJournalAndCheckpointContext(t *testing.T) {
	var requests atomic.Int32
	runner := modelReceiptRunner(t, agent.AgentDefinition{Model: modelReceiptScript(nil, &requests)})
	stream, h, err := runner.RunWithReceipts(t.Context(), agent.AgentSnapshot{}, nil, agent.LoopHooks{Journal: modelReceiptJournal{}}, 1)
	if err == nil || stream != nil || h != nil || requests.Load() != 0 {
		t.Fatalf("journal accepted stream=%v handle=%v err=%v requests=%d", stream, h, err, requests.Load())
	}
	var checkpointRequests atomic.Int32
	checkpointRunner, err := checkpoint.NewRunner(checkpoint.RunnerConfig{Definition: agent.AgentDefinition{Model: agent.StreamFunc(func(ctx context.Context, _ agent.ModelRequest) (agent.AssistantStream, error) {
		checkpointRequests.Add(1)
		stream, h, err := runner.RunWithReceipts(ctx, agent.AgentSnapshot{}, nil, agent.LoopHooks{}, 1)
		if err == nil || stream != nil || h != nil {
			t.Errorf("checkpoint model context allowed nested receipt run: %v %v %v", stream, h, err)
		}
		message := agent.NewTextMessage(agent.RoleAssistant, "done")
		message.StopReason = agent.StopReasonStop
		return modelReceiptReply(message), nil
	})}, DefinitionVersion: "v1", Store: checkpoint.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	checkpointStream := checkpointRunner.Run(t.Context(), "receipt-context", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("read only")})
	for range checkpointStream.Events() {
	}
	if _, err := checkpointStream.Wait(); err != nil {
		t.Fatal(err)
	}
	if checkpointRequests.Load() != 1 || requests.Load() != 0 {
		t.Fatalf("model requests checkpoint=%d nested=%d", checkpointRequests.Load(), requests.Load())
	}
	// Ordinary, receipt-unaware model runs retain their existing behavior.
	if _, err := modelReceiptDrain(runner.Run(t.Context(), agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("read only")})); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("legacy model requests=%d", requests.Load())
	}
}

// Capture is observation, not an idempotency key or a failure latch. The model
// can request another write after a committed call reports an error. Keep this
// limitation explicit so hosts do not treat receipts as automatic retry safety.
func TestModelReceiptsKnownRetryLimitationCommittedErrorDoesNotPreventSecondWrite(t *testing.T) {
	var requests, writes atomic.Int32
	path := filepath.Join(t.TempDir(), "effects.txt")
	tool := agent.ToolDefinition{Name: "write", Execute: func(ctx context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
		if err := executionreceipt.RequireCapture(ctx); err != nil {
			return agent.ToolResult{}, err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return agent.ToolResult{}, err
		}
		_, writeErr := file.WriteString(in.ToolCall.ID + "\n")
		closeErr := file.Close()
		if writeErr != nil {
			return agent.ToolResult{}, writeErr
		}
		if closeErr != nil {
			return agent.ToolResult{}, closeErr
		}
		writes.Add(1)
		if !executionreceipt.ReportLocalCommit(ctx) {
			return agent.ToolResult{}, errors.New("commit report rejected")
		}
		if in.ToolCall.ID == "first" {
			return agent.ToolResult{}, errors.New("failure after committing first write")
		}
		return agent.ToolResult{}, nil
	}}
	runner := modelReceiptRunner(t, agent.AgentDefinition{Tools: []agent.ToolDefinition{tool}, Model: modelReceiptScript([]agent.ToolCall{{ID: "first", Name: tool.Name}, {ID: "retry", Name: tool.Name}}, &requests)})
	stream, handle, err := runner.RunWithReceipts(t.Context(), agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("write")}, agent.LoopHooks{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	var firstFailed, retrySucceeded bool
	for event := range stream.Events() {
		if event.Type != agent.EventToolExecutionEnd {
			continue
		}
		if event.ToolCallID == "first" {
			firstFailed = event.IsError
		}
		if event.ToolCallID == "retry" {
			retrySucceeded = !event.IsError
		}
	}
	if _, err := stream.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := handle.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "first\nretry\n" || writes.Load() != 2 || requests.Load() != 3 || !firstFailed || !retrySucceeded {
		t.Fatalf("effects=%q readErr=%v writes=%d requests=%d firstFailed=%v retrySucceeded=%v", data, err, writes.Load(), requests.Load(), firstFailed, retrySucceeded)
	}
	facts := handle.Snapshot()
	if len(facts) != 2 {
		t.Fatalf("facts=%+v", facts)
	}
	for i, fact := range facts {
		if !fact.Committed() || !fact.Completed() || fact.ParentToken() != 0 || fact.CallID() != []string{"first", "retry"}[i] {
			t.Fatalf("fact %d=%+v", i, fact)
		}
	}
	if facts[0].Token() == facts[1].Token() {
		t.Fatal("retry overwrote original receipt")
	}
}

func TestModelReceiptsConcurrentRunsRemainIsolated(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	tool := agent.ToolDefinition{Name: "write", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		if err := executionreceipt.RequireCapture(ctx); err != nil {
			return agent.ToolResult{}, err
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return agent.ToolResult{}, ctx.Err()
		}
		if !executionreceipt.ReportLocalCommit(ctx) {
			return agent.ToolResult{}, errors.New("missing capture")
		}
		return agent.ToolResult{}, nil
	}}
	runner, err := agent.NewRunner(agent.AgentDefinition{Tools: []agent.ToolDefinition{tool}, Model: agent.StreamFunc(func(_ context.Context, request agent.ModelRequest) (agent.AssistantStream, error) {
		m := agent.NewTextMessage(agent.RoleAssistant, "done")
		m.StopReason = agent.StopReasonStop
		id := ""
		finished := false
		for _, message := range request.Messages {
			if message.Role == agent.RoleUser && len(message.Parts) > 0 {
				id = message.Parts[0].Text
			}
			if message.Role == agent.RoleTool {
				finished = true
			}
		}
		if !finished {
			m.Parts = nil
			m.StopReason = agent.StopReasonToolUse
			m.ToolCalls = []agent.ToolCall{{ID: id, Name: tool.Name, Arguments: json.RawMessage(`{}`)}}
		}
		return modelReceiptReply(m), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	streams := make([]*agent.RunStream, 2)
	handles := make([]*executionreceipt.Handle, 2)
	for i, id := range []string{"A", "B"} {
		streams[i], handles[i], err = runner.RunWithReceipts(ctx, agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage(id)}, agent.LoopHooks{}, 1)
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for _, stream := range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range stream.Events() {
			}
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("both run bodies did not enter")
		}
	}
	releaseOnce.Do(func() { close(release) })
	for _, stream := range streams {
		if _, err = stream.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	for i, id := range []string{"A", "B"} {
		facts := handles[i].Snapshot()
		if len(facts) != 1 || facts[0].Token() != 1 || facts[0].ParentToken() != 0 || facts[0].CallID() != id || !facts[0].Committed() || !facts[0].Completed() {
			t.Fatalf("run %s: %+v", id, facts)
		}
	}
}
