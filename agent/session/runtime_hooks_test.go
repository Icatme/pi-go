package session

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func hookRunner(t *testing.T, call func(agent.ModelRequest) (agent.AssistantStream, error)) *agent.Runner {
	t.Helper()
	r, err := agent.NewRunner(agent.AgentDefinition{MaxTurns: 8, Model: agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) { return call(req) })})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func hookAnswer() agent.AssistantStream {
	m := agent.NewTextMessage(agent.RoleAssistant, "answer")
	m.StopReason = agent.StopReasonStop
	return runtimeReply(m)
}
func hookText(messages []agent.Message) []string {
	var out []string
	for _, m := range messages {
		if m.Role == agent.RoleUser {
			for _, p := range m.Parts {
				if p.Type == agent.PartTypeText {
					out = append(out, p.Text)
				}
			}
		}
	}
	return out
}
func hookSession(t *testing.T) *Session {
	t.Helper()
	storage, err := NewMemoryStorage(Header{ID: "hooks"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(storage, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func drainHooks(t *testing.T, stream *agent.RunStream) (agent.AgentSnapshot, error) {
	t.Helper()
	for range stream.Events() {
	}
	return stream.Wait()
}

func TestRuntimeHooksSteerFollowUpAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.jsonl")
	storage, err := CreateJSONLStorage(path, Header{ID: "hooks"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(storage, Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.PrepareRun(MainLane, runtimeOptions())
	if err != nil {
		t.Fatal(err)
	}
	calls, steerCalls, followCalls := 0, 0, 0
	runner := hookRunner(t, func(req agent.ModelRequest) (agent.AssistantStream, error) {
		calls++
		want := []string{"first", "steer"}
		if calls >= 2 {
			want = append(want, "mid-run")
		}
		if calls == 3 {
			want = append(want, "follow")
		}
		if calls <= 3 && !reflect.DeepEqual(hookText(req.Messages), want) {
			return nil, errors.New("model did not receive admitted inputs in order")
		}
		return hookAnswer(), nil
	})
	stream, err := b.RunWithHooks(t.Context(), runner, []agent.Message{agent.NewUserTextMessage("first")}, agent.LoopHooks{
		GetSteeringMessages: func(context.Context) ([]agent.Message, error) {
			steerCalls++
			if steerCalls == 1 {
				return []agent.Message{agent.NewUserTextMessage("steer")}, nil
			}
			if steerCalls == 2 {
				return []agent.Message{agent.NewUserTextMessage("mid-run")}, nil
			}
			return nil, nil
		},
		GetFollowUpMessages: func(context.Context) ([]agent.Message, error) {
			followCalls++
			if followCalls == 1 {
				return []agent.Message{agent.NewUserTextMessage("follow")}, nil
			}
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := drainHooks(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"first", "steer", "mid-run", "follow"}
	if calls != 3 || !reflect.DeepEqual(hookText(snapshot.Messages), want) {
		t.Fatalf("calls=%d messages=%v", calls, hookText(snapshot.Messages))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	storage, err = OpenJSONLStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err = New(storage, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	restored, err := s.PrepareRun(MainLane, runtimeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(hookText(restored.snapshot.Messages), want) {
		t.Fatalf("restored=%v", hookText(restored.snapshot.Messages))
	}
	stream, err = restored.Run(t.Context(), runner, []agent.Message{agent.NewUserTextMessage("next")})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = drainHooks(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(hookText(snapshot.Messages), append(want, "next")) {
		t.Fatal(hookText(snapshot.Messages))
	}
}

func TestRuntimeHooksRejectJournalWithoutConsumingBinding(t *testing.T) {
	s := hookSession(t)
	b, err := s.PrepareRun(MainLane, runtimeOptions())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	runner := hookRunner(t, func(agent.ModelRequest) (agent.AssistantStream, error) { calls++; return hookAnswer(), nil })
	if _, err = b.RunWithHooks(t.Context(), runner, nil, agent.LoopHooks{Journal: b}); err == nil {
		t.Fatal("accepted replacement journal")
	}
	stream, err := b.Run(t.Context(), runner, []agent.Message{agent.NewUserTextMessage("valid")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = drainHooks(t, stream); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	if _, err = b.RunWithHooks(t.Context(), runner, nil, agent.LoopHooks{}); err == nil {
		t.Fatal("reused binding")
	}
}

func TestRuntimeHooksFailureStopsBeforeModel(t *testing.T) {
	for _, mode := range []string{"hook-error", "scope-change", "conflict"} {
		t.Run(mode, func(t *testing.T) {
			s := hookSession(t)
			scope := testScope
			b, err := s.PrepareRun(MainLane, RuntimeOptions{Scope: func() RuntimeScope { return scope }})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			runner := hookRunner(t, func(agent.ModelRequest) (agent.AssistantStream, error) { calls++; return hookAnswer(), nil })
			failure := errors.New("queue unavailable")
			stream, err := b.RunWithHooks(t.Context(), runner, []agent.Message{agent.NewUserTextMessage("first")}, agent.LoopHooks{GetSteeringMessages: func(context.Context) ([]agent.Message, error) {
				switch mode {
				case "hook-error":
					return nil, failure
				case "scope-change":
					scope.AuthEpoch++
				case "conflict":
					if err := s.MoveLane(MainLane, ""); err != nil {
						return nil, err
					}
				}
				return []agent.Message{agent.NewUserTextMessage("steer")}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = drainHooks(t, stream)
			if err == nil || calls != 0 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			if mode == "hook-error" && !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if mode == "scope-change" && !errors.Is(err, ErrScopeMismatch) {
				t.Fatal(err)
			}
			if mode == "conflict" && errorCode(err) != ErrorConflict {
				t.Fatal(err)
			}
			branch, err := s.ReadBranch(MainLane)
			if err != nil {
				t.Fatal(err)
			}
			if len(branch.Entries) != 0 {
				t.Fatalf("unexpected commit: %v", branch.Entries)
			}
		})
	}
}

func TestRuntimeHooksFollowUpErrorPreservesCommittedPrefix(t *testing.T) {
	s := hookSession(t)
	b, err := s.PrepareRun(MainLane, runtimeOptions())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	runner := hookRunner(t, func(agent.ModelRequest) (agent.AssistantStream, error) { calls++; return hookAnswer(), nil })
	failure := errors.New("follow-up queue unavailable")
	stream, err := b.RunWithHooks(t.Context(), runner, []agent.Message{agent.NewUserTextMessage("first")}, agent.LoopHooks{GetFollowUpMessages: func(context.Context) ([]agent.Message, error) { return nil, failure }})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := drainHooks(t, stream)
	if !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	restored, err := s.PrepareRun(MainLane, runtimeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.snapshot.Messages, snapshot.Messages) {
		t.Fatal("committed prefix changed")
	}
}

func TestRuntimeHooksSuspensionRemainsInterrupted(t *testing.T) {
	s := hookSession(t)
	b, err := s.PrepareRun(MainLane, runtimeOptions())
	if err != nil {
		t.Fatal(err)
	}
	executions := 0
	runner, err := agent.NewRunner(agent.AgentDefinition{Tools: []agent.ToolDefinition{{Name: "write", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		executions++
		return agent.ToolResult{}, nil
	}}}, Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		return runtimeReply(agent.Message{Role: agent.RoleAssistant, StopReason: agent.StopReasonToolUse, ToolCalls: []agent.ToolCall{{ID: "write-1", Name: "write", Arguments: json.RawMessage(`{}`)}}}), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := b.RunWithHooks(t.Context(), runner, []agent.Message{agent.NewUserTextMessage("write")}, agent.LoopHooks{ToolGate: func(context.Context, agent.BeforeToolCallContext) (agent.ToolGateResult, error) {
		return agent.ToolGateResult{Action: agent.ToolGateActionSuspend}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := drainHooks(t, stream)
	if err == nil || len(snapshot.PendingToolCalls) != 1 || executions != 0 {
		t.Fatalf("err=%v pending=%d executions=%d", err, len(snapshot.PendingToolCalls), executions)
	}
	if _, err = s.PrepareRun(MainLane, runtimeOptions()); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("resumed unfinished intent: %v", err)
	}
}
