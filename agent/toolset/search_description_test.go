package toolset

import (
	"context"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
	managed "github.com/Icatme/pi-go/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestActiveSearchMatchesFullDescriptionBeforeBoundingOutput(t *testing.T) {
	f := newFixture(t, managed.Codemode, mixedRules())
	f.server.AddTool(&sdk.Tool{Name: "direct_issue", Description: strings.Repeat("intro ", 40) + "zebraprint", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			f.writes.Add(1)
			return &sdk.CallToolResult{}, nil
		})
	ts, err := New(f.manager, f.sandbox, Options{})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := ts.Resolve(t.Context(), agent.AgentSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "search", Name: "tool_search", ParsedArgs: map[string]any{"query": "zebraprint"}}, agent.RunToolCallOptions{Tools: tools, Context: agent.AgentContext{Tools: tools}})
	text := resultText(out.Result)
	if out.Err != nil || !strings.Contains(text, "already declared") || !strings.Contains(text, "direct_issue") {
		t.Fatalf("description suffix did not match: %q, %v", text, out.Err)
	}
	if strings.Contains(text, "zebraprint") || f.writes.Load() != 0 || out.Result.Details != nil {
		t.Fatalf("search did not bound output or changed execution/selection state: %+v", out)
	}
}
