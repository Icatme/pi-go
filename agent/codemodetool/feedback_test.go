package codemodetool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/codemode"
)

func modelVisibleCodemodeCall(t *testing.T, tool agent.ToolDefinition, code string) agent.ToolResultPayload {
	t.Helper()
	requests := 0
	var visible *agent.ToolResultPayload
	model := agent.StreamFunc(func(_ context.Context, request agent.ModelRequest) (agent.AssistantStream, error) {
		requests++
		message := agent.NewTextMessage(agent.RoleAssistant, "done")
		message.StopReason = agent.StopReasonStop
		if requests == 1 {
			arguments, err := json.Marshal(map[string]any{"code": code})
			if err != nil {
				t.Fatal(err)
			}
			message.Parts = nil
			message.StopReason = agent.StopReasonToolUse
			message.ToolCalls = []agent.ToolCall{{ID: "outer", Name: tool.Name, Arguments: arguments}}
		} else {
			for _, candidate := range request.Messages {
				if candidate.Role == agent.RoleTool && candidate.ToolResult != nil {
					copy := *candidate.ToolResult
					visible = &copy
				}
			}
		}
		return immediate(message), nil
	})
	runner, err := agent.NewRunner(agent.AgentDefinition{Model: model, Tools: []agent.ToolDefinition{tool}})
	if err != nil {
		t.Fatal(err)
	}
	stream := runner.Query(t.Context(), "run the script")
	for range stream.Events() {
	}
	if _, err := stream.Wait(); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || visible == nil {
		t.Fatalf("model did not receive the bounded code result: requests=%d result=%+v", requests, visible)
	}
	return *visible
}

func TestModelReceivesActionableScriptFailuresAndPartialOutput(t *testing.T) {
	calls := 0
	leaf := agent.ToolDefinition{Name: "read", Parameters: map[string]any{"type": "object"}, Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		calls++
		return agent.ToolResult{}, nil
	}}
	tool, err := New(newSandbox(t), []Binding{Native(leaf, "local")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, code string
		want       []string
	}{
		{"name", `text("partial"); await tools.reda({});`, []string{"partial", "failed", "Did you mean tools.read?", "codemode.js:1"}},
		{"syntax", "const a=1;\nconst b=2;\nconst =;", []string{"failed", "SyntaxError", "codemode.js:3"}},
		{"throw", `text("partial"); throw Error("provide the required ID");`, []string{"partial", "provide the required ID", "codemode.js:1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := modelVisibleCodemodeCall(t, tool, test.code)
			text := contentText(result.Content)
			if !result.IsError || calls != 0 {
				t.Fatalf("failed script called a leaf: calls=%d result=%+v", calls, result)
			}
			for _, want := range test.want {
				if !strings.Contains(text, want) {
					t.Fatalf("model result lacks %q: %s", want, text)
				}
			}
		})
	}
}

func TestModelDiagnosticReservesBytesAndItemWithinEffectiveLimits(t *testing.T) {
	config := codemode.DefaultConfig()
	config.MaxOutputBytes = 2048
	config.MaxOutputItems = 2
	sandbox, err := codemode.NewSandbox(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sandbox.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	tool, err := New(sandbox, nil, Options{MaxOutputTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, code   string
		partial, cap int
	}{
		{"config", `text("x".repeat(1536)); throw Error("provide the required ID " + "😀".repeat(10000));`, 1536, 2048},
		{"header", "// @options: {\"max_output_tokens\":200}\ntext('x'.repeat(600)); throw Error('provide the required ID');", 600, 800},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := modelVisibleCodemodeCall(t, tool, test.code)
			if !result.IsError || len(result.Content) != 2 || len(result.Content[0].Text) != test.partial || !strings.Contains(result.Content[1].Text, "provide the required ID") {
				t.Fatalf("full output evicted diagnostic or partial content: %+v", result)
			}
			bytes := 0
			for _, part := range result.Content {
				bytes += len(part.Text)
				if !utf8.ValidString(part.Text) {
					t.Fatalf("diagnostic truncated inside a Unicode character: %q", part.Text)
				}
			}
			if bytes > test.cap {
				t.Fatalf("model output enlarged cap: bytes=%d cap=%d", bytes, test.cap)
			}
		})
	}
}

