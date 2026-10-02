package codemodetool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/codemode"
)

func TestDynamicSetupFailuresHonorDiagnosticBudget(t *testing.T) {
	for _, test := range []struct {
		name, mode, source string
		bytes              int
		options            Options
	}{
		{"resolver_header", "resolver", "// @options: {\"max_output_tokens\":1}\ntext(1);", 0, Options{}},
		{"catalog_header", "catalog", "// @options: {\"max_output_tokens\":1}\ntext(1);", 0, Options{}},
		{"permission_header", "permission", "// @options: {\"max_output_tokens\":1}\ntext(1);", 0, Options{}},
		{"panic_header", "panic", "// @options: {\"max_output_tokens\":1}\ntext(1);", 0, Options{}},
		{"canceled_header", "canceled", "// @options: {\"max_output_tokens\":1}\ntext(1);", 0, Options{}},
		{"configured_bytes", "resolver", "text(1);", 2, Options{}},
		{"configured_tokens", "resolver", "text(1);", 0, Options{MaxOutputTokens: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var sandbox *codemode.Sandbox
			if test.bytes > 0 {
				config := codemode.DefaultConfig()
				config.MaxOutputBytes = test.bytes
				var err error
				sandbox, err = codemode.NewSandbox(t.Context(), config)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					if err := sandbox.Close(ctx); err != nil {
						t.Error(err)
					}
				})
			} else {
				sandbox = newSandbox(t)
			}
			cause := errors.New("PRIVATE dynamic directory cause")
			if test.mode == "canceled" {
				cause = context.Canceled
			}
			entered, resolved, permissions := 0, 0, 0
			tool, err := NewDynamic(sandbox, func(context.Context) ([]Binding, []codemode.Namespace, error) {
				resolved++
				switch test.mode {
				case "resolver", "canceled":
					return nil, nil, cause
				case "panic":
					panic(cause)
				}
				leaf := agent.ToolDefinition{Name: "read", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
					entered++
					return agent.ToolResult{}, nil
				}}
				if test.mode == "catalog" {
					leaf.Name = ""
				}
				return []Binding{Native(leaf, "local")}, nil, nil
			}, test.options)
			if err != nil {
				t.Fatal(err)
			}
			out := agent.RunToolCall(t.Context(), agent.ToolCall{Name: tool.Name, ParsedArgs: map[string]any{"code": test.source}}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}, CheckToolPermission: func(context.Context, agent.BeforeToolCallContext) error {
				permissions++
				if test.mode == "permission" && permissions == 2 {
					return &agent.ToolExecutionError{Code: agent.ToolFailurePolicyDenied, Message: "审批被拒绝", Err: cause}
				}
				return nil
			}})
			if resolved != 1 || entered != 0 || out.Err == nil || out.Execution.Local != agent.ToolLocalNotStarted || out.Execution.Remote != agent.ToolRemoteNotDispatched {
				t.Fatalf("setup rejection executed script/lost facts: resolved=%d entered=%d outcome=%+v", resolved, entered, out)
			}
			if test.mode != "catalog" && !errors.Is(out.Err, cause) {
				t.Fatalf("budget limit lost original setup cause: %v", out.Err)
			}
			assertBudget := func(parts []agent.Part) {
				t.Helper()
				bytes := 0
				for _, part := range parts {
					bytes += len(part.Text)
					if part.Type != agent.PartTypeText || !utf8.ValidString(part.Text) || strings.Contains(part.Text, "PRIVATE") {
						t.Fatalf("invalid/private setup presentation: %+v", part)
					}
				}
				if bytes > 1 {
					t.Fatalf("setup diagnostic exceeded reserved source/configuration budget: bytes=%d parts=%+v", bytes, parts)
				}
			}
			assertBudget(out.Result.Content)
			if test.mode == "resolver" || test.mode == "catalog" {
				visible := modelVisibleCodemodeCall(t, tool, test.source)
				if !visible.IsError {
					t.Fatal("setup rejection was hidden from the next model request")
				}
				assertBudget(visible.Content)
			}
		})
	}
}

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
