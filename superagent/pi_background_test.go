package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/background"
	"github.com/Icatme/pi-go/executionreceipt"
)

func TestPiBackgroundNativeProtocolAndSameWorkerInput(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch_%v", batch), func(t *testing.T) { testPiBackgroundNativeProtocolAndSameWorkerInput(t, batch) })
	}
}

func testPiBackgroundNativeProtocolAndSameWorkerInput(t *testing.T, batch bool) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	leaf := agent.ToolDefinition{Name: "slow", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		if err := executionreceipt.RequireCapture(ctx); err != nil {
			return agent.ToolResult{}, err
		}
		close(entered)
		<-release
		if !executionreceipt.ReportLocalCommit(ctx) {
			return agent.ToolResult{}, errors.New("receipt lost")
		}
		return agent.ToolResult{Content: []agent.Part{agent.NewTextPart("PRIVATE TOOL OUTPUT")}}, nil
	}}
	backend, err := background.NewLocal(t.Context(), "workspace-A", agent.RunToolCallOptions{Tools: []agent.ToolDefinition{leaf}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var follow, completion, results atomic.Int32
	model := agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
		var last agent.Message
		for _, m := range req.Messages {
			if m.Role != agent.RoleSystem {
				last = m
			}
		}
		if last.Role == agent.RoleTool {
			if last.ToolResult.ToolCallID != "native-launch" {
				return nil, errors.New("tool response changed owner")
			}
			results.Add(1)
			return offlineAnswer("Background operation started."), nil
		}
		var envelope struct {
			Trigger    Trigger               `json:"trigger"`
			Completion *BackgroundCompletion `json:"background_completion"`
		}
		if err := json.Unmarshal([]byte(messageText(last)), &envelope); err != nil {
			return nil, fmt.Errorf("envelope decode last=%+v: %w", last, err)
		}
		if envelope.Completion != nil {
			if envelope.Completion.CallID != "native-launch" || !strings.Contains(string(envelope.Completion.Output), "PRIVATE TOOL OUTPUT") || last.Role != agent.RoleUser {
				return nil, errors.New("completion protocol or data lost")
			}
			completion.Add(1)
			return offlineAnswer("Background finished with a recorded commit."), nil
		}
		var trig Trigger
		if err := json.Unmarshal([]byte(messageText(last)), &trig); err != nil {
			return nil, err
		}
		if trig.ID == "followup" {
			follow.Add(1)
			return offlineAnswer("Processed the new input."), nil
		}
		msg := agent.NewTextMessage(agent.RoleAssistant, "")
		msg.StopReason = agent.StopReasonToolUse
		msg.ToolCalls = []agent.ToolCall{{ID: "native-launch", Name: "background_start", Arguments: json.RawMessage(`{"tool":"slow","arguments":{}}`)}}
		return piReply(msg), nil
	})
	work, err := PiBackgroundWorker(agent.AgentDefinition{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	var a *SuperAgent
	if batch {
		options := backgroundOptions(backend)
		a, err = OpenMainJournal(t.Context(), leasedMemory(t), MainJournalOptions{Parallel: 1, BatchRouter: backgroundBatch, BatchOptions: MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, Worker: work, Background: &options})
	} else {
		a, err = OpenMainBackground(t.Context(), filepath.Join(t.TempDir(), "native.jsonl"), 1, bgMain, work, backgroundOptions(backend))
	}
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start"})
	waitIdle(t, a)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatalf("native body not entered: operations=%+v reports=%+v cause=%v", a.BackgroundOperations(), a.ReportsSince(0), a.FailureCause("im", "first"))
	}
	operations := a.BackgroundOperations()
	if len(operations) != 1 || !operations[0].Started {
		t.Fatalf("no durable Started: %+v", operations)
	}
	if _, err = a.SubmitTask(Trigger{ID: "followup", Source: "im", Thread: "chat", Kind: "request", Text: "change scope"}, operations[0].TaskID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	if follow.Load() != 1 || completion.Load() != 0 || results.Load() != 1 {
		t.Fatal("Worker did not process new input while tool was blocked")
	}
	close(release)
	waitBG(t, a, func(o BackgroundOperation) bool { return o.Exited })
	waitIdle(t, a)
	if completion.Load() != 1 || results.Load() != 1 {
		t.Fatal("completion emitted a second native tool response")
	}
	for _, r := range a.ReportsSince(0) {
		raw, _ := json.Marshal(r)
		if strings.Contains(string(raw), "PRIVATE TOOL OUTPUT") {
			t.Fatal("private background output published")
		}
	}
	snapshot, ok, e := a.TaskSnapshot(operations[0].TaskID)
	if e != nil || !ok {
		t.Fatal(e)
	}
	n := 0
	for _, m := range snapshot.Messages {
		if m.ToolResult != nil && m.ToolResult.ToolCallID == "native-launch" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("native request/result cardinality %d", n)
	}
}

func TestPiBackgroundToolsStrictArgumentsAndPermission(t *testing.T) {
	for _, input := range []string{`{"tool":"slow","arguments":{},"scope":"other"}`, `{"tool":"slow","tool":"slow","arguments":{}}`, `{"tool":"slow","arguments":null}`} {
		tools := piBackgroundTools(nil)
		out, handle, err := agent.RunToolCallWithReceipts(t.Context(), agent.ToolCall{ID: "x", Name: "background_start", Arguments: json.RawMessage(input)}, agent.RunToolCallOptions{Tools: tools}, 4)
		if err != nil {
			t.Fatal(err)
		}
		if handle != nil {
			if err = handle.Wait(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if !out.IsError {
			t.Fatalf("accepted unauthorized fields: %s", input)
		}
	}
	calls := 0
	tools := piBackgroundTools(nil)
	out, handle, err := agent.RunToolCallWithReceipts(t.Context(), agent.ToolCall{ID: "x", Name: "background_start", Arguments: json.RawMessage(`{"tool":"slow","arguments":{}}`)}, agent.RunToolCallOptions{Tools: tools, CheckToolPermission: func(context.Context, agent.BeforeToolCallContext) error { calls++; return errors.New("denied") }}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if handle != nil {
		_ = handle.Wait(t.Context())
	}
	if !out.IsError || calls != 1 {
		t.Fatalf("native permission path bypassed: calls=%d outcome=%+v", calls, out)
	}
}

func TestPiBackgroundUncertaintyFencesSubsequentSynchronousTools(t *testing.T) {
	var writes atomic.Int32
	backend := scriptedBackground{start: func(context.Context, background.Request) error { return errors.New("dispatch uncertain") }}
	model := agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
		var last agent.Message
		for _, m := range req.Messages {
			if m.Role != agent.RoleSystem {
				last = m
			}
		}
		call := agent.ToolCall{ID: "launch", Name: "background_start", Arguments: json.RawMessage(`{"tool":"slow","arguments":{}}`)}
		if last.Role == agent.RoleTool {
			if last.ToolResult.ToolName == "write" {
				return offlineAnswer("Need host review."), nil
			}
			call = agent.ToolCall{ID: "later", Name: "write", Arguments: json.RawMessage(`{}`)}
		}
		msg := agent.NewTextMessage(agent.RoleAssistant, "")
		msg.StopReason = agent.StopReasonToolUse
		msg.ToolCalls = []agent.ToolCall{call}
		return piReply(msg), nil
	})
	work, e := PiBackgroundWorker(agent.AgentDefinition{Model: model, Tools: []agent.ToolDefinition{{Name: "write", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		writes.Add(1)
		return agent.ToolResult{}, nil
	}}}})
	if e != nil {
		t.Fatal(e)
	}
	a, e := OpenMainBackground(t.Context(), filepath.Join(t.TempDir(), "retry-fence.jsonl"), 1, bgMain, work, backgroundOptions(backend))
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start"})
	waitIdle(t, a)
	if writes.Load() != 0 || len(a.BackgroundOperations()) != 1 || !a.BackgroundOperations()[0].NeedsReview {
		t.Fatal("uncertain background launch allowed later synchronous side effects")
	}
}

func TestPiBackgroundRechecksBarrierAfterHostPermission(t *testing.T) {
	entered, allow := make(chan struct{}), make(chan struct{})
	var released atomic.Bool
	release := func() {
		if released.CompareAndSwap(false, true) {
			close(allow)
		}
	}
	defer release()
	var unknown atomic.Bool
	var writes atomic.Int32
	backend := scriptedBackground{inspect: func(_ context.Context, id string) (background.Observation, error) {
		state := "running"
		if unknown.Load() {
			state = "unknown"
		}
		return background.Observation{ID: id, State: state}, nil
	}}
	model := agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
		var last agent.Message
		for _, m := range req.Messages {
			if m.Role != agent.RoleSystem {
				last = m
			}
		}
		call := agent.ToolCall{ID: "launch", Name: "background_start", Arguments: json.RawMessage(`{"tool":"slow","arguments":{}}`)}
		if last.Role == agent.RoleTool {
			if last.ToolResult.ToolName == "write" {
				return offlineAnswer("Need review."), nil
			}
			call = agent.ToolCall{ID: "later", Name: "write", Arguments: json.RawMessage(`{}`)}
		}
		msg := agent.NewTextMessage(agent.RoleAssistant, "")
		msg.StopReason = agent.StopReasonToolUse
		msg.ToolCalls = []agent.ToolCall{call}
		return piReply(msg), nil
	})
	work, e := PiBackgroundWorker(agent.AgentDefinition{Model: model, CheckToolPermission: func(_ context.Context, in agent.BeforeToolCallContext) error {
		if in.ToolCall.Name == "write" {
			close(entered)
			<-allow
		}
		return nil
	}, Tools: []agent.ToolDefinition{{Name: "write", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		writes.Add(1)
		return agent.ToolResult{}, nil
	}}}})
	if e != nil {
		t.Fatal(e)
	}
	options := backgroundOptions(backend)
	options.PollInterval = time.Minute
	a, e := OpenMainBackground(t.Context(), filepath.Join(t.TempDir(), "permission-race.jsonl"), 1, bgMain, work, options)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { release(); a.Close() }()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start"})
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("host permission did not block")
	}
	unknown.Store(true)
	if e = a.RefreshBackground(t.Context(), a.BackgroundOperations()[0].ID); e != nil {
		t.Fatal(e)
	}
	release()
	waitIdle(t, a)
	if writes.Load() != 0 {
		t.Fatal("host permission return bypassed new review barrier")
	}
}