func TestDefaultGuidanceAndOnDemandNamespaceMetadata(t *testing.T) {
	leaf := agent.ToolDefinition{Name: "records", Description: "Read records", Parameters: map[string]any{"type": "object", "description": "PRIVATE_LEAF_SCHEMA"}, Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		return agent.ToolResult{}, nil
	}}
	metadata := codemode.Namespace{Name: "allowed", Description: "PRIVATE_NAMESPACE_DESCRIPTION", Instructions: "PRIVATE_NAMESPACE_INSTRUCTIONS"}
	tool, err := New(newSandbox(t), []Binding{MCP(leaf, "allowed")}, Options{Namespaces: []codemode.Namespace{metadata}})
	if err != nil {
		t.Fatal(err)
	}
	declaration, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(declaration), "PRIVATE_") || len(tool.Description) > 1024 {
		t.Fatalf("on-demand declarations were preloaded: %s", declaration)
	}
	for _, want := range []string{"Promise.all", "namespace?", "describeNamespace", `"name" in tools`, "store/load"} {
		if !strings.Contains(tool.Description, want) {
			t.Fatalf("compact guidance lacks %q: %s", want, tool.Description)
		}
	}
	result := modelVisibleCodemodeCall(t, tool, `text((await describeNamespace("allowed")).instructions); text((await describeTool("mcp__allowed__records")).resultDescription);`)
	text := contentText(result.Content)
	if result.IsError || !strings.Contains(text, metadata.Instructions) || !strings.Contains(text, "structuredContent") || !strings.Contains(text, "isError:true") {
		t.Fatalf("on-demand resolution conventions missing: %s", text)
	}
	_, err = New(newSandbox(t), []Binding{MCP(leaf, "allowed")}, Options{Namespaces: []codemode.Namespace{{Name: "hidden", Instructions: "SECRET_HIDDEN_NAMESPACE"}}})
	if err == nil || strings.Contains(err.Error(), "SECRET_HIDDEN_NAMESPACE") {
		t.Fatalf("unavailable namespace metadata accepted or echoed: %v", err)
	}
}

func TestFailureAfterWriteDoesNotReplayAndRetainsCause(t *testing.T) {
	secret := errors.New("SECRET_ORIGINAL_GO_ERROR")
	calls := 0
	leaf := agent.ToolDefinition{Name: "write", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		calls++
		return agent.ToolResult{Execution: &agent.ToolExecutionInfo{Remote: agent.ToolRemoteCompleteReported}}, &agent.ToolExecutionError{Code: agent.ToolFailureTransport, Message: "public tool response was unavailable", Err: secret}
	}}
	tool, err := New(newSandbox(t), []Binding{Native(leaf, "local")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	outcome := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "outer", Name: tool.Name, ParsedArgs: map[string]any{"code": `await tools.write({})`}}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
	text := contentText(outcome.Result.Content)
	if calls != 1 || !outcome.IsError || !errors.Is(outcome.Err, secret) || !strings.Contains(text, "public tool response was unavailable") || !strings.Contains(text, "Earlier calls may have effects") || strings.Contains(text, "SECRET_") {
		t.Fatalf("diagnostic changed effects, public presentation or cause: calls=%d result=%+v", calls, outcome)
	}
	if outcome.Result.ChildCalls == nil || len(outcome.Result.ChildCalls.Calls) != 1 || outcome.Result.ChildCalls.Calls[0].Execution.Remote != agent.ToolRemoteCompleteReported {
		t.Fatalf("failure diagnostic lost completed write ledger: %+v", outcome.Result.ChildCalls)
	}
}

func TestRawLeafGoErrorDoesNotBecomeModelDiagnosticOrEventText(t *testing.T) {
	secret := errors.New("SECRET_CREDENTIAL_IN_RAW_GO_ERROR")
	leaf := agent.ToolDefinition{Name: "bad", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		return agent.ToolResult{}, secret
	}}
	tool, err := New(newSandbox(t), []Binding{Native(leaf, "local")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, events := runnerCall(t, tool, `await tools.bad({})`, agent.AgentDefinition{}, nil)
	if !result.IsError || strings.Contains(contentText(result.Content), "SECRET_") || !strings.Contains(contentText(result.Content), "tool call failed") {
		t.Fatalf("raw Go cause became a public diagnostic: %+v", result)
	}
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil || strings.Contains(string(data), "SECRET_") {
			t.Fatalf("raw Go cause leaked into model/public event JSON: %s err=%v", data, err)
		}
	}
	if result.ChildCalls == nil || len(result.ChildCalls.Calls) != 1 || !errors.Is(result.ChildCalls.Calls[0].Err, secret) {
		t.Fatalf("raw cause was dropped from trusted host ledger: %+v", result.ChildCalls)
	}
}

func TestNamespaceAliasPrefixRetainsDeterministicServerIdentity(t *testing.T) {
	names := []string{"server", "notes-workspace", "notes_workspace", "notes__workspace", strings.Repeat("long", 20) + "a", strings.Repeat("long", 20) + "b"}
	prefixes := make(map[string]bool)
	for _, namespace := range names {
		binding := MCP(agent.ToolDefinition{Name: strings.Repeat("tool", 25)}, namespace)
		prefix := binding.NamespacePrefix()
		alias := binding.ExportedName()
		if prefixes[prefix] || len(alias) > 64 || !identifier(alias) || !strings.HasPrefix(alias, prefix) || binding.NamespacePrefix() != prefix || binding.ExportedName() != alias {
			t.Fatalf("namespace aliases collided or lost stability: namespace=%s prefix=%s alias=%s", namespace, prefix, alias)
		}
		prefixes[prefix] = true
	}
	ordinary := MCP(agent.ToolDefinition{Name: "read"}, "server")
	if ordinary.ExportedName() != "mcp__server__read" || ordinary.NamespacePrefix() != "mcp__server__" {
		t.Fatalf("ordinary alias changed: %+v", ordinary)
	}
}
