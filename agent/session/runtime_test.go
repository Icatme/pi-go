package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/codemode"
)

var testScope = RuntimeScope{Identity: "test-account", AuthEpoch: 1}

func runtimeOptions() RuntimeOptions {
	return RuntimeOptions{Scope: func() RuntimeScope { return testScope }}
}

type runtimeStream struct {
	message agent.Message
	events  chan agent.AssistantEvent
}

func (s *runtimeStream) Events() <-chan agent.AssistantEvent { return s.events }
func (s *runtimeStream) Wait() (agent.Message, error)        { return s.message, nil }
func (s *runtimeStream) Close() error                        { return nil }
func runtimeReply(message agent.Message) agent.AssistantStream {
	events := make(chan agent.AssistantEvent, 1)
	events <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: message}
	close(events)
	return &runtimeStream{message, events}
}

func runtimeSandbox(t *testing.T) *codemode.Sandbox {
	t.Helper()
	s, err := codemode.NewSandbox(t.Context(), codemode.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s
}

func runtimeTool(t *testing.T, sandbox *codemode.Sandbox, leaf func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error)) agent.ToolDefinition {
	t.Helper()
	var bindings []codemodetool.Binding
	if leaf != nil {
		bindings = []codemodetool.Binding{codemodetool.Native(agent.ToolDefinition{Name: "write", Parameters: map[string]any{"type": "object"}, Execute: leaf}, "native")}
	}
	tool, err := codemodetool.New(sandbox, bindings, codemodetool.Options{StateNamespace: "fixture-v1"})
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func runtimeExecute(t *testing.T, ctx context.Context, b *RunBinding, tool agent.ToolDefinition, code string, after agent.AfterToolCallHook) (agent.AgentSnapshot, int, error) {
	t.Helper()
	requests := 0
	runner, err := agent.NewRunner(agent.AgentDefinition{Tools: []agent.ToolDefinition{tool}, AfterToolCall: after, MaxTurns: 3, Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		requests++
		message := agent.NewTextMessage(agent.RoleAssistant, "done")
		message.StopReason = agent.StopReasonStop
		if requests == 1 {
			raw, _ := json.Marshal(map[string]string{"code": code})
			message.Parts = nil
			message.StopReason = agent.StopReasonToolUse
			message.ToolCalls = []agent.ToolCall{{ID: "outer", Name: "code", Arguments: raw}}
		}
		return runtimeReply(message), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := b.Run(ctx, runner, []agent.Message{agent.NewUserTextMessage("execute")})
	if err != nil {
		return agent.AgentSnapshot{}, requests, err
	}
	for range stream.Events() {
	}
	snapshot, err := stream.Wait()
	return snapshot, requests, err
}

func restoredValue(t *testing.T, s *Session, lane string) string {
	t.Helper()
	b, err := s.PrepareRun(lane, runtimeOptions())
	if err != nil {
		t.Fatal(err)
	}
	value, err := b.LoadState("code", "fixture-v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(value) == 0 {
		return ""
	}
	var saved struct {
		Values map[string]string `json:"values"`
	}
	if err := json.Unmarshal(value, &saved); err != nil {
		t.Fatal(err)
	}
	values := make(map[string]json.RawMessage, len(saved.Values))
	for key, raw := range saved.Values {
		values[key] = json.RawMessage(raw)
	}
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRuntimeStoreRestartForkMoveAndCompaction(t *testing.T) {
	sandbox := runtimeSandbox(t)
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	storage, err := CreateJSONLStorage(path, Header{ID: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := New(storage, Options{})
	tool := runtimeTool(t, sandbox, nil)
	for i := 1; i <= 2; i++ {
		b, err := s.PrepareRun(MainLane, runtimeOptions())
		if err != nil {
			t.Fatal(err)
		}
		snapshot, requests, err := runtimeExecute(t, t.Context(), b, tool, `store("n",(load("n")??0)+1);text(load("n"));`, nil)
		if err != nil || requests != 2 {
			t.Fatalf("run %d requests=%d err=%v snapshot=%+v", i, requests, err, snapshot)
		}
		if got := restoredValue(t, s, MainLane); got != fmt.Sprintf(`{"n":%d}`, i) {
			t.Fatalf("state=%s", got)
		}
		if i == 1 {
			branch, _ := s.ReadBranch(MainLane)
			if err := s.CreateLane("fork", branch.Version.Head); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			storage, err = OpenJSONLStorage(path)
			if err != nil {
				t.Fatal(err)
			}
			s, _ = New(storage, Options{})
		}
	}
	defer s.Close()
	if got := restoredValue(t, s, "fork"); got != `{"n":1}` {
		t.Fatal(got)
	}
	b, err := s.PrepareRun("fork", runtimeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtimeExecute(t, t.Context(), b, tool, `store("n",10)`, nil); err != nil {
		t.Fatal(err)
	}
	if restoredValue(t, s, MainLane) != `{"n":2}` || restoredValue(t, s, "fork") != `{"n":10}` {
		t.Fatal("fork state leaked")
	}
	branch, _ := s.ReadBranch(MainLane)
	plan, err := PrepareCompaction(branch.Entries, CompactionOptions{})
	if err != nil || plan == nil {
		t.Fatalf("plan=%v %v", plan, err)
	}
	data, err := Compact(t.Context(), *plan, func(context.Context, SummaryRequest) (SummaryResult, error) {
		return SummaryResult{Summary: "first run complete"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CompareAppend(branch.Version, []NewEntry{{Type: EntryTypeCompaction, Compaction: &data}}); err != nil {
		t.Fatal(err)
	}
	if got := restoredValue(t, s, MainLane); got != `{"n":2}` {
		t.Fatal(got)
	}
	b, err = s.PrepareRun(MainLane, runtimeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtimeExecute(t, t.Context(), b, tool, `if(load("n")!==2) throw Error("lost");store("n",3)`, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveLane(MainLane, branch.Version.Head); err != nil {
		t.Fatal(err)
	}
	if got := restoredValue(t, s, MainLane); got != `{"n":2}` {
		t.Fatal(got)
	}
}

func TestRuntimeFinalizationRejectsStateAndRetainsChildFacts(t *testing.T) {
	sandbox := runtimeSandbox(t)
	for _, mode := range []string{"script", "hook", "validator", "cancel", "success"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "finalization.jsonl")
			storage, err := CreateJSONLStorage(path, Header{ID: mode})
			if err != nil {
				t.Fatal(err)
			}
			s, _ := New(storage, Options{})
			defer s.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			writes := 0
			tool := runtimeTool(t, sandbox, func(_ context.Context, execution agent.ToolExecutionContext) (agent.ToolResult, error) {
				if execution.State != nil {
					t.Error("child inherited state capability")
				}
				writes++
				return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "written"}}}, nil
			})
			var after agent.AfterToolCallHook
			if mode == "hook" {
				after = func(_ context.Context, in agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
					if in.ToolCall.Name == "code" {
						return agent.AfterToolCallResult{}, errors.New("blocked final output")
					}
					return agent.AfterToolCallResult{}, nil
				}
			}
			if mode == "validator" {
				tool.ValidateResult = func(agent.ToolResult) error { return errors.New("rejected final output") }
			}
			if mode == "cancel" {
				tool.ValidateResult = func(agent.ToolResult) error { cancel(); return nil }
			}
			code := `await tools.write({});store("n",1)`
			if mode == "script" {
				code += `;throw Error("failed")`
			}
			b, _ := s.PrepareRun(MainLane, runtimeOptions())
			snapshot, _, err := runtimeExecute(t, ctx, b, tool, code, after)
			if mode != "cancel" && err != nil {
				t.Fatal(err)
			}
			if writes != 1 {
				t.Fatalf("writes=%d snapshot=%+v", writes, snapshot)
			}
			var result *agent.ToolResultPayload
			for _, message := range snapshot.Messages {
				if message.ToolResult != nil {
					result = message.ToolResult
				}
			}
			if result == nil || result.ChildCalls == nil || len(result.ChildCalls.Calls) != 1 {
				t.Fatalf("missing execution facts: %+v", result)
			}
			value := restoredValue(t, s, MainLane)
			if mode == "success" {
				if value != `{"n":1}` {
					t.Fatal(value)
				}
			} else if value != "" {
				t.Fatalf("rejected state saved: %s", value)
			}
			if mode != "success" && !result.IsError {
				t.Fatalf("failure hidden: %+v", result)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenJSONLStorage(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			branch, err := reopened.ReadBranch(MainLane)
			if err != nil {
				t.Fatal(err)
			}
			var reports []agent.ChildCallReport
			for _, entry := range branch.Entries {
				if entry.Custom == nil || entry.Custom.CustomType != runtimeRecordType {
					continue
				}
				var saved struct {
					ChildCalls []agent.ChildCallReport `json:"child_calls"`
				}
				if err := json.Unmarshal(entry.Custom.Payload, &saved); err != nil {
					t.Fatal(err)
				}
				reports = append(reports, saved.ChildCalls...)
			}
			if len(reports) != 1 || len(reports[0].Calls) != 1 || !reports[0].Calls[0].Completed || reports[0].Calls[0].ToolName != "write" || reports[0].Calls[0].Execution.Local != agent.ToolLocalReturned {
				t.Fatalf("durable execution facts missing: %+v", reports)
			}
			if len(reports[0].Details) != 0 {
				t.Fatal("optional child output details were persisted")
			}
		})
	}
}

func TestRuntimeFinalCommitFailureStopsLoopAndDoesNotReplay(t *testing.T) {
	sandbox := runtimeSandbox(t)
	path := filepath.Join(t.TempDir(), "failure.jsonl")
	storage, err := CreateJSONLStorage(path, Header{ID: "failure"})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := New(storage, Options{})
	writes := 0
	cause := errors.New("disk sync fixture failure")
	tool := runtimeTool(t, sandbox, func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		writes++
		storage.syncFile = func() error { return cause }
		return agent.ToolResult{}, nil
	})
	b, _ := s.PrepareRun(MainLane, runtimeOptions())
	snapshot, requests, err := runtimeExecute(t, t.Context(), b, tool, `await tools.write({});store("n",1)`, nil)
	if !errors.Is(err, cause) || writes != 1 || requests != 1 {
		t.Fatalf("requests=%d writes=%d err=%v", requests, writes, err)
	}
	if len(snapshot.Messages) == 0 || snapshot.Messages[len(snapshot.Messages)-1].ToolResult == nil {
		t.Fatal("host lost final tool result")
	}
	if _, err := storage.AppendEntry(MainLane, transactionEntry("later")); !errors.Is(err, cause) {
		t.Fatalf("writer not poisoned: %v", err)
	}
	_ = s.Close()
	storage, err = OpenJSONLStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	s, _ = New(storage, Options{})
	if _, err := s.PrepareRun(MainLane, runtimeOptions()); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("interrupted run resumed: %v", err)
	}
	branch, _ := s.ReadBranch(MainLane)
	for _, entry := range branch.Entries {
		if entry.Message != nil && entry.Message.ToolResult != nil {
			t.Fatal("partial final transaction persisted")
		}
	}
}

func TestRuntimeScopeConflictAndCorruptState(t *testing.T) {
	sandbox := runtimeSandbox(t)
	tool := runtimeTool(t, sandbox, nil)
	storage, _ := NewMemoryStorage(Header{ID: "scope"})
	s, _ := New(storage, Options{})
	defer s.Close()
	b, _ := s.PrepareRun(MainLane, runtimeOptions())
	stale, _ := s.PrepareRun(MainLane, runtimeOptions())
	if _, _, err := runtimeExecute(t, t.Context(), b, tool, `store("x",1)`, nil); err != nil {
		t.Fatal(err)
	}
	if _, requests, err := runtimeExecute(t, t.Context(), stale, tool, `store("x",2)`, nil); errorCode(err) != ErrorConflict || requests != 0 {
		t.Fatalf("stale requests=%d err=%v", requests, err)
	}
	for _, scope := range []RuntimeScope{{Identity: "other", AuthEpoch: 1}, {Identity: testScope.Identity, AuthEpoch: 2}} {
		if _, err := s.PrepareRun(MainLane, RuntimeOptions{Scope: func() RuntimeScope { return scope }}); !errors.Is(err, ErrScopeMismatch) {
			t.Fatal(err)
		}
	}
	b, _ = s.PrepareRun(MainLane, runtimeOptions())
	if _, ok := b.TrustedScope(agent.AgentSnapshot{SessionID: s.Header().ID}); ok {
		t.Fatal("session id forged provenance")
	}
	branch, _ := s.ReadBranch(MainLane)
	bad, _ := json.Marshal(runtimeRecord{Version: 1, SessionID: s.Header().ID, Lane: MainLane, Scope: testScope, State: []agent.ToolStateChange{{Tool: "code", Namespace: "fixture-v1", Value: json.RawMessage(`"` + strings.Repeat("x", 1<<20) + `"`)}}})
	if _, _, err := s.CompareAppend(branch.Version, []NewEntry{{Type: EntryTypeCustom, Custom: &CustomData{CustomType: runtimeRecordType, Payload: bad}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareRun(MainLane, runtimeOptions()); errorCode(err) != ErrorCorruptLog {
		t.Fatalf("oversized restoration: %v", err)
	}
}

func TestRuntimeCrashProcessHelper(t *testing.T) {
	path := os.Getenv("PIGO_RUNTIME_CRASH")
	if path == "" {
		return
	}
	storage, err := CreateJSONLStorage(path, Header{ID: "crash"})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := New(storage, Options{})
	b, _ := s.PrepareRun(MainLane, runtimeOptions())
	tool := runtimeTool(t, runtimeSandbox(t), func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		if err := os.WriteFile(path+".effect", []byte("remote write completed"), 0600); err != nil {
			return agent.ToolResult{}, err
		}
		os.Exit(71)
		return agent.ToolResult{}, nil
	})
	runtimeExecute(t, t.Context(), b, tool, `await tools.write({});store("n",1)`, nil)
	t.Fatal("fixture did not exit during side effect")
}

func TestRuntimeProcessDeathPreservesUnknownWithoutReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crash.jsonl")
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRuntimeCrashProcessHelper$")
	cmd.Env = append(os.Environ(), "PIGO_RUNTIME_CRASH="+path)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 71 {
		t.Fatalf("helper: %v %s", err, output)
	}
	if _, err := os.ReadFile(path + ".effect"); err != nil {
		t.Fatal(err)
	}
	storage, err := OpenJSONLStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	s, _ := New(storage, Options{})
	if _, err := s.PrepareRun(MainLane, runtimeOptions()); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("unsettled side effect was resumed: %v", err)
	}
}

func TestRuntimeCanIntroduceSystemContextAfterPlainConversation(t *testing.T) {
	for _, mode := range []string{"prompt", "tool"} {
		t.Run(mode, func(t *testing.T) {
			storage, err := NewMemoryStorage(Header{ID: mode})
			if err != nil {
				t.Fatal(err)
			}
			s, err := New(storage, Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			requests := 0
			definition := agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
				requests++
				message := agent.NewTextMessage(agent.RoleAssistant, "done")
				message.StopReason = agent.StopReasonStop
				return runtimeReply(message), nil
			})}
			for round := range 2 {
				if round == 1 {
					if mode == "prompt" {
						definition.SystemPrompt = "New host instruction"
					} else {
						definition.Tools = []agent.ToolDefinition{{Name: "new_tool", Parameters: map[string]any{"type": "object"}, Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
							return agent.ToolResult{}, nil
						}}}
					}
				}
				runner, err := agent.NewRunner(definition)
				if err != nil {
					t.Fatal(err)
				}
				binding, err := s.PrepareRun(MainLane, runtimeOptions())
				if err != nil {
					t.Fatal(err)
				}
				stream, err := binding.Run(t.Context(), runner, []agent.Message{agent.NewUserTextMessage("hello")})
				if err != nil {
					t.Fatal(err)
				}
				for range stream.Events() {
				}
				if _, err := stream.Wait(); err != nil {
					t.Fatalf("round %d: %v", round, err)
				}
			}
			if requests != 2 {
				t.Fatalf("model requests=%d, want 2", requests)
			}
			restored, err := s.PrepareRun(MainLane, runtimeOptions())
			if err != nil {
				t.Fatal(err)
			}
			if restored.snapshot.Messages[0].Role != agent.RoleUser || agent.GetCurrentSystemMessage(restored.snapshot.Messages) == nil {
				t.Fatal("system context was not appended after the saved conversation")
			}
		})
	}
}

func TestRuntimeStateLimitsUsePersistedEncoding(t *testing.T) {
	for _, tc := range []struct {
		name         string
		chars, slots int
		valid        bool
	}{{"value", 200000, 1, false}, {"total", 150000, 5, false}, {"valid", 150000, 4, true}} {
		t.Run(tc.name, func(t *testing.T) {
			storage, _ := NewMemoryStorage(Header{ID: tc.name})
			s, _ := New(storage, Options{})
			defer s.Close()
			binding, err := s.PrepareRun(MainLane, runtimeOptions())
			if err != nil {
				t.Fatal(err)
			}
			value := json.RawMessage(`"` + strings.Repeat("&", tc.chars) + `"`)
			var changes []agent.ToolStateChange
			for i := range tc.slots {
				changes = append(changes, agent.ToolStateChange{Tool: "tool", Namespace: fmt.Sprint(i), Value: value})
			}
			err = binding.Record(t.Context(), agent.JournalRecord{State: changes})
			if (err == nil) != tc.valid {
				t.Fatalf("commit valid=%v: %v", tc.valid, err)
			}
			if !tc.valid && len(storage.Entries()) != 0 {
				t.Fatal("oversized encoded state became durable")
			}
			restored, err := s.PrepareRun(MainLane, runtimeOptions())
			if err != nil {
				t.Fatalf("committed state cannot reopen: %v", err)
			}
			if tc.valid {
				raw, err := restored.LoadState("tool", "0")
				var decoded string
				if err != nil || json.Unmarshal(raw, &decoded) != nil || decoded != strings.Repeat("&", tc.chars) {
					t.Fatal("saved state changed")
				}
			}
		})
	}
}

type cancelAtWaitStream struct {
	agent.AssistantStream
	cancel context.CancelFunc
}

func (s *cancelAtWaitStream) Wait() (agent.Message, error) {
	message, err := s.AssistantStream.Wait()
	s.cancel()
	return message, err
}

func TestRuntimeCanceledAssistantFinalizesUnexecutedCalls(t *testing.T) {
	for _, point := range []string{"stream", "wait", "gate", "gate-error"} {
		for _, gated := range []bool{false, true} {
			if strings.HasPrefix(point, "gate") && !gated {
				continue
			}
			for _, mode := range []agent.ToolExecutionMode{agent.ToolExecutionSequential, agent.ToolExecutionParallel} {
				t.Run(fmt.Sprintf("%s/gated=%v/%s", point, gated, mode), func(t *testing.T) {
					testRuntimeCanceledAssistant(t, point, gated, mode)
				})
			}
		}
	}
}

func testRuntimeCanceledAssistant(t *testing.T, point string, gated bool, mode agent.ToolExecutionMode) {
	t.Helper()
	storage, _ := NewMemoryStorage(Header{ID: "canceled-assistant"})
	s, _ := New(storage, Options{})
	defer s.Close()
	binding, err := s.PrepareRun(MainLane, runtimeOptions())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	executions := 0
	runner, err := agent.NewRunner(agent.AgentDefinition{
		ToolExecution: mode,
		Tools: []agent.ToolDefinition{{Name: "write", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
			executions++
			return agent.ToolResult{}, nil
		}}},
		Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
			message := agent.Message{Role: agent.RoleAssistant, StopReason: agent.StopReasonAborted, ErrorMessage: context.Canceled.Error(), ToolCalls: []agent.ToolCall{{ID: "not-executed", Name: "write", Arguments: json.RawMessage(`{}`)}, {ID: "not-executed-2", Name: "write", Arguments: json.RawMessage(`{}`)}}}
			if point != "stream" {
				message.StopReason, message.ErrorMessage = agent.StopReasonToolUse, ""
				if point == "wait" {
					return &cancelAtWaitStream{AssistantStream: runtimeReply(message), cancel: cancel}, nil
				}
				return runtimeReply(message), nil
			}
			cancel()
			return runtimeReply(message), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var stream *agent.RunStream
	if gated {
		gateCalls := 0
		stream = runner.RunWithHooks(ctx, binding.snapshot, []agent.Message{agent.NewUserTextMessage("write")}, agent.LoopHooks{Journal: binding, ToolGate: func(context.Context, agent.BeforeToolCallContext) (agent.ToolGateResult, error) {
			gateCalls++
			if strings.HasPrefix(point, "gate") {
				if gateCalls == 1 {
					return agent.ToolGateResult{Action: agent.ToolGateActionSuspend}, nil
				}
				cancel()
				if point == "gate-error" {
					return agent.ToolGateResult{}, fmt.Errorf("approval wait stopped: %w", ctx.Err())
				}
			}
			return agent.ToolGateResult{Action: agent.ToolGateActionAllow}, nil
		}})
	} else {
		stream, err = binding.Run(ctx, runner, []agent.Message{agent.NewUserTextMessage("write")})
	}
	if err != nil {
		t.Fatal(err)
	}
	turnEnded := false
	for event := range stream.Events() {
		if event.Type == agent.EventTurnEnd {
			turnEnded = true
		}
	}
	snapshot, _ := stream.Wait()
	if executions != 0 || !turnEnded {
		t.Fatalf("executions=%d turnEnded=%v", executions, turnEnded)
	}
	if _, err := completeTurnStarts(snapshot.Messages); err != nil {
		t.Fatal(err)
	}
	restored, err := s.PrepareRun(MainLane, runtimeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.snapshot.Messages) != len(snapshot.Messages) {
		t.Fatal("aborted turn was not persisted")
	}
	tail := restored.snapshot.Messages[len(restored.snapshot.Messages)-1]
	if tail.ToolResult == nil || !tail.ToolResult.IsError || tail.ToolResult.ToolCallID != "not-executed-2" {
		t.Fatal("missing unexecuted tool result")
	}
}

func TestRuntimeRejectsIndividuallyInvalidStateKeys(t *testing.T) {
	for _, tool := range []string{"\xc2", strings.Repeat("a", 127) + "\xc2"} {
		storage, _ := NewMemoryStorage(Header{ID: "invalid-state-key"})
		s, _ := New(storage, Options{})
		defer s.Close()
		binding, err := s.PrepareRun(MainLane, runtimeOptions())
		if err != nil {
			t.Fatal(err)
		}
		err = binding.Record(t.Context(), agent.JournalRecord{State: []agent.ToolStateChange{{Tool: tool, Namespace: "\xa0", Value: json.RawMessage(`{}`)}}})
		if err == nil || len(storage.Entries()) != 0 {
			t.Fatal("invalid state key strings were committed")
		}
		if _, err := s.PrepareRun(MainLane, runtimeOptions()); err != nil {
			t.Fatal(err)
		}
	}
}
