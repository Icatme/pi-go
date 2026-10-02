package codemodetool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/codemode"
)

func TestDynamicCatalogRebindPreservesInvocationStore(t *testing.T) {
	resolutions := 0
	tool, err := NewDynamic(newSandbox(t), func(context.Context) ([]Binding, []codemode.Namespace, error) {
		resolutions++
		value := resolutions
		leaf := agent.ToolDefinition{Name: "version", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
			return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: fmt.Sprint(value)}}}, nil
		}}
		return []Binding{Native(leaf, "local")}, nil, nil
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	runner, err := agent.NewRunner(agent.AgentDefinition{Tools: []agent.ToolDefinition{tool}, Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		requests++
		m := agent.NewTextMessage(agent.RoleAssistant, "done")
		m.StopReason = agent.StopReasonStop
		if requests <= 2 {
			code := `store("remembered", "first"); text(await tools.version({}));`
			if requests == 2 {
				code = `text(load("remembered")); text(await tools.version({}));`
			}
			m.Parts = nil
			m.StopReason = agent.StopReasonToolUse
			m.ToolCalls = []agent.ToolCall{{ID: fmt.Sprint(requests), Name: tool.Name, ParsedArgs: map[string]any{"code": code}}}
		}
		return immediate(m), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	stream := runner.Run(ctx, agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("run")})
	for range stream.Events() {
	}
	snapshot, err := stream.Wait()
	if err != nil {
		t.Fatal(err)
	}
	var results []agent.ToolResultPayload
	for _, m := range snapshot.Messages {
		if m.ToolResult != nil {
			results = append(results, *m.ToolResult)
		}
	}
	if resolutions != 2 || len(results) != 2 || results[1].IsError {
		t.Fatalf("resolutions=%d results=%+v", resolutions, results)
	}
	var text strings.Builder
	for _, p := range results[1].Content {
		text.WriteString(p.Text)
	}
	if !strings.Contains(text.String(), "first") || !strings.Contains(text.String(), "2") {
		t.Fatal("dynamic rebind reset store or reused executor", text.String())
	}
}

func TestDynamicSourceDeadlineIncludesCatalogSetup(t *testing.T) {
	called := 0
	tool, err := NewDynamic(newSandbox(t), func(ctx context.Context) ([]Binding, []codemode.Namespace, error) {
		called++
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	out := agent.RunToolCall(t.Context(), agent.ToolCall{Name: tool.Name, ParsedArgs: map[string]any{"code": "// @options: {\"timeout_ms\":50}\ntext(1);"}}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
	if called != 1 || !errors.Is(out.Err, context.DeadlineExceeded) || out.Failure.Code != agent.ToolFailureDeadline || out.Execution.Local != agent.ToolLocalNotStarted || time.Since(start) > 2*time.Second {
		t.Fatal(out, called, time.Since(start))
	}
}

func TestDynamicSourceDeadlineIncludesPermissionRecheck(t *testing.T) {
	var directoryDeadline, permissionDeadline time.Time
	var permissions, calls int
	tool, err := NewDynamic(newSandbox(t), func(ctx context.Context) ([]Binding, []codemode.Namespace, error) {
		directoryDeadline, _ = ctx.Deadline()
		leaf := agent.ToolDefinition{Name: "write", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
			calls++
			return agent.ToolResult{}, nil
		}}
		return []Binding{Native(leaf, "local")}, nil, nil
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	started := time.Now()
	out := agent.RunToolCall(ctx, agent.ToolCall{Name: tool.Name, ParsedArgs: map[string]any{
		"code": "// @options: {\"timeout_ms\":250}\nawait tools.write({});",
	}}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}, CheckToolPermission: func(ctx context.Context, _ agent.BeforeToolCallContext) error {
		permissions++
		if permissions == 2 {
			permissionDeadline, _ = ctx.Deadline()
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}})
	if permissions != 2 || calls != 0 || directoryDeadline.IsZero() || !permissionDeadline.Equal(directoryDeadline) || !errors.Is(out.Err, context.DeadlineExceeded) || out.Failure.Code != agent.ToolFailureDeadline || out.Execution.Local != agent.ToolLocalNotStarted || out.Execution.Remote != agent.ToolRemoteNotDispatched || time.Since(started) > time.Second {
		t.Fatalf("permission recheck escaped script deadline: directory=%v permission=%v elapsed=%v permissions=%d calls=%d outcome=%+v", directoryDeadline, permissionDeadline, time.Since(started), permissions, calls, out)
	}
}

func TestDynamicInvalidSourceLimitsDoNotLoadCatalog(t *testing.T) {
	called := false
	tool, err := NewDynamic(newSandbox(t), func(context.Context) ([]Binding, []codemode.Namespace, error) { called = true; return nil, nil, nil }, Options{})
	if err != nil {
		t.Fatal(err)
	}
	out := agent.RunToolCall(t.Context(), agent.ToolCall{Name: tool.Name, ParsedArgs: map[string]any{"code": "// @options: {\"timeout_ms\":-1}\ntext(1);"}}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
	if called || out.Err == nil || out.Failure.Code != agent.ToolFailureArgumentInvalid || out.Execution.Remote != agent.ToolRemoteNotDispatched {
		t.Fatal(out, called)
	}
}
